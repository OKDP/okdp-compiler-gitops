package gitops

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/okdp/okdp-gitops/internal/gittest"
)

// A store of depth 1, over a server that fetches shallow (git
// http-backend): it reads the last commit of the branch, not its history,
// commits on it and pushes, replays a write that lost the race, refuses a
// change of a revision that moved; the remote keeps its whole history.
func TestGitStoreOfDepthOneFetchesTheLastCommitOnly(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(filepath.Join(root, "remote.git"), true); err != nil {
		t.Fatal(err)
	}
	url := gittest.Serve(t, root) + "/remote.git"
	ctx := context.Background()
	open := func(depth int) *GitStore {
		t.Helper()
		s, err := NewGitStore(GitOptions{URL: url, Branch: "main", AuthorName: "tester", AuthorEmail: "t@example.com", Depth: depth})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	write := func(s *GitStore, content string) string {
		t.Helper()
		rev, err := s.Update(ctx, Commit{Subject: content}, func(tx Tx) error {
			return tx.WriteFile("platform.yaml", []byte(content+"\n"))
		})
		if err != nil {
			t.Fatalf("write %s: %v", content, err)
		}
		return rev
	}
	full := open(0)
	first := write(full, "v0")
	write(full, "v1")
	write(full, "v2")

	shallow := open(1)
	if got := readFile(t, shallow, "platform.yaml"); got != "v2\n" {
		t.Fatalf("read %q", got)
	}
	if shallows, err := shallow.repo.Storer.Shallow(); err != nil || len(shallows) != 1 {
		t.Fatalf("shallow commits %v, %v: want the last one", shallows, err)
	}
	if _, err := shallow.repo.CommitObject(plumbing.NewHash(first)); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("the first commit was fetched: %v", err)
	}

	base := shallow.Head()
	if _, err := shallow.UpdateAt(ctx, base, Commit{Subject: "v3"}, func(tx Tx) error {
		return tx.WriteFile("platform.yaml", []byte("v3\n"))
	}); err != nil {
		t.Fatalf("a commit on the shallow clone: %v", err)
	}

	calls := 0
	if _, err := shallow.Update(ctx, Commit{Subject: "v5"}, func(tx Tx) error {
		calls++
		if calls == 1 {
			write(full, "v4")
		}
		return tx.WriteFile("other.yaml", []byte("v5\n"))
	}); err != nil || calls != 2 {
		t.Fatalf("the replay on the moved branch: %d calls, %v", calls, err)
	}

	moved := shallow.Head()
	write(full, "v6")
	if _, err := shallow.UpdateAt(ctx, moved, Commit{Subject: "v7"}, func(tx Tx) error {
		return tx.WriteFile("platform.yaml", []byte("v7\n"))
	}); !errors.Is(err, ErrMoved) {
		t.Fatalf("a change of a revision that moved: %v", err)
	}

	if err := full.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	head, err := full.repo.CommitObject(plumbing.NewHash(full.Head()))
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	if err := object.NewCommitPreorderIter(head, nil, nil).ForEach(func(c *object.Commit) error {
		subjects = append(subjects, c.Message)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(subjects), "[v6 v5 v4 v3 v2 v1 v0]"; got != want {
		t.Fatalf("the history of the remote %s, want %s", got, want)
	}
}

// A store with its clone in memory does not grow with every revision: a
// clone past its bound is replaced by a new one at the next fetch, which
// brings the last commit again, and the reads and the writes go on.
func TestAnInMemoryCloneIsReplacedPastItsBound(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(filepath.Join(root, "remote.git"), true); err != nil {
		t.Fatal(err)
	}
	url := gittest.Serve(t, root) + "/remote.git"
	ctx := context.Background()
	s, err := NewGitStore(GitOptions{URL: url, Branch: "main", AuthorName: "tester", AuthorEmail: "t@example.com", Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.maxClone = 6 << 10
	var largest int64
	for i := range 30 {
		content := fmt.Sprintf("revision %d\n%s", i, strings.Repeat(fmt.Sprintf("%d,", i), 300))
		if _, err := s.Update(ctx, Commit{Subject: fmt.Sprintf("v%d", i)}, func(tx Tx) error {
			return tx.WriteFile("platform.yaml", []byte(content))
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if got := readFile(t, s, "platform.yaml"); got != content {
			t.Fatalf("read %d: %q", i, got)
		}
		largest = max(largest, cloneSize(s.repo))
	}
	// A revision weighs some 2 KiB inflated: 30 of them are 60 KiB.
	if largest > 24<<10 {
		t.Errorf("the clone reached %d bytes, bound %d", largest, s.maxClone)
	}
	reader, err := NewGitStore(GitOptions{URL: url, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, reader, "platform.yaml"); !strings.HasPrefix(got, "revision 29\n") {
		t.Errorf("the remote holds %q", got[:20])
	}
}
