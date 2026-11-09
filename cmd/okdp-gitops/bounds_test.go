package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/gitops"
)

// A remote that never answers: the compile fails at its --timeout, not
// later, saying so.
func TestATimeoutEndsAHangingCompile(t *testing.T) {
	t.Setenv(gitops.TokenEnv, "")
	t.Setenv(gitops.CredentialsDirEnv, "")
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	url := srv.URL + "/okdp/deployments.git"
	topology := filepath.Join(t.TempDir(), "topology.yaml")
	write(t, topology, "platform: {url: '"+url+"'}\nprojects: {url: '"+url+"', path: 'projects/{project}'}\noutput: {url: '"+srv.URL+"/okdp/okdp-output.git'}\n")
	start := time.Now()
	var code int
	out := stderrOf(t, func() { code = run([]string{"compile", "--config", topology, "--timeout", "300ms"}) })
	require.Equal(t, 1, code, out)
	require.Less(t, time.Since(start), 10*time.Second, "bounded by --timeout")
	require.Contains(t, out, "okdp-gitops: timed out after 300ms (--timeout)")

	require.Equal(t, 2, run([]string{"compile", "--config", topology, "--timeout", "0s"}), "a timeout of 0 is refused: a run is always bounded")
}

// The compile reads the paths of the layout only: a large file beside the
// tree, or one it cannot read, is not read (a sparse file: no disk, no
// memory); a file over gitops.MaxTreeFileSize in the tree is refused by its
// size, before it is read.
func TestTheCompileReadsTheLayoutOnly(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	sparse := func(p string, size int64) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		f, err := os.Create(p)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(size))
		require.NoError(t, f.Close())
	}
	sparse(filepath.Join(root, "images", "disk.iso"), 100<<20)
	sparse(filepath.Join(root, "big.bin"), gitops.MaxTreeFileSize+1)
	private := filepath.Join(root, "private")
	write(t, private, "s3cr3t\n")
	require.NoError(t, os.Chmod(private, 0))
	t.Cleanup(func() { _ = os.Chmod(private, 0o600) })

	args := []string{"compile", "--tree", root, "--output", out, "--path-prefix", "", "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args), "the files beside the tree are not read")
	require.Equal(t, 0, run(append(args, "--check")))

	sparse(filepath.Join(root, "projects/demo/notes.bin"), gitops.MaxTreeFileSize+1)
	var code int
	msg := stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Contains(t, msg, "projects/demo/notes.bin is "+strconv.Itoa(gitops.MaxTreeFileSize+1)+" bytes, more than the "+strconv.Itoa(gitops.MaxTreeFileSize)+" a file of the tree may weigh")
}

// recorder is a Reader that records the files read.
type recorder struct {
	treeedit.Reader
	read []string
}

func (r *recorder) ReadFile(p string) ([]byte, error) {
	data, err := r.Reader.ReadFile(p)
	if err == nil {
		r.read = append(r.read, p)
	}
	return data, err
}

// Of a git repository too: the files of the layout read, none beside it;
// one over the bound refused.
func TestARepositoryIsReadAtTheLayoutPathsOnly(t *testing.T) {
	topology, err := gitops.ParseTopology([]byte("platform: {url: https://git.example.org/okdp/deployments.git, path: gitops}\n" +
		"projects: {url: https://git.example.org/okdp/deployments.git, path: 'gitops/projects/{project}'}\n" +
		"output: {url: https://git.example.org/okdp/okdp-output.git}\n"))
	require.NoError(t, err)
	paths := topology.LayoutPaths(topology.Platform.Repository(), nil)
	store := gitops.MustMemoryStore(map[string]string{
		"README.md":                                   "a repository of more than the tree\n",
		"assets/video.mp4":                            "a large file",
		"gitops/platform.yaml":                        "global: {}\n",
		"gitops/notes.txt":                            "beside the tree\n",
		"gitops/projects/demo/project.yaml":           "description: demo\n",
		"gitops/compiled/projects/kustomization.yaml": "resources: []\n",
	})
	var got []string
	require.NoError(t, store.View(context.Background(), func(r treeedit.Reader) error {
		rec := &recorder{Reader: r}
		files := map[string]string{}
		err := readPaths(rec, paths, files, map[string]bool{})
		got = rec.read
		return err
	}))
	slices.Sort(got)
	require.Equal(t, []string{"gitops/platform.yaml", "gitops/projects/demo/project.yaml"}, got, "the output is not the sources'")

	large := gitops.MustMemoryStore(map[string]string{"gitops/projects/demo/big.bin": string(make([]byte, gitops.MaxTreeFileSize+1))})
	err = large.View(context.Background(), func(r treeedit.Reader) error {
		return readPaths(r, paths, map[string]string{}, map[string]bool{})
	})
	require.ErrorContains(t, err, "gitops/projects/demo/big.bin is "+strconv.Itoa(gitops.MaxTreeFileSize+1)+" bytes")
}

// A compile of a Git repository reads the layout out of the clone on disk,
// never a file beside it: a file over the bound outside the layout does
// not matter, one inside is refused by its name and nothing is pushed.
func TestACompileOfAGitRepositoryReadsTheLayoutOnly(t *testing.T) {
	t.Setenv(gitops.TokenEnv, "")
	t.Setenv(gitops.CredentialsDirEnv, "")
	dir := t.TempDir()
	url, store := bare(t, dir, "okdp-sources")
	outputURL, _ := bare(t, dir, "okdp-output")
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	delete(tree, gitops.ArgoCDRootPath)
	tree["assets/disk.iso"] = string(make([]byte, gitops.MaxTreeFileSize+1))
	commitFiles(t, store, "the sources", tree)

	topology := filepath.Join(dir, "topology.yaml")
	write(t, topology, "platform: {url: '"+url+"'}\nprojects: {url: '"+url+"', path: 'projects/{project}'}\noutput: {url: '"+outputURL+"'}\n")
	args := []string{"compile", "--config", topology, "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args), "the file beside the layout is not read")

	commitFiles(t, store, "a large file", map[string]string{"projects/demo/notes.bin": string(make([]byte, gitops.MaxTreeFileSize+1))})
	var code int
	msg := stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Contains(t, msg, "projects/demo/notes.bin is "+strconv.Itoa(gitops.MaxTreeFileSize+1)+" bytes, more than the "+strconv.Itoa(gitops.MaxTreeFileSize)+" a file of the tree may weigh")
}
