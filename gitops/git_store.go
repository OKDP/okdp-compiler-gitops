package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/internal/gitfetch"
)

// GitOptions configures a GitStore.
type GitOptions struct {
	// URL of the deployments repository (https://, ssh://, git@host:path or file://).
	URL string
	// Branch the desired state lives on.
	Branch string
	// PathPrefix is the directory of the repository the store is rooted
	// at, empty for the root.
	PathPrefix string
	// Auth authenticates the fetches and pushes, nil for anonymous access
	// (Credentials.For).
	Auth transport.AuthMethod
	// AuthorName and AuthorEmail are the service identity: the committer of
	// every commit, and its author when the commit names no user
	// (GITOPS_AUTHOR_NAME, GITOPS_AUTHOR_EMAIL).
	AuthorName  string
	AuthorEmail string
	// CloneDir holds the local clone, a cache only (an emptyDir in the pod).
	// Empty keeps it in memory.
	CloneDir string
	// MaxRetries bounds the replays of a write that lost a race.
	MaxRetries int
	// RefreshInterval is how stale a read may be before it fetches again.
	RefreshInterval time.Duration
	// Depth bounds the commits of the branch a fetch brings, 0 for its
	// whole history. Nothing reads the history: a read is of the branch's
	// last commit, a write commits on it and pushes (okdp-gitops compile
	// fetches with a depth of 1). A server without shallow fetches gets a
	// fetch of the whole history.
	Depth int
	// CABundle is PEM certificates an https remote is trusted with beside
	// the system's (Credentials.CABundle).
	CABundle []byte
	// Layout, when set, names the paths of the repository (from its root,
	// "." for all of it) a read needs, as Topology.LayoutPaths does: the
	// snapshot reads those and nothing beside them, a file beside them is
	// not even listed. It is called each time a snapshot is built, and a
	// layout that changed is read again from the clone (Reread). Nil reads
	// every path under PathPrefix. Whatever the layout, a file over
	// MaxTreeFileSize, or that would take the files read over MaxTreeSize,
	// is not read: it is listed, and reading it fails (ErrTooLarge).
	Layout func() []string
}

// GitStore is a Store backed by a Git remote through go-git. A write
// fetches, applies, commits and pushes as one step, one write at a time; a
// read takes no lock: it reads a snapshot of the branch's last commit,
// immutable (Snapshot), which a fetch or a write replaces, never changes.
type GitStore struct {
	opts GitOptions

	// mu serialises what touches the clone: a fetch, a write. Readers
	// never wait on it but for the very first read (Snapshot).
	mu       sync.Mutex
	repo     *git.Repository
	worktree *git.Worktree
	root     billy.Filesystem

	// snap is the tree at the revision last fetched or pushed, nil before
	// the first read.
	snap atomic.Pointer[gitSnapshot]

	// maxFile and maxTree are the bounds of a snapshot (MaxTreeFileSize,
	// MaxTreeSize), maxClone what a clone in memory may hold
	// (MaxCloneSize): the tests' to lower.
	maxFile, maxTree, maxClone int64
}

// MaxCloneSize is the bytes the objects of a GitStore's clone in memory may
// hold, inflated, before the next fetch starts a new clone: each revision
// fetched adds its objects, and nothing drops the older ones.
const MaxCloneSize = 256 << 20

// NewGitStore prepares the local clone, on disk under CloneDir (its packs
// stay compressed, a blob over MaxTreeFileSize is never read into memory
// to know its size), in memory without it: a fetch then holds every blob
// inflated. It does not reach the remote: the first read or write does, so
// a server can start while Git is unreachable.
func NewGitStore(opts GitOptions) (*GitStore, error) {
	if opts.URL == "" {
		return nil, errors.New("the deployments repository URL is required")
	}
	if opts.Branch == "" {
		opts.Branch = "main"
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 5
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 5 * time.Second
	}
	if opts.AuthorName == "" {
		opts.AuthorName = DefaultCommitter.Name
	}
	if opts.AuthorEmail == "" {
		opts.AuthorEmail = DefaultCommitter.Email
	}
	prefix, err := cleanPath(strings.Trim(opts.PathPrefix, "/"))
	if err != nil {
		return nil, err
	}
	opts.PathPrefix = prefix

	var repo *git.Repository
	if opts.CloneDir == "" {
		repo, err = git.Init(memory.NewStorage(), memfs.New())
	} else {
		repo, err = openClone(opts.CloneDir)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to prepare the local clone: %w", err)
	}

	worktree, root, err := declareClone(repo, opts.URL, prefix)
	if err != nil {
		return nil, err
	}

	return &GitStore{opts: opts, repo: repo, worktree: worktree, root: root, maxFile: MaxTreeFileSize, maxTree: MaxTreeSize, maxClone: MaxCloneSize}, nil
}

