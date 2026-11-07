// Package gitops keeps the desired state of the platform in Git: the tree
// of its deployments, files in Git repositories, and the output a compile
// writes. The cluster is never written to directly; a GitOps engine (Flux
// or Argo CD) reconciles what is committed here.
//
// The package is the Git around okdp-compiler, which owns every rule of
// the tree, of its edits, of the compile and of the output:
//   - Store is a transactional view of a Git working tree (GitStore talks to a
//     real remote through go-git, MemoryStore backs the tests), Repositories
//     the parts of a Topology, each in its repository; a Store's reads are
//     an okdp-compiler treeedit.Reader, a snapshot no write waits on nor
//     changes, its transactions a treeedit.Writer;
//   - Compiler compiles the tree a Store holds with okdp-compiler: its parts
//     and the catalogs of treeedit.CatalogsFile, fetched, given to
//     deploy.Build;
//   - Deployments commits the edits of the tree, okdp-compiler's (package
//     treeedit), each a transaction checked by the compile of the whole
//     tree before and after it (Apply, treeedit.Apply), reads the tree
//     with treeedit's readers, creates the repository of a new project of
//     a split topology (ForgejoClient), and writes the output,
//     compiled/, the files of okdp-compiler's Flux and Argo CD targets
//     (WriteCompiled, deploy.TreeFiles);
//   - Credentials give every credential sent, to the repositories, the
//     catalogs and the Forgejo API, one sent over http:// in clear to the
//     hosts they allow only.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"unicode"

	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/internal/buildinfo"
)

// ErrConflict is returned when a write kept losing the race against other
// writers of the same branch, after every retry.
var ErrConflict = errors.New("the deployments repository kept changing under the write, giving up")

// ErrMoved is returned by GitStore.UpdateAt when the branch is no longer at
// the revision the change was computed from.
var ErrMoved = errors.New("the branch moved since the change was computed")

// ErrSymlink is returned for a path that is, or goes through, a symbolic
// link committed in the repository: a link may reach outside it, or make
// one file two; the tree holds regular files and directories only, and
// a read, a write or a removal through a link is refused (SymlinkError).
// It is okdp-compiler's, which refuses the tree that holds one
// (treeedit.ErrSymlink).
var ErrSymlink = treeedit.ErrSymlink

// SymlinkError is the error of a path that reaches the symbolic link link
// of the repository repo: "<repo>: <link>: " then ErrSymlink, which it
// wraps.
func SymlinkError(repo, link string) error {
	return fmt.Errorf("%s: %s: %w", repo, link, ErrSymlink)
}

// ErrTooLarge is returned for a file of the tree over MaxTreeFileSize, or
// that would take the tree over MaxTreeSize: refused by its size, before
// it is read (TooLargeError). It is okdp-compiler's, which bounds the
// tree it compiles (treeedit.ErrTooLarge).
var ErrTooLarge = treeedit.ErrTooLarge

// TooLargeError is the error of the file p of size bytes, over
// MaxTreeFileSize; it wraps ErrTooLarge.
func TooLargeError(p string, size int64) error { return treeedit.TooLargeError(p, size) }

// treeTooLargeError is the error of a tree over its bound of limit bytes;
// it wraps ErrTooLarge.
func treeTooLargeError(limit int64) error { return treeedit.TreeTooLargeError(limit) }

// Tx is a write transaction on the desired state, a treeedit.Writer: its
// reads (treeedit.Reader, paths slash-separated and relative to the root
// of the store, a missing file an error wrapping treeedit.ErrNotFound)
// see its writes; its changes are committed together, or not at all.
type Tx interface {
	treeedit.Reader
	WriteFile(p string, data []byte) error
	// Remove deletes a file or a whole directory. Removing nothing is not an error.
	Remove(p string) error
}

// Store is the desired-state store.
type Store interface {
	// View runs fn against a consistent, recently refreshed snapshot.
	// Views run at once: none waits on another, nor on a write.
	View(ctx context.Context, fn func(r treeedit.Reader) error) error
	// Update runs fn on top of the latest remote revision and commits its
	// changes as commit describes. When another writer pushed in between, the change
	// is replayed on the new revision (the equivalent of a rebase: fn is the
	// change, not a patch) and pushed again. A transaction changing nothing
	// commits nothing. Returns the revision holding the change.
	Update(ctx context.Context, commit Commit, fn func(tx Tx) error) (string, error)
}

// Snapshot is the tree of a store at one revision, immutable: a
// treeedit.Reader any number of goroutines read at once, for as long as
// they keep it, while the store fetches and writes.
type Snapshot interface {
	treeedit.Reader
	// Revision is the commit read, "" for a branch that does not exist
	// yet.
	Revision() string
}

