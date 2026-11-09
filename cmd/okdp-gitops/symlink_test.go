package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/gitops"
)

// stderrOf runs fn and returns what it wrote on stderr.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	saved := os.Stderr
	os.Stderr = w
	done := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()
	defer func() { os.Stderr = saved }()
	fn()
	require.NoError(t, w.Close())
	return string(<-done)
}

// A symbolic link the tree reaches fails the compile, naming the
// repository and the link, in a local directory: nothing is written, the
// link is not followed. A link the tree does not reach, beside it, is no
// problem.
func TestASymbolicLinkOfTheTreeFailsTheCompile(t *testing.T) {
	root, output := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, output)
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "s3cr3t\n")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "notes.txt")))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(root, "elsewhere")))
	args := []string{"compile", "--tree", root, "--output", output, "--path-prefix", "", "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args), "links the tree does not reach")
	require.Equal(t, 0, run(append(args, "--check")))

	config := filepath.Join(root, "projects/demo/instances/legacy/config.yaml")
	require.NoError(t, os.Remove(config))
	require.NoError(t, os.Symlink(outside, config))
	before, err := readTree(output)
	require.NoError(t, err)
	var code int
	out := stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Equal(t, "okdp-gitops: "+root+": projects/demo/instances/legacy/config.yaml: "+gitops.ErrSymlink.Error()+"\n", out)
	after, err := readTree(output)
	require.NoError(t, err)
	require.Equal(t, before, after, "nothing written")

	// A linked directory of the output: nothing is written through it.
	require.NoError(t, os.Remove(config))
	write(t, config, "database: ext-db\n")
	require.NoError(t, os.RemoveAll(filepath.Join(output, "compiled")))
	target := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(output, "compiled")))
	out = stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Equal(t, "okdp-gitops: "+output+": compiled: "+gitops.ErrSymlink.Error()+"\n", out)
	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// The same in a Git repository, the links committed (mode 120000): the
// compile fails, nothing pushed; a link beside the tree is no problem.
func TestASymbolicLinkCommittedInTheTreeFailsTheCompile(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "deployments.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)
	url := "file://" + remote
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "s3cr3t\n")

	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	require.NoError(t, err)
	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	require.NoError(t, err)
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	for p, content := range tree {
		require.NoError(t, util.WriteFile(fs, "gitops/"+p, []byte(content), 0o644))
	}
	require.NoError(t, fs.Symlink("gitops/catalogs.yaml", "README.md"))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	sig := &object.Signature{Name: "a person", Email: "p@example.com"}
	commit := func(subject string) {
		t.Helper()
		require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
		_, err = wt.Commit(subject, &git.CommitOptions{Author: sig, Committer: sig})
		require.NoError(t, err)
		require.NoError(t, repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/main"}}))
	}
	commit("the sources")

	output := filepath.Join(dir, "okdp-output.git")
	_, err = git.PlainInit(output, true)
	require.NoError(t, err)
	outputURL := "file://" + output
	topology := filepath.Join(dir, "topology.yaml")
	write(t, topology, "platform: {url: '"+url+"', path: gitops}\nprojects: {url: '"+url+"', path: 'gitops/projects/{project}'}\noutput: {url: '"+outputURL+"'}\n")
	args := []string{"compile", "--config", topology, "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args), "a link beside the tree")
	outputHead := func() string {
		t.Helper()
		out, err := git.PlainOpen(output)
		require.NoError(t, err)
		ref, err := out.Reference("refs/heads/main", true)
		require.NoError(t, err)
		return ref.Hash().String()
	}
	compiled := outputHead()

	require.NoError(t, fs.Remove("gitops/projects/demo/instances/legacy/config.yaml"))
	require.NoError(t, fs.Symlink(outside, "gitops/projects/demo/instances/legacy/config.yaml"))
	commit("a link")
	var code int
	out := stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Equal(t, "okdp-gitops: "+url+": gitops/projects/demo/instances/legacy/config.yaml: "+gitops.ErrSymlink.Error()+"\n", out)
	require.Equal(t, compiled, outputHead(), "nothing pushed")
}