// declareClone declares the remote of the clone repo, on every start: the
// URL may have changed since the clone directory was last used. It returns
// its working tree, and its root, at the path prefix.
func declareClone(repo *git.Repository, url, prefix string) (*git.Worktree, billy.Filesystem, error) {
	_ = repo.DeleteRemote("origin")
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		return nil, nil, fmt.Errorf("failed to declare the remote: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, nil, err
	}
	root := worktree.Filesystem
	if prefix != "." {
		if root, err = worktree.Filesystem.Chroot(prefix); err != nil {
			return nil, nil, err
		}
	}
	return worktree, root, nil
}

// trimClone replaces a clone in memory that grew past maxClone by an empty
// one: the next fetch brings the last commit again. The snapshot readers
// hold is a copy, no object of the clone.
func (s *GitStore) trimClone() error {
	if s.opts.CloneDir != "" || cloneSize(s.repo) <= s.maxClone {
		return nil
	}
	repo, err := git.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		return err
	}
	worktree, root, err := declareClone(repo, s.opts.URL, s.opts.PathPrefix)
	if err != nil {
		return err
	}
	s.repo, s.worktree, s.root = repo, worktree, root
	return nil
}

// cloneSize is the bytes the objects of repo hold inflated.
func cloneSize(repo *git.Repository) int64 {
	iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return 0
	}
	var size int64
	_ = iter.ForEach(func(o plumbing.EncodedObject) error {
		size += o.Size()
		return nil
	})
	return size
}

// openClone opens, or creates, the clone in dir. Its storage hands out an
// object over MaxTreeFileSize as a stream of the pack, not as bytes.
func openClone(dir string) (*git.Repository, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	worktree := osfs.New(dir)
	dot, err := worktree.Chroot(git.GitDirName)
	if err != nil {
		return nil, err
	}
	storer := filesystem.NewStorageWithOptions(dot, cache.NewObjectLRUDefault(), filesystem.Options{LargeObjectThreshold: MaxTreeFileSize})
	repo, err := git.Open(storer, worktree)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.Init(storer, worktree)
	}
	return repo, err
}

func (s *GitStore) branchRef() plumbing.ReferenceName {
	return plumbing.NewBranchReferenceName(s.opts.Branch)
}

func (s *GitStore) remoteRef() plumbing.ReferenceName {
	return plumbing.NewRemoteReferenceName("origin", s.opts.Branch)
}

// fetch brings the remote branch into the clone, the local branch made
// it, and returns its revision: "" for an empty remote (or a missing
// branch), a valid, empty desired state the first write creates. The
// working tree is left as it is: reads go to the snapshot, a write resets
// it (reset).
func (s *GitStore) fetch(ctx context.Context) (string, error) {
	// Forget unpushed local commits first: they are either pushed already or
	// about to be replayed, and advertising them as "haves" confuses some
	// servers into failing the fetch.
	if ref, err := s.repo.Reference(s.remoteRef(), true); err == nil {
		_ = s.repo.Storer.SetReference(plumbing.NewHashReference(s.branchRef(), ref.Hash()))
	} else {
		_ = s.repo.Storer.RemoveReference(s.branchRef())
	}

	refSpec := config.RefSpec(fmt.Sprintf("+%s:%s", s.branchRef(), s.remoteRef()))
	err := gitfetch.Fetch(ctx, s.repo, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       s.opts.Auth,
		Force:      true,
		Depth:      s.opts.Depth,
		Tags:       git.NoTags,
		CABundle:   s.opts.CABundle,
	})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	case errors.Is(err, transport.ErrEmptyRemoteRepository), errors.Is(err, git.NoMatchingRefSpecError{}):
		// Nothing upstream: forget any local commit.
		_ = s.repo.Storer.RemoveReference(s.remoteRef())
		_ = s.repo.Storer.RemoveReference(s.branchRef())
		return "", s.repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, s.branchRef()))
	default:
		return "", fmt.Errorf("failed to fetch %s from the deployments repository: %w", s.opts.Branch, err)
	}

	if err := s.repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, s.branchRef())); err != nil {
		return "", err
	}
	ref, err := s.repo.Reference(s.remoteRef(), true)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the fetched branch: %w", err)
	}
	if err := s.repo.Storer.SetReference(plumbing.NewHashReference(s.branchRef(), ref.Hash())); err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// reset makes the working tree exactly rev, as fetch returned it,
