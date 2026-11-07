package gitops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
)

// done fails the test when ch is not closed in time: what it waits on
// is blocked.
func done(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: still blocked", what)
	}
}

// async runs fn in a goroutine; the channel is closed when it returns.
func async(fn func()) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		fn()
	}()
	return ch
}

func seedStore(t *testing.T, files map[string]string) (*GitStore, string) {
	t.Helper()
	url := newRemote(t)
	s := newStore(t, url, false)
	if _, err := s.Update(context.Background(), Commit{Subject: "seed"}, func(tx Tx) error {
		for p, content := range files {
			if err := tx.WriteFile(p, []byte(content)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, url
}

// A compile that takes its time holds no lock of the store: the reads
// and the writes of the others go on meanwhile; a compile of the same
// tree at once waits on it and shares its result, one compile; a compile
// of the tree a write changed is another.
func TestReadsAndWritesDoNotWaitOnASlowCompile(t *testing.T) {
	ctx := context.Background()
	s, _ := seedStore(t, map[string]string{"platform.yaml": "global: {}\n", "projects/demo/project.yaml": "{}\n"})
	var builds atomic.Int32
	started, release, waiting := make(chan struct{}, 4), make(chan struct{}), make(chan struct{}, 4)
	c := NewCompiler(&catalog.Loader{})
	c.build = func(ctx context.Context, _ []compiler.Repository, _ compiler.BuildOptions) (*compiler.Tree, error) {
		builds.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &compiler.Tree{}, nil
	}
	c.waiting = func() { waiting <- struct{}{} }
	d := &Deployments{Store: s, Compiler: c}

	var first, second *compiler.Tree
	a := async(func() {
		var err error
		if first, err = d.Tree(ctx); err != nil {
			t.Errorf("the slow compile: %v", err)
		}
	})
	done(t, chanOf(started), "the first compile")
	b := async(func() {
		var err error
		if second, err = d.Tree(ctx); err != nil {
			t.Errorf("the compile of the same tree: %v", err)
		}
	})
	done(t, chanOf(waiting), "the compile of the same tree, waiting on the first")

	done(t, async(func() {
		projects, err := d.ListProjects(ctx)
		if err != nil || len(projects) != 1 || projects[0].Name != "demo" {
			t.Errorf("a read during the compile: %+v, %v", projects, err)
		}
	}), "a read during the compile")
	done(t, async(func() {
		if _, err := s.Update(ctx, Commit{Subject: "a write"}, func(tx Tx) error {
			return tx.WriteFile("platform.yaml", []byte("global: {okdp: {}}\n"))
		}); err != nil {
			t.Errorf("a write during the compile: %v", err)
		}
	}), "a write during the compile")

	close(release)
	done(t, a, "the first compile, released")
	done(t, b, "the second compile, released")
	if first == nil || first != second || builds.Load() != 1 {
		t.Fatalf("the compiles of one tree at once: %d builds, %p and %p, want one shared", builds.Load(), first, second)
	}
	third, err := d.Tree(ctx)
	if err != nil || third == first || builds.Load() != 2 {
		t.Fatalf("the compile of the tree written: %d builds, %p (%v), want a new one", builds.Load(), third, err)
	}
	if again, err := d.Tree(ctx); err != nil || again != third || builds.Load() != 2 {
		t.Fatalf("the same tree again: %d builds, %v, want the one kept", builds.Load(), err)
	}
}

// chanOf closes a channel once ch receives.
func chanOf(ch <-chan struct{}) <-chan struct{} {
	return async(func() { <-ch })
}

// A view reads the revision it started on to its end: a write of the
// store in the middle of it, its own or another clone's, is not seen;
// the next view sees it. A write does not wait on a view either.
func TestAViewKeepsItsRevisionWhileAWriteCommits(t *testing.T) {
	ctx := context.Background()
	s, url := seedStore(t, map[string]string{"platform.yaml": "v1\n", "projects/demo/project.yaml": "{}\n"})
	other := newStore(t, url, true)
	check := func(r treeedit.Reader, platform, projects string) {
		t.Helper()
		data, err := r.ReadFile("platform.yaml")
		if err != nil || string(data) != platform {
			t.Errorf("platform.yaml = %q, %v, want %q", data, err, platform)
		}
		names, err := r.ReadDir("projects")
		if err != nil || strings.Join(names, ",") != projects {
			t.Errorf("projects = %v, %v, want %s", names, err, projects)
		}
	}
	rev := s.Head()
	err := s.View(ctx, func(r treeedit.Reader) error {
		check(r, "v1\n", "demo")
		if _, err := s.Update(ctx, Commit{Subject: "v2"}, func(tx Tx) error {
			if err := tx.WriteFile("platform.yaml", []byte("v2\n")); err != nil {
				return err
			}
			if err := tx.WriteFile("projects/lab/project.yaml", []byte("{}\n")); err != nil {
				return err
			}
			return tx.Remove("projects/demo")
		}); err != nil {
			t.Fatalf("a write during a view: %v", err)
		}
		if _, err := other.Update(ctx, Commit{Subject: "v3"}, func(tx Tx) error {
			return tx.WriteFile("platform.yaml", []byte("v3\n"))
		}); err != nil {
			t.Fatalf("another clone's write during a view: %v", err)
		}
		if err := s.Sync(ctx); err != nil {
			t.Fatal(err)
		}
		check(r, "v1\n", "demo")
		if r.Exists("projects/lab") || !r.Exists("projects/demo/project.yaml") {
			t.Error("the view sees the write")
		}
		if snap, ok := r.(Snapshot); !ok || snap.Revision() != rev {
			t.Errorf("the view's revision moved from %s", rev)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, func(r treeedit.Reader) error {
		check(r, "v3\n", "lab")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.Head() == rev || s.Head() != other.Head() {
		t.Errorf("head %s, want the other clone's %s", s.Head(), other.Head())
	}
}

// A view of Repositories reads one revision of each repository, the one
// it read first, even when its store fetched a newer one meanwhile.
func TestAViewOfRepositoriesKeepsTheRevisionsItRead(t *testing.T) {
	ctx := context.Background()
	// The stores of newStore are rooted at gitops/.
	_, url := seedStore(t, map[string]string{"platform.yaml": "v1\n", "projects/demo/project.yaml": "{}\n"})
	writer := newStore(t, url, false)
	topo := &Topology{
		Platform: Location{URL: url, Branch: "main", Path: "gitops"},
		Projects: Location{URL: url, Branch: "main", Path: "gitops/projects/{project}"},
		Output:   Location{URL: newRemote(t), Branch: "main"},
	}
	stores := map[string]*GitStore{}
	repos := NewRepositories(topo, func(loc Location) (Store, error) {
		s, err := NewGitStore(GitOptions{URL: loc.URL, Branch: loc.Branch})
		stores[loc.URL] = s
		return s, err
	})
	read := func(r treeedit.Reader, p string) string {
		t.Helper()
		data, err := r.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return string(data)
	}
	err := repos.View(ctx, func(r treeedit.Reader) error {
		if got := read(r, "platform.yaml"); got != "v1\n" {
			t.Fatalf("platform.yaml = %q", got)
		}
		if _, err := writer.Update(ctx, Commit{Subject: "v2"}, func(tx Tx) error {
			if err := tx.WriteFile("platform.yaml", []byte("v2\n")); err != nil {
				return err
			}
			return tx.WriteFile("projects/demo/project.yaml", []byte("name: demo\n"))
		}); err != nil {
			t.Fatal(err)
		}
		if err := stores[url].Sync(ctx); err != nil {
			t.Fatal(err)
		}
		if got := read(r, "platform.yaml"); got != "v1\n" {
			t.Errorf("platform.yaml = %q in the same view, want the revision it read", got)
		}
		if got := read(r, "projects/demo/project.yaml"); got != "{}\n" {
			t.Errorf("project.yaml = %q in the same view, want the revision it read", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.View(ctx, func(r treeedit.Reader) error {
		if got := read(r, "platform.yaml"); got != "v2\n" {
			t.Errorf("platform.yaml = %q in the next view", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A view of a MemoryStore does not wait on a write under way, and reads
// the files as they were when it started.
func TestAMemoryStoreViewDoesNotWaitOnAWrite(t *testing.T) {
	ctx := context.Background()
	s := MustMemoryStore(map[string]string{"platform.yaml": "v1\n"})
	if _, err := s.Update(ctx, Commit{Subject: "v2"}, func(tx Tx) error {
		if err := tx.WriteFile("platform.yaml", []byte("v2\n")); err != nil {
			return err
		}
		done(t, async(func() {
			_ = s.View(ctx, func(r treeedit.Reader) error {
				if data, err := r.ReadFile("platform.yaml"); err != nil || string(data) != "v1\n" {
					t.Errorf("a view during a write: %q, %v", data, err)
				}
				return nil
			})
		}), "a view during a write")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, s, "platform.yaml"); got != "v2\n" {
		t.Errorf("after the write: %q", got)
	}
}

// The audit's probe of GIT-11: an edit of platform.yaml, read at one
// revision, is committed on that revision only; a push to the same file
// meanwhile (an access: revocation) is not overwritten by the whole-file
// write of the edit: the edit is computed again on the new revision, and
// keeps both changes. A repository that keeps moving ends the edit with
// ErrMoved, after the retries.
func TestAnUpdateOfRepositoriesNeverOverwritesAConcurrentPush(t *testing.T) {
	ctx := context.Background()
	_, url := seedStore(t, map[string]string{"platform.yaml": "a: 1\nperson: 1\n"})
	writer := newStore(t, url, false)
	push := func(content string) {
		t.Helper()
		if _, err := writer.Update(ctx, Commit{Subject: "push"}, func(tx Tx) error {
			return tx.WriteFile("platform.yaml", []byte(content))
		}); err != nil {
			t.Fatal(err)
		}
	}
	repos := NewRepositories(&Topology{
		Platform: Location{URL: url, Branch: "main", Path: "gitops"},
		Projects: Location{URL: url, Branch: "main", Path: "gitops/projects/{project}"},
		Output:   Location{URL: newRemote(t), Branch: "main"},
	}, func(loc Location) (Store, error) { return NewGitStore(GitOptions{URL: loc.URL, Branch: loc.Branch}) })

	calls := 0
	edit := func(concurrent func(call int)) func(tx Tx) error {
		return func(tx Tx) error {
			calls++
			data, err := tx.ReadFile("platform.yaml")
			if err != nil {
				return err
			}
			concurrent(calls)
			return tx.WriteFile("platform.yaml", []byte(strings.Replace(string(data), "a: 1", "a: 2", 1)))
		}
	}
	if _, err := repos.Update(ctx, Commit{Subject: "edit"}, edit(func(call int) {
		if call == 1 {
			push("a: 1\nperson: 2\n")
		}
	})); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("the edit ran %d times, want 2: once on the revision that moved, once on the new one", calls)
	}
	if got := readFile(t, newStore(t, url, false), "platform.yaml"); got != "a: 2\nperson: 2\n" {
		t.Errorf("platform.yaml = %q: the concurrent push or the edit is lost", got)
	}

	// A repository that moves under every attempt: the retries are bounded.
	repos.MaxRetries = 2
	calls = 0
	_, err := repos.Update(ctx, Commit{Subject: "edit"}, edit(func(call int) { push(fmt.Sprintf("a: 1\nperson: %d\n", 10+call)) }))
	if !errors.Is(err, ErrMoved) || !errors.Is(err, ErrConflict) {
		t.Errorf("a repository that keeps moving: %v, want ErrConflict wrapping ErrMoved", err)
	}
	if calls != 3 {
		t.Errorf("the edit ran %d times, want 3 (the first and 2 retries)", calls)
	}
	if got := readFile(t, newStore(t, url, false), "platform.yaml"); got != "a: 1\nperson: 13\n" {
		t.Errorf("platform.yaml = %q: the last push only", got)
	}
}

// An edit across two repositories that finds the second moved computes
// again before anything is committed: the first holds one commit of the
// edit, on the final revisions, not one per attempt.
func TestAnEditAcrossRepositoriesIsComputedAgainBeforeItsFirstCommit(t *testing.T) {
	ctx := context.Background()
	open := func(loc Location) (Store, error) { return NewGitStore(GitOptions{URL: loc.URL, Branch: loc.Branch}) }
	platform, projects := newRemote(t), newRemote(t)
	for url, files := range map[string]map[string]string{
		platform: {"platform.yaml": "n: 0\n"},
		projects: {"projects/demo/project.yaml": "description: one\n"},
	} {
		s, err := NewGitStore(GitOptions{URL: url, Branch: "main", AuthorName: "tester", AuthorEmail: "t@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error {
			for p, content := range files {
				if err := tx.WriteFile(p, []byte(content)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	repos := NewRepositories(&Topology{
		Platform: Location{URL: platform, Branch: "main"},
		Projects: Location{URL: projects, Branch: "main", Path: "projects/{project}"},
		Output:   Location{URL: newRemote(t), Branch: "main"},
	}, open)
	writer, err := NewGitStore(GitOptions{URL: projects, Branch: "main", AuthorName: "tester", AuthorEmail: "t@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	if _, err := repos.Update(ctx, Commit{Subject: "edit"}, func(tx Tx) error {
		calls++
		if _, err := tx.ReadFile("platform.yaml"); err != nil {
			return err
		}
		if _, err := tx.ReadFile("projects/demo/project.yaml"); err != nil {
			return err
		}
		if calls == 1 {
			if _, err := writer.Update(ctx, Commit{Subject: "push"}, func(w Tx) error {
				return w.WriteFile("projects/demo/project.yaml", []byte("description: two\n"))
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.WriteFile("platform.yaml", []byte(fmt.Sprintf("n: %d\n", calls))); err != nil {
			return err
		}
		return tx.WriteFile("projects/demo/notes.yaml", []byte("note\n"))
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("the edit ran %d times, want 2", calls)
	}
	if got := readFile(t, newStoreAt(t, platform), "platform.yaml"); got != "n: 2\n" {
		t.Errorf("platform.yaml = %q", got)
	}
	if got := readFile(t, newStoreAt(t, projects), "projects/demo/project.yaml"); got != "description: two\n" {
		t.Errorf("project.yaml = %q: the push is lost", got)
	}
	if n := commitsOf(t, platform, "edit"); n != 1 {
		t.Errorf("the platform repository holds %d commits of the edit, want 1", n)
	}
}

// newStoreAt is a store of url at its root.
func newStoreAt(t *testing.T, url string) *GitStore {
	t.Helper()
	s, err := NewGitStore(GitOptions{URL: url, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// commitsOf counts the commits of the repository at url whose message
// starts with subject.
func commitsOf(t *testing.T, url, subject string) int {
	t.Helper()
	repo, err := git.PlainOpen(strings.TrimPrefix(url, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	_ = iter.ForEach(func(c *object.Commit) error {
		if strings.HasPrefix(c.Message, subject) {
			n++
		}
		return nil
	})
	return n
}

// A tree compiled without the checks against the upstream charts (a chart
// that could not be fetched) is not kept: the next compile of the same
// files tries again, and the one that is checked is.
func TestATreeCompiledWithoutTheChartChecksIsNotKept(t *testing.T) {
	ctx := context.Background()
	s, _ := seedStore(t, map[string]string{"platform.yaml": "global: {}\n", "projects/demo/project.yaml": "{}\n"})
	builds := 0
	c := NewCompiler(&catalog.Loader{})
	c.build = func(context.Context, []compiler.Repository, compiler.BuildOptions) (*compiler.Tree, error) {
		builds++
		if builds <= 2 {
			return &compiler.Tree{ChartFailures: map[compiler.ChartRef]error{{Repository: "oci://r", Chart: "c", Version: "1"}: errors.New("chart unreachable")}}, nil
		}
		return &compiler.Tree{}, nil
	}
	d := &Deployments{Store: s, Compiler: c}
	for i := 1; i <= 2; i++ {
		if tree, err := d.Tree(ctx); err != nil || tree.ChartFailures == nil || builds != i {
			t.Fatalf("compile %d: %d builds, %v, %v: want a build each time", i, builds, tree, err)
		}
	}
	checked, err := d.Tree(ctx)
	if err != nil || checked.ChartFailures != nil || builds != 3 {
		t.Fatalf("the third compile: %d builds, %v, %v", builds, checked, err)
	}
	if again, err := d.Tree(ctx); err != nil || again != checked || builds != 3 {
		t.Fatalf("the checked tree again: %d builds, %v, want the one kept", builds, err)
	}
}
