package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/internal/gittest"
)

func init() {
	// Serve file:// in process: the tests need no git binary.
	client.InstallProtocol("file", server.DefaultServer)
}

func files(m map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for p, c := range m {
		out[p] = &fstest.MapFile{Data: []byte(c)}
	}
	return out
}

func TestTheCatalogsAreIndexedByTheCompiler(t *testing.T) {
	cats := New(
		Catalog{Name: "platform-charts", FS: files(map[string]string{
			"catalog/trino/480.0.0-1.0.0/Chart.yaml":  "name: trino\n",
			"catalog/trino/480.0.0-1.0.10/Chart.yaml": "name: trino\n",
			"contracts/s3.schema.json":                "{}",
		})},
		Catalog{Name: "sandbox-dependencies", FS: files(map[string]string{
			"catalog/keycloak/7.3.2-1.0.0/Chart.yaml": "name: keycloak\n",
		})},
	)
	index, err := cats.Index()
	require.NoError(t, err)
	services, err := index.Services()
	require.NoError(t, err)
	require.Equal(t, []compiler.CatalogService{
		{Name: "keycloak", Repository: "sandbox-dependencies", Versions: []string{"7.3.2-1.0.0"}},
		{Name: "trino", Repository: "platform-charts", Versions: []string{"480.0.0-1.0.10", "480.0.0-1.0.0"}},
	}, services)
	pkg, err := index.Package("keycloak", "7.3.2-1.0.0")
	require.NoError(t, err)
	chart, err := fs.ReadFile(pkg, "Chart.yaml")
	require.NoError(t, err)
	require.Equal(t, "name: keycloak\n", string(chart))
	_, err = index.Package("trino", "1.0.0")
	require.ErrorIs(t, err, compiler.ErrNotInCatalog)
	_, err = fs.Stat(index.Contracts(), "s3.schema.json")
	require.NoError(t, err)

	_, err = New(cats.List()[0], Catalog{Name: "other", FS: files(map[string]string{"catalog/trino/1.0.0/Chart.yaml": "x"})}).Index()
	require.ErrorContains(t, err, `service "trino" is in the repositories other and platform-charts`)
}

// commit writes files to the worktree of repo and commits them.
func commit(t *testing.T, repo *git.Repository, files map[string]string) plumbing.Hash {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err)
	for p, c := range files {
		full := filepath.Join(wt.Filesystem.Root(), filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o644))
	}
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	hash, err := wt.Commit("catalog", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}})
	require.NoError(t, err)
	return hash
}

// A catalog is read from its repository at a branch, a tag or a commit, and
// from a local directory instead of its url when one is given.
func TestTheLoaderReadsARepositoryAtItsRef(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)
	first := commit(t, repo, map[string]string{"catalog/trino/1.0.0/Chart.yaml": "name: trino\nversion: 1.0.0\n", "contracts/s3.schema.json": "{}"})
	_, err = repo.CreateTag("v1", first, nil)
	require.NoError(t, err)
	commit(t, repo, map[string]string{"catalog/trino/1.1.0/Chart.yaml": "name: trino\nversion: 1.1.0\n"})

	// The in-process file:// server reads the repository itself, .git.
	url := "file://" + filepath.Join(dir, ".git")
	loader := &Loader{RefreshInterval: time.Nanosecond, FileURLs: true}
	versions := func(ref string) []string {
		t.Helper()
		cats, err := loader.Open(context.Background(), []treeedit.CatalogRepository{{Name: "platform-charts", URL: url, Ref: ref}})
		require.NoError(t, err)
		index, err := cats.Index()
		require.NoError(t, err)
		svc, err := index.Service("trino")
		require.NoError(t, err)
		return svc.Versions
	}
	require.Equal(t, []string{"1.1.0", "1.0.0"}, versions("main"))
	require.Equal(t, []string{"1.0.0"}, versions("v1"))
	require.Equal(t, []string{"1.0.0"}, versions(first.String()))

	_, err = loader.Open(context.Background(), []treeedit.CatalogRepository{{Name: "platform-charts", URL: url, Ref: "nope"}})
	require.ErrorContains(t, err, "ref nope: no such branch, tag or commit")

	// A local checkout instead of the url.
	local := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(local, "catalog/trino/9.0.0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(local, "catalog/trino/9.0.0/Chart.yaml"), []byte("name: trino\nversion: 9.0.0\n"), 0o644))
	loader.Dirs = map[string]string{"platform-charts": local}
	require.Equal(t, []string{"9.0.0"}, versions("main"))

	_, err = (&Loader{Dirs: map[string]string{"x": filepath.Join(dir, "missing")}}).Open(context.Background(), []treeedit.CatalogRepository{{Name: "x", URL: url}})
	require.True(t, errors.Is(err, fs.ErrNotExist), "%v", err)
}