// dropping whatever a failed write left behind: what a write changes.
func (s *GitStore) reset(rev string) error {
	if rev != "" {
		if err := s.worktree.Reset(&git.ResetOptions{Commit: plumbing.NewHash(rev), Mode: git.HardReset}); err != nil {
			return fmt.Errorf("failed to reset the local clone: %w", err)
		}
	} else if err := s.repo.Storer.SetIndex(&index.Index{Version: 2}); err != nil {
		// Nothing upstream: start from an empty tree.
		return err
	}
	if err := s.worktree.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("failed to clean the local clone: %w", err)
	}
	return nil
}

// install makes the tree of rev, the remote seen at it at at, the
// snapshot readers read: the current one again when it is of rev, a new
// one, built from the commit, otherwise.
func (s *GitStore) install(rev string, at time.Time) (*gitSnapshot, error) {
	cur := s.snap.Load()
	lim := s.limits()
	if cur != nil && cur.rev == rev && cur.layout == lim.key() {
		next := cur.seen(at)
		s.snap.Store(next)
		return next, nil
	}
	next, err := buildSnapshot(s.repo, s.opts.URL, s.opts.PathPrefix, rev, cur, lim)
	if err != nil {
		return nil, err
	}
	next.at = at
	s.snap.Store(next)
	return next, nil
}

// limits are the bounds of a snapshot now: the layout is asked each time.
func (s *GitStore) limits() snapshotLimits {
	lim := snapshotLimits{file: s.maxFile, tree: s.maxTree}
	if s.opts.Layout != nil {
		lim.layout = s.opts.Layout()
		if lim.layout == nil {
			lim.layout = []string{}
		}
	}
	return lim
}

// Reread reads the snapshot again for the layout as it is now
// (GitOptions.Layout), from the clone: the same revision, nothing fetched,
// so what was read of it before and what is read now are of one commit.
// It returns the new snapshot; nil before any read.
func (s *GitStore) Reread() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.Load()
	if cur == nil {
		return nil, nil
	}
	snap, err := s.install(cur.rev, cur.at)
	if err != nil {
		return nil, err
	}
	if snap.err != nil {
		return nil, snap.err
	}
	return snap, nil
}

// sync fetches the branch and makes its last commit the snapshot.
func (s *GitStore) sync(ctx context.Context) (*gitSnapshot, error) {
	at := time.Now()
	if err := s.trimClone(); err != nil {
		return nil, err
	}
	rev, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return s.install(rev, at)
}

// Sync brings the snapshot to the latest remote revision now, whatever
// the refresh interval: a write across repositories reads each one fresh.
func (s *GitStore) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.sync(ctx)
	return err
}

func (s *GitStore) fresh(snap *gitSnapshot) bool {
	return snap != nil && time.Since(snap.at) < s.opts.RefreshInterval
}

// Snapshot returns the tree at the branch's last commit, immutable, read
// by any number of readers at once: the snapshot of the last fetch or
// write, fetched again first when it is older than RefreshInterval. A
// read never waits on another's fetch or on a write but the first one:
// while the clone is busy, the snapshot it is replacing is read. A failed
// refresh keeps serving the last snapshot, with a warning, rather than
// failing every page while Git is briefly unreachable. A prefix that is,
// or goes through, a symbolic link fails it (ErrSymlink).
func (s *GitStore) Snapshot(ctx context.Context) (Snapshot, error) {
	snap := s.snap.Load()
	if !s.fresh(snap) {
		var err error
		if snap, err = s.refresh(ctx, snap); err != nil {
			return nil, err
		}
	}
	if snap.err != nil {
		return nil, snap.err
	}
	return snap, nil
}

