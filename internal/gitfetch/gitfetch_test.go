package gitfetch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"
)

// go-git's own server fetches no depth: the fetch is made again without
// one, the history fetched.
func TestAServerWithoutShallowFetchesGetsTheHistory(t *testing.T) {
	client.InstallProtocol("file", server.DefaultServer)
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	fs := memfs.New()
	src, err := git.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := src.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}
	for _, content := range []string{"one", "two"} {
		if err := util.WriteFile(fs, "f", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add("f"); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Commit(content, &git.CommitOptions{Author: sig}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file://" + dir}}); err != nil {
		t.Fatal(err)
	}
	if err := src.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}

	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file://" + dir}}); err != nil {
		t.Fatal(err)
	}
	opts := &git.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"+refs/heads/main:refs/remotes/origin/main"}, Depth: 1}
	if err := classify(repo.FetchContext(context.Background(), opts)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("go-git's server took a depth: %v", err)
	}
	if err := Fetch(context.Background(), repo, opts); err != nil {
		t.Fatal(err)
	}
	commits, err := repo.CommitObjects()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	_ = commits.ForEach(func(*object.Commit) error { n++; return nil })
	if n != 2 {
		t.Fatalf("%d commits fetched, want the history, 2", n)
	}
}