// A url of catalogs.yaml that reads the local file system is refused: a
// local directory, which --catalog reads instead, a file:// repository
// unless the platform is local (FileURLs).
func TestTheLoaderRefusesALocalURL(t *testing.T) {
	dir := t.TempDir()
	for _, url := range []string{dir, "./catalog", "../catalog", "file://" + dir, "FILE://" + dir} {
		_, err := (&Loader{}).Open(context.Background(), []treeedit.CatalogRepository{{Name: "x", URL: url, Ref: "main"}})
		require.ErrorContains(t, err, "reads the local file system: a catalog of catalogs.yaml is a Git repository, a local checkout the operator's (okdp-gitops compile --catalog x=DIR)", url)
	}
	_, err := (&Loader{FileURLs: true}).Open(context.Background(), []treeedit.CatalogRepository{{Name: "x", URL: dir, Ref: "main"}})
	require.ErrorContains(t, err, "reads the local file system")
}

// A local directory is read as a repository is: its regular files only,
// a symbolic link left out, .git too, each file and the whole bounded.
func TestALocalCatalogIsBounded(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "catalog/trino/1.0.0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalog/trino/1.0.0/Chart.yaml"), []byte("name: trino\nversion: 1.0.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s3cr3t"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "catalog/trino/1.0.0/values.yaml")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "catalog/outside")))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git/config"), []byte("[core]\n"), 0o644))

	cat, err := localCatalog("x", dir)
	require.NoError(t, err)
	var names []string
	require.NoError(t, fs.WalkDir(cat.FS, ".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			names = append(names, p)
		}
		return err
	}))
	require.Equal(t, []string{"catalog/trino/1.0.0/Chart.yaml"}, names)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalog/big.bin"), make([]byte, MaxFileSize+1), 0o644))
	_, err = localCatalog("x", dir)
	require.ErrorContains(t, err, "catalog/big.bin is 4194305 bytes, more than the 4194304 a catalog file may weigh")
}

// The loader fetches the ref named only, its last commit: from a server
// that fetches shallow and serves a commit by its hash (git http-backend),
// a branch, then a commit, then a tag, none of the others.
func TestTheLoaderFetchesTheRefNamedOnly(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInitWithOptions(filepath.Join(root, "catalog"), &git.PlainInitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)
	first := commit(t, repo, map[string]string{"catalog/trino/1.0.0/Chart.yaml": "name: trino\nversion: 1.0.0\n"})
	_, err = repo.CreateTag("v1", first, &git.CreateTagOptions{Tagger: &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}, Message: "v1"})
	require.NoError(t, err)
	second := commit(t, repo, map[string]string{"catalog/trino/1.1.0/Chart.yaml": "name: trino\nversion: 1.1.0\n"})
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("other"), second)))
	commit(t, repo, map[string]string{"catalog/trino/1.2.0/Chart.yaml": "name: trino\nversion: 1.2.0\n"})

	url := gittest.Serve(t, root) + "/catalog/.git"
	loader := &Loader{}
	versions := func(ref string) []string {
		t.Helper()
		cats, err := loader.Open(context.Background(), []treeedit.CatalogRepository{{Name: "platform-charts", URL: url, Ref: ref}})
		require.NoError(t, err)
		index, err := cats.Index()
		require.NoError(t, err)
		svc, err := index.Service("trino")
		require.NoError(t, err)
		return svc.Versions
	}
	refs := func() []string {
		t.Helper()
		iter, err := loader.repo(url).repo.References()
		require.NoError(t, err)
		var names []string
		require.NoError(t, iter.ForEach(func(r *plumbing.Reference) error {
			if r.Name() != plumbing.HEAD {
				names = append(names, r.Name().String())
			}
			return nil
		}))
		return names
	}

	require.Equal(t, []string{"1.2.0", "1.1.0", "1.0.0"}, versions("main"))
	require.Equal(t, []string{"refs/remotes/origin/main"}, refs())
	_, err = loader.repo(url).repo.CommitObject(second)
	require.ErrorIs(t, err, plumbing.ErrObjectNotFound, "the history of main is not fetched")

	require.Equal(t, []string{"1.1.0", "1.0.0"}, versions(second.String()))
	require.ElementsMatch(t, []string{"refs/remotes/origin/main", "refs/okdp/commits/" + second.String()}, refs())

	require.Equal(t, []string{"1.0.0"}, versions("v1"))
	require.ElementsMatch(t, []string{"refs/remotes/origin/main", "refs/okdp/commits/" + second.String(), "refs/tags/v1"}, refs())
	shallow, err := loader.repo(url).repo.Storer.Shallow()
	require.NoError(t, err)
	require.Len(t, shallow, 3, "the last commit of each ref")
}