// refresh fetches the branch for a reader whose snapshot, last, is stale
// (nil before the first read).
func (s *GitStore) refresh(ctx context.Context, last *gitSnapshot) (*gitSnapshot, error) {
	if last == nil {
		s.mu.Lock()
	} else if !s.mu.TryLock() {
		// A fetch or a write is under way, which replaces the snapshot.
		return last, nil
	}
	defer s.mu.Unlock()
	if cur := s.snap.Load(); s.fresh(cur) {
		return cur, nil // fetched while this reader waited
	}
	snap, err := s.sync(ctx)
	if err != nil {
		if last != nil {
			slog.Warn("Could not refresh the deployments repository, serving the last known state", "error", err)
			return last, nil
		}
		return nil, err
	}
	return snap, nil
}

// View runs fn on a Snapshot: no lock is held while it runs.
func (s *GitStore) View(ctx context.Context, fn func(r treeedit.Reader) error) error {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	return fn(snap)
}

// tx is a transaction of the working tree, rooted at the path prefix.
func (s *GitStore) tx() *fsTx {
	return &fsTx{fs: s.root, url: s.opts.URL, prefix: s.opts.PathPrefix}
}

// checkPrefix refuses a path prefix that is, or goes through, a symbolic
// link of the clone: the root of the tree would be elsewhere.
func (s *GitStore) checkPrefix() error {
	link, err := symlinkOn(s.worktree.Filesystem, s.opts.PathPrefix)
	if err != nil || link == "" {
		return err
	}
	return SymlinkError(s.opts.URL, link)
}

func (s *GitStore) Update(ctx context.Context, commit Commit, fn func(tx Tx) error) (string, error) {
	return s.update(ctx, nil, commit, fn)
}

// Head returns the revision of the branch as the store last read or wrote
// it, the revision of its snapshot: "" before any read or when the branch
// does not exist (an empty remote).
func (s *GitStore) Head() string {
	if snap := s.snap.Load(); snap != nil {
		return snap.rev
	}
	return ""
}

// UpdateAt is Update for a change computed from revision base (Head): it
// commits on base only. When the branch has moved, before the change or
// before its push, it fails with an error wrapping ErrMoved rather than
// replaying fn on the new revision: the caller computes the change again
// from it (okdp-gitops compile, whose change is the compiled tree of the
// sources it read).
func (s *GitStore) UpdateAt(ctx context.Context, base string, commit Commit, fn func(tx Tx) error) (string, error) {
	return s.update(ctx, &base, commit, fn)
}

// update is Update, and UpdateAt when base is set.
func (s *GitStore) update(ctx context.Context, base *string, commit Commit, fn func(tx Tx) error) (string, error) {
	committerID := Signature{Name: s.opts.AuthorName, Email: s.opts.AuthorEmail}
	authorID := committerID
	if commit.Author != nil {
		authorID = *commit.Author
	}
	message := commit.Message(committerID)

	s.mu.Lock()
	defer s.mu.Unlock()

	for attempt := 0; attempt <= s.opts.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		snap, err := s.sync(ctx)
		if err != nil {
			return "", err
		}
		synced := snap.rev
		if base != nil && synced != *base {
			return "", fmt.Errorf("%s: %w", s.opts.URL, ErrMoved)
		}
		if err := s.reset(synced); err != nil {
			return "", err
		}
		if err := s.checkPrefix(); err != nil {
			return "", err
		}
		// A failed change leaves the working tree half-written, which no
		// read sees and the next write resets.
		if err := fn(s.tx()); err != nil {
			return "", err
		}

		if err := s.worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return "", fmt.Errorf("failed to stage the change: %w", err)
		}
		status, err := s.worktree.Status()
		if err != nil {
			return "", err
		}
		if status.IsClean() {
			return synced, nil
		}

		now := time.Now()
		hash, err := s.worktree.Commit(message, &git.CommitOptions{
			Author:    &object.Signature{Name: authorID.Name, Email: authorID.Email, When: now},
			Committer: &object.Signature{Name: committerID.Name, Email: committerID.Email, When: now},
		})
		if err != nil {
			return "", fmt.Errorf("failed to commit: %w", err)
		}

		push := &git.PushOptions{
			RemoteName: "origin",
			RefSpecs:   []config.RefSpec{config.RefSpec(fmt.Sprintf("%s:%s", s.branchRef(), s.branchRef()))},
			Auth:       s.opts.Auth,
			CABundle:   s.opts.CABundle,
		}
		if synced != "" {
			// The branch must still be the revision synced: one that moved
			// is a lost race, which a shallow clone cannot tell from its
			// history (the commits between are not fetched).
			push.RequireRemoteRefs = []config.RefSpec{config.RefSpec(synced + ":" + s.branchRef().String())}
		}
		err = s.repo.PushContext(ctx, push)
		if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
			// The remote now holds exactly this commit.
			_ = s.repo.Storer.SetReference(plumbing.NewHashReference(s.remoteRef(), hash))
			if _, err := s.install(hash.String(), now); err != nil {
				slog.Warn("Could not read back the commit pushed, the next read fetches it", "error", err)
			}
			slog.Info("Committed to the deployments repository", "revision", hash.String(), "author", authorID.Name, "message", commit.Subject)
			return hash.String(), nil
		}
		if isNonFastForward(err) && base != nil {
			return "", fmt.Errorf("%s: %w (during the push)", s.opts.URL, ErrMoved)
		}
		if isNonFastForward(err) {
			slog.Info("The deployments repository moved during the write, replaying it on the new revision", "error", err, "attempt", attempt+1)
			continue
		}
		return "", fmt.Errorf("failed to push to the deployments repository: %w", err)
	}
	return "", ErrConflict
}

