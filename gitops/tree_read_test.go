package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/okdp/okdp-gitops/treeedit"
)

// pushTree pushes files and symbolic links (link -> target) to the main
// branch of url, as a person commits them (git mode 120000).
func pushTree(t *testing.T, url string, files map[string]string, links map[string]string) {
	t.Helper()
	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		if err := util.WriteFile(fs, p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range links {
		if err := fs.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := fs.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "a person", Email: "p@example.com"}
	if _, err := wt.Commit("links", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
}

// A symbolic link committed in the tree is refused, in a clone on disk as
// in memory, whatever it points to: a file outside the repository (an
// absolute path, a relative one), a file of the tree, a directory; so is
// a path through a linked directory. The tree does not compile, the
// repository and the link named; the files beside it are read as before.
func TestASymbolicLinkInTheTreeIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url := newRemote(t)
	pushTree(t, url, map[string]string{
		"gitops/platform.yaml":                                "global: {}\n",
		"gitops/projects/demo/project.yaml":                   "{}\n",
		"gitops/projects/demo/instances/app/instance.yaml":    "service: app\nversion: 1.0.0\n",
		"gitops/projects/demo/instances/legacy/config.yaml":   "database: ext\n",
		"gitops/projects/demo/instances/legacy/instance.yaml": "service: app\nversion: 1.0.0\n",
	}, map[string]string{
		"gitops/projects/demo/instances/app/config.yaml":      outside,
		"gitops/projects/demo/instances/app/connections.yaml": strings.Repeat("../", 8) + strings.TrimPrefix(outside, "/"),
		"gitops/projects/demo/instances/legacy/values.yaml":   "config.yaml",
		"gitops/projects/demo/instances/copy":                 "legacy",
	})
	refused := func(link string) string { return url + ": gitops/" + link + ": " + ErrSymlink.Error() }
	for _, onDisk := range []bool{false, true} {
		s := newStore(t, url, onDisk)
		err := s.View(context.Background(), func(r treeedit.Reader) error {
			for p, link := range map[string]string{
				"projects/demo/instances/app/config.yaml":      "projects/demo/instances/app/config.yaml",
				"projects/demo/instances/app/connections.yaml": "projects/demo/instances/app/connections.yaml",
				"projects/demo/instances/legacy/values.yaml":   "projects/demo/instances/legacy/values.yaml",
				"projects/demo/instances/copy":                 "projects/demo/instances/copy",
				"projects/demo/instances/copy/config.yaml":     "projects/demo/instances/copy",
			} {
				if data, err := r.ReadFile(p); !errors.Is(err, ErrSymlink) || err.Error() != refused(link) || data != nil {
					t.Errorf("onDisk=%v ReadFile(%s) = %q, %v", onDisk, p, data, err)
				}
				if names, err := r.ReadDir(p); !errors.Is(err, ErrSymlink) || err.Error() != refused(link) || names != nil {
					t.Errorf("onDisk=%v ReadDir(%s) = %v, %v", onDisk, p, names, err)
				}
				if r.Exists(p) {
					t.Errorf("onDisk=%v Exists(%s)", onDisk, p)
				}
			}
			if data, err := r.ReadFile("projects/demo/instances/legacy/config.yaml"); err != nil || string(data) != "database: ext\n" {
				t.Errorf("onDisk=%v a file beside a link: %q, %v", onDisk, data, err)
			}
			if names, err := r.ReadDir("projects/demo/instances"); err != nil || strings.Join(names, ",") != "app,copy,legacy" {
				t.Errorf("onDisk=%v ReadDir: %v, %v", onDisk, names, err)
			}
			if _, err := treeedit.ReadSources(r); !errors.Is(err, ErrSymlink) || err.Error() != refused("projects/demo/instances/app/config.yaml") {
				t.Errorf("onDisk=%v snapshot: %v", onDisk, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// Nothing is written through a link either.
		_, err = s.Update(context.Background(), Commit{Subject: "through a link"}, func(tx Tx) error {
			if err := tx.WriteFile("projects/demo/instances/copy/instance.yaml", []byte("x")); !errors.Is(err, ErrSymlink) {
				t.Errorf("onDisk=%v WriteFile through a link: %v", onDisk, err)
			}
			if err := tx.Remove("projects/demo/instances/copy"); !errors.Is(err, ErrSymlink) {
				t.Errorf("onDisk=%v Remove of a link: %v", onDisk, err)
			}
			return errors.New("nothing")
		})
		if err == nil || err.Error() != "nothing" {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(outside); err != nil || string(data) != "s3cr3t\n" {
			t.Fatalf("the file outside: %q, %v", data, err)
		}
	}
}

// The prefix of the tree in its repository may not be a link either.
func TestALinkedPrefixIsRefused(t *testing.T) {
	url := newRemote(t)
	pushTree(t, url, map[string]string{"elsewhere/platform.yaml": "global: {}\n"}, map[string]string{"gitops": "elsewhere"})
	for _, onDisk := range []bool{false, true} {
		s := newStore(t, url, onDisk)
		err := s.View(context.Background(), func(treeedit.Reader) error { return nil })
		if !errors.Is(err, ErrSymlink) || err.Error() != url+": gitops: "+ErrSymlink.Error() {
			t.Errorf("onDisk=%v: %v", onDisk, err)
		}
	}
}

// The compiler is given a tree within bounds: a file of 4 MiB at most,
// 64 MiB together.
func TestTheTreeGivenToTheCompilerIsBounded(t *testing.T) {
	big := strings.Repeat("#", MaxTreeFileSize+1)
	store := MustMemoryStore(map[string]string{"platform.yaml": "global: {}\n", "projects/demo/instances/app/config.yaml": big})
	err := store.View(context.Background(), func(r treeedit.Reader) error {
		_, err := treeedit.ReadSources(r)
		return err
	})
	if err == nil || !strings.HasPrefix(err.Error(), "projects/demo/instances/app/config.yaml is 4194305 bytes, more than the 4194304") {
		t.Errorf("a big file: %v", err)
	}

	files := map[string]string{}
	for i := range MaxTreeSize/MaxTreeFileSize + 1 {
		files["projects/demo/instances/app/"+string(rune('a'+i))+".yaml"] = big[1:]
	}
	err = MustMemoryStore(files).View(context.Background(), func(r treeedit.Reader) error {
		_, err := treeedit.ReadSources(r)
		return err
	})
	if err == nil || err.Error() != "the tree holds more than 67108864 bytes" {
		t.Errorf("a big tree: %v", err)
	}
}