// A symbolic link committed in a catalog is left out, as a local
// directory's is: go-git counts it a file whose content is the path it
// points to.
func TestACommittedSymbolicLinkIsNotACatalogFile(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)
	commit(t, repo, map[string]string{"catalog/trino/1.0.0/Chart.yaml": "name: trino\nversion: 1.0.0\n", "run.sh": "#!/bin/sh\n"})
	require.NoError(t, os.Chmod(filepath.Join(dir, "run.sh"), 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "catalog/trino/1.0.0/values.yaml")))
	head := commit(t, repo, nil)

	files, err := readCommit(repo, head)
	require.NoError(t, err)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	require.ElementsMatch(t, []string{"catalog/trino/1.0.0/Chart.yaml", "run.sh"}, names, "the link is no file; an executable is")
}

// The loader keeps the files of the last commit of a ref, not of every
// revision it fetched, and the in-memory clone it fetches into, which only
// grows, is replaced past its bound by a new one: a long-running reader
// does not grow with every revision.
func TestTheLoaderDoesNotGrowWithEveryRevision(t *testing.T) {
	// From a server that fetches shallow: only the last commit comes.
	root := t.TempDir()
	dir := filepath.Join(root, "catalog")
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)
	url := gittest.Serve(t, root) + "/catalog/.git"
	loader := &Loader{RefreshInterval: time.Nanosecond, maxRepoSize: 2048}
	read := func() []string {
		t.Helper()
		cats, err := loader.Open(context.Background(), []treeedit.CatalogRepository{{Name: "c", URL: url, Ref: "main"}})
		require.NoError(t, err)
		index, err := cats.Index()
		require.NoError(t, err)
		svc, err := index.Service("trino")
		require.NoError(t, err)
		return svc.Versions
	}
	var sizes []int64
	for i := 1; i <= 12; i++ {
		version := fmt.Sprintf("1.0.%d", i)
		require.NoError(t, os.RemoveAll(filepath.Join(dir, "catalog")))
		commit(t, repo, map[string]string{
			"catalog/trino/" + version + "/Chart.yaml": "name: trino\nversion: " + version + "\n",
			"catalog/trino/" + version + "/notes.txt":  strings.Repeat(version+"\n", 200),
		})
		require.Equal(t, []string{version}, read(), "the last commit")
		remote := loader.repo(url)
		require.Len(t, remote.trees, 1, "the last tree of the ref only")
		sizes = append(sizes, objectsSize(remote.repo))
	}
	// Bound 2 KiB, a revision about 1.3 KiB inflated: never more than a
	// couple of revisions at once, where 12 would hold some 16 KiB.
	for _, size := range sizes {
		require.Less(t, size, int64(6<<10), "sizes %v", sizes)
	}
}