// Snapshotter is a store whose reads are snapshots (GitStore): the reads
// of a view of Repositories each keep the snapshot of its repository
// they read first, one revision of each repository for the whole view.
type Snapshotter interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// cleanPath validates a repository-relative path. Absolute paths and paths
// escaping the prefix are refused: every path is built by this package from
// validated names, so one reaching here is a bug worth failing on.
func cleanPath(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("absolute path %q in the deployments repository", p)
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q escapes the deployments repository", p)
	}
	return cleaned, nil
}

// Signature is a Git identity: a commit author or committer.
type Signature struct {
	Name  string
	Email string
}

// Actor is who asks for a change, as the commit records it: Username goes in
// the subject, Name and Email make the commit author.
type Actor struct {
	Username string
	Name     string
	// Email is empty when unknown (a token without an email claim, no
	// authentication): the committer then authors the commit.
	Email string
}

// Program names the program that commits and its version, for the co-author
// trailer of Message: by default the running binary (the last element of its
// main module path, fallback without build information) and the version of
// package buildinfo. A program embedding the package with its own version
// (the control plane) sets it.
var Program = func(fallback string) (name, version string) {
	return buildinfo.Name(fallback), buildinfo.CurrentVersion()
}

// Commit is a change the Store writes: its subject line and who made it. The
// Store is the committer, with the service identity it is configured with
// (GITOPS_AUTHOR_NAME, GITOPS_AUTHOR_EMAIL).
type Commit struct {
	// Subject is the first line of the message.
	Subject string
	// Author is the logged-in user, nil when unknown: the committer then
	// authors the commit too.
	Author *Signature
}

// NewCommit describes a console action, as the shared contract fixes its
// subject: "okdp: <action> <project>/<instance> by <user>". The logged-in
// user authors the commit with the name and email claims of the token; the
// control plane commits it and names itself as co-author (see Message):
//
//	Author:    Alice Martin <alice@example.com>
//	Committer: OKDP control plane <okdp-control-plane@okdp.io>
//
//	okdp: deploy demo/trino by alice
//
//	Co-Authored-By: okdp-control-plane-server v0.9.0 <okdp-control-plane@okdp.io>
//
// An author needs an email: without one (a token lacking the email claim, or
// authentication disabled) the service identity authors the commit, the user
// is named in the subject only, and a warning is logged once per user. Name
// and email are sanitised so a crafted claim cannot add lines.
func NewCommit(action, target string, actor Actor) Commit {
	user := stripUnsafe(actor.Username)
	if user == "" {
		user = "unknown"
	}
	commit := Commit{Subject: fmt.Sprintf("okdp: %s %s by %s", action, target, user)}

	email := stripUnsafe(actor.Email)
	if !looksLikeEmail(email) {
		warnNoAuthor(user, actor.Email)
		return commit
	}
	name := stripUnsafe(actor.Name)
	if name == "" {
		name = user
	}
	commit.Author = &Signature{Name: name, Email: email}
	return commit
}

// Message returns the full commit message. A commit authored by a user names
// the control plane, which committed it, as co-author:
// "Co-Authored-By: <program> v<version> <committer email>", the program and
// version being those Program names. A commit the service identity authors
// itself has the subject only: the trailer would repeat the author.
func (c Commit) Message(committer Signature) string {
	if c.Author == nil {
		return c.Subject
	}
	name, version := Program(committer.Name)
	return fmt.Sprintf("%s\n\nCo-Authored-By: %s v%s <%s>", c.Subject,
		stripUnsafe(name), stripUnsafe(version), stripUnsafe(committer.Email))
}

// DefaultCommitter is the service identity used when none is configured.
var DefaultCommitter = Signature{Name: "OKDP control plane", Email: "okdp-control-plane@okdp.io"}

// stripUnsafe removes what could break out of a message line or an
// identity's <email> part: control characters (CR and LF included), '<' and '>'.
func stripUnsafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// looksLikeEmail accepts exactly one '@' with something on both sides and no
// white space.
func looksLikeEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && domain != "" &&
		!strings.Contains(domain, "@") &&
		!strings.ContainsFunc(s, unicode.IsSpace)
}

// warnedActors remembers who was already reported as lacking an email.
var warnedActors sync.Map

func warnNoAuthor(user, email string) {
	if _, seen := warnedActors.LoadOrStore(user, struct{}{}); seen {
		return
	}
	reason := "the token has no email claim (Keycloak: add the \"email\" client scope)"
	switch {
	case email != "":
		reason = "its email claim is not a valid address"
	case user == "anonymous":
		reason = "no logged-in user (authentication disabled)"
	}
	slog.Warn(fmt.Sprintf("gitops: commits by %q are authored by the control plane, not the user: %s", user, reason))
}
