package gitops

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/okdp/okdp-gitops/treeedit"
)

func init() {
	// Serve file:// in process: the tests need no git binary, and go-git's own
	// server is stricter than git's, which is what a replay must survive.
	client.InstallProtocol("file", server.DefaultServer)
}

// newRemote creates an empty bare repository and returns its URL.
func newRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("init remote: %v", err)
	}
	return "file://" + dir
}

func newStore(t *testing.T, url string, onDisk bool) *GitStore {
	t.Helper()
	opts := GitOptions{URL: url, Branch: "main", PathPrefix: "gitops", AuthorName: "tester", AuthorEmail: "t@example.com"}
	if onDisk {
		opts.CloneDir = filepath.Join(t.TempDir(), "clone")
	}
	s, err := NewGitStore(opts)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func readFile(t *testing.T, s Store, p string) string {
	t.Helper()
	var out string
	err := s.View(context.Background(), func(r treeedit.Reader) error {
		data, err := r.ReadFile(p)
		out = string(data)
		return err
	})
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return out
}

func TestGitStoreWritesToAnEmptyRemoteAndReadsBack(t *testing.T) {
	for _, onDisk := range []bool{false, true} {
		url := newRemote(t)
		writer := newStore(t, url, onDisk)
		ctx := context.Background()

		rev, err := writer.Update(ctx, NewCommit("deploy", "demo/hive", alice), func(tx Tx) error {
			return tx.WriteFile("projects/demo/services/hive/instance.yaml", []byte("name: hive\n"))
		})
		if err != nil || rev == "" {
			t.Fatalf("first write: rev=%q err=%v", rev, err)
		}

		reader := newStore(t, url, !onDisk)
		if got := readFile(t, reader, "projects/demo/services/hive/instance.yaml"); got != "name: hive\n" {
			t.Fatalf("read back %q", got)
		}
		names := []string{}
		_ = reader.View(ctx, func(r treeedit.Reader) error {
			names, _ = r.ReadDir("projects/demo/services")
			return nil
		})
		if len(names) != 1 || names[0] != "hive" {
			t.Fatalf("ReadDir = %v", names)
		}

		// The prefix is honoured: the file sits under gitops/ in the repository.
		repo, _ := git.PlainOpen(strings.TrimPrefix(url, "file://"))
		head, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
		if err != nil {
			t.Fatalf("main branch not pushed: %v", err)
		}
		commit, _ := repo.CommitObject(head.Hash())
		if want := "okdp: deploy demo/hive by alice\n\nCo-Authored-By: okdp-gitops v0.9.0 <t@example.com>"; commit.Message != want {
			t.Fatalf("message = %q, want %q", commit.Message, want)
		}
		if commit.Author.Name != "Alice Martin" || commit.Author.Email != "alice@example.com" {
			t.Fatalf("author = %s <%s>, want the logged-in user", commit.Author.Name, commit.Author.Email)
		}
		if commit.Committer.Name != "tester" || commit.Committer.Email != "t@example.com" {
			t.Fatalf("committer = %s <%s>, want the service identity", commit.Committer.Name, commit.Committer.Email)
		}
		if _, err := commit.File("gitops/projects/demo/services/hive/instance.yaml"); err != nil {
			t.Fatalf("file not under the prefix: %v", err)
		}
	}
}

func TestGitStoreCommitsWithoutAUserAsTheServiceIdentity(t *testing.T) {
	url := newRemote(t)
	s := newStore(t, url, false)
	anonymous := Actor{Username: "anonymous", Name: "anonymous"}
	if _, err := s.Update(context.Background(), NewCommit("create connection", "demo/lake", anonymous), func(tx Tx) error {
		return tx.WriteFile("a.yaml", []byte("a\n"))
	}); err != nil {
		t.Fatal(err)
	}
	repo, _ := git.PlainOpen(strings.TrimPrefix(url, "file://"))
	head, _ := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	commit, _ := repo.CommitObject(head.Hash())
	if commit.Message != "okdp: create connection demo/lake by anonymous" {
		t.Errorf("message = %q, want the subject only", commit.Message)
	}
	if commit.Author.Name != "tester" || commit.Author.Email != "t@example.com" ||
		commit.Committer.Name != "tester" || commit.Committer.Email != "t@example.com" {
		t.Errorf("author %s <%s>, committer %s <%s>: want the service identity for both",
			commit.Author.Name, commit.Author.Email, commit.Committer.Name, commit.Committer.Email)
	}
}

func TestGitStoreReplaysAWriteThatLostTheRace(t *testing.T) {
	url := newRemote(t)
	ctx := context.Background()
	a := newStore(t, url, false)
	b := newStore(t, url, true)

	if _, err := a.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error {
		return tx.WriteFile("platform/catalog.yaml", []byte("categories: []\n"))
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	_, err := b.Update(ctx, Commit{Subject: "b writes"}, func(tx Tx) error {
		calls++
		if calls == 1 {
			// Another writer pushes between our fetch and our push.
			if _, err := a.Update(ctx, Commit{Subject: "a writes"}, func(tx Tx) error {
				return tx.WriteFile("projects/demo/project.yaml", []byte("name: demo\n"))
			}); err != nil {
				t.Fatalf("concurrent write: %v", err)
			}
		}
		return tx.WriteFile("projects/other/project.yaml", []byte("name: other\n"))
	})
	if err != nil {
		t.Fatalf("replayed write: %v", err)
	}
	if calls != 2 {
		t.Fatalf("the change ran %d times, want 2 (once lost, once replayed)", calls)
	}

	fresh := newStore(t, url, false)
	if got := readFile(t, fresh, "projects/demo/project.yaml"); got != "name: demo\n" {
		t.Fatalf("the concurrent write was lost: %q", got)
	}
	if got := readFile(t, fresh, "projects/other/project.yaml"); got != "name: other\n" {
		t.Fatalf("the replayed write is missing: %q", got)
	}
}

// A change computed from a revision is committed on it only: a branch that
// moved, before the change or during its push, fails it with ErrMoved,
// and nothing is pushed.
func TestGitStoreUpdateAtCommitsOnItsRevisionOnly(t *testing.T) {
	url := newRemote(t)
	ctx := context.Background()
	a := newStore(t, url, false)
	b := newStore(t, url, true)
	if b.Head() != "" {
		t.Fatalf("head of an unread store: %q", b.Head())
	}
	if _, err := a.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error {
		return tx.WriteFile("platform/catalog.yaml", []byte("categories: []\n"))
	}); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		if err := b.View(ctx, func(treeedit.Reader) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := b.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		return b.Head()
	}
	push := func(p string) {
		t.Helper()
		if _, err := a.Update(ctx, Commit{Subject: "a writes " + p}, func(tx Tx) error {
			return tx.WriteFile(p, []byte("x\n"))
		}); err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	write := func(tx Tx) error { return tx.WriteFile("compiled/b.yaml", []byte("b\n")) }

	// The branch moved after the read.
	base := read()
	push("projects/one/project.yaml")
	if _, err := b.UpdateAt(ctx, base, Commit{Subject: "b writes"}, write); !errors.Is(err, ErrMoved) {
		t.Fatalf("a moved branch: %v", err)
	}

	// The branch moved during the change, before its push.
	base = read()
	_, err := b.UpdateAt(ctx, base, Commit{Subject: "b writes"}, func(tx Tx) error {
		push("projects/two/project.yaml")
		return write(tx)
	})
	if !errors.Is(err, ErrMoved) {
		t.Fatalf("a branch that moved during the push: %v", err)
	}
	fresh := newStore(t, url, false)
	if err := fresh.View(ctx, func(r treeedit.Reader) error {
		if r.Exists("compiled/b.yaml") {
			t.Error("a refused change was pushed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// On the revision read, it is committed.
	base = read()
	rev, err := b.UpdateAt(ctx, base, Commit{Subject: "b writes"}, write)
	if err != nil || rev == base || b.Head() != rev {
		t.Fatalf("on its revision: %q %v, head %q", rev, err, b.Head())
	}
	if got := readFile(t, newStore(t, url, false), "compiled/b.yaml"); got != "b\n" {
		t.Fatalf("compiled/b.yaml = %q", got)
	}
}

func TestGitStoreFailedChangeLeavesNothingBehind(t *testing.T) {
	url := newRemote(t)
	ctx := context.Background()
	s := newStore(t, url, true)
	if _, err := s.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error {
		return tx.WriteFile("a.yaml", []byte("a\n"))
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	_, err := s.Update(ctx, Commit{Subject: "fails"}, func(tx Tx) error {
		_ = tx.WriteFile("b.yaml", []byte("b\n"))
		_ = tx.Remove("a.yaml")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	_ = s.View(ctx, func(r treeedit.Reader) error {
		if r.Exists("b.yaml") || !r.Exists("a.yaml") {
			t.Fatalf("the failed change leaked into reads")
		}
		return nil
	})
}

func TestGitStoreNoChangeCommitsNothing(t *testing.T) {
	url := newRemote(t)
	ctx := context.Background()
	s := newStore(t, url, false)
	rev1, err := s.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error { return tx.WriteFile("a.yaml", []byte("a\n")) })
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := s.Update(ctx, Commit{Subject: "same"}, func(tx Tx) error { return tx.WriteFile("a.yaml", []byte("a\n")) })
	if err != nil {
		t.Fatal(err)
	}
	if rev1 != rev2 {
		t.Fatalf("an empty change produced a commit: %s then %s", rev1, rev2)
	}
}

func TestCleanPathRefusesEscapes(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "../x", "a/../../x"} {
		if _, err := cleanPath(p); err == nil {
			t.Errorf("cleanPath(%q) accepted", p)
		}
	}
}