// isNonFastForward recognises a push refused because the branch moved. go-git
// detects it itself when it sees the remote ref first (not the revision the
// push requires, or not an ancestor); a server may also refuse it, and only
// says so in words.
func isNonFastForward(err error) bool {
	if errors.Is(err, git.ErrNonFastForwardUpdate) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"non-fast-forward", "required to be", "fetch first", "reference has changed concurrently", "failed to lock", "cannot lock ref"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// symlinkOn returns the first component of p, a clean path of fsys, that
// is a symbolic link, "" when none is: a link of the working tree is
// what Git checks out of a tree entry of mode 120000, in a clone on disk
// as in memory. A component that does not exist, or that is below a file,
// ends the search: nothing below it can be a link.
func symlinkOn(fsys billy.Filesystem, p string) (string, error) {
	if p == "." {
		return "", nil
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		prefix := path.Join(parts[:i+1]...)
		info, err := fsys.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return prefix, nil
		}
	}
	return "", nil
}

// fsTx is a Tx over a billy filesystem rooted at the path prefix of the
// repository url. Every path is validated (cleanPath), and refused when it
// is, or goes through, a symbolic link (symlinkOn): a link may reach
// outside the repository.
type fsTx struct {
	fs     billy.Filesystem
	url    string
	prefix string
}

// resolve validates p: a repository-relative path, no symbolic link on it.
func (t *fsTx) resolve(p string) (string, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	link, err := symlinkOn(t.fs, cleaned)
	if err != nil {
		return "", err
	}
	if link != "" {
		return "", SymlinkError(t.url, path.Join(t.prefix, link))
	}
	return cleaned, nil
}

func (t *fsTx) ReadFile(p string) ([]byte, error) {
	cleaned, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	data, err := util.ReadFile(t.fs, cleaned)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", cleaned, treeedit.ErrNotFound)
	}
	return data, err
}

func (t *fsTx) ReadDir(p string) ([]string, error) {
	cleaned, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	entries, err := t.fs.ReadDir(cleaned)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (t *fsTx) Exists(p string) bool {
	cleaned, err := t.resolve(p)
	if err != nil {
		return false
	}
	_, err = t.fs.Stat(cleaned)
	return err == nil
}

func (t *fsTx) WriteFile(p string, data []byte) error {
	cleaned, err := t.resolve(p)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(cleaned); dir != "." {
		if err := t.fs.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return util.WriteFile(t.fs, cleaned, data, 0o644)
}

func (t *fsTx) Remove(p string) error {
	cleaned, err := t.resolve(p)
	if err != nil {
		return err
	}
	if cleaned == "." {
		return errors.New("refusing to remove the whole deployments tree")
	}
	if !t.Exists(cleaned) {
		return nil
	}
	return util.RemoveAll(t.fs, cleaned)
}
