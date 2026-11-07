package gitops_test

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/gitops"
)

// A directory is read as a clone is: its files and its listings but .git,
// a link refused and never followed, a file over the bound refused by its
// size, what is neither a file nor a directory not there.
func TestADirReaderReadsADirectoryAsAClone(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("s3cr3t\n"), 0o600))
	for p, content := range map[string]string{
		"platform.yaml":       "global: {}\n",
		"projects/demo/a.txt": "a\n",
		"projects/demo/.git":  "gitdir: elsewhere\n",
		"big/file":            string(make([]byte, gitops.MaxTreeFileSize+1)),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), []byte(content), 0o644))
	}
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "projects/demo/link")))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "projects/demo/pipe"), 0o644))
	r := gitops.NewDirReader(root)

	data, err := r.ReadFile("platform.yaml")
	require.NoError(t, err)
	require.Equal(t, "global: {}\n", string(data))
	names, err := r.ReadDir("projects/demo")
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt", "link", "pipe"}, names, ".git not listed")
	names, err = r.ReadDir(".")
	require.NoError(t, err)
	require.Equal(t, []string{"big", "linkdir", "platform.yaml", "projects"}, names)

	for _, p := range []string{"projects/demo/link", "linkdir", "linkdir/secret"} {
		_, err = r.ReadFile(p)
		require.ErrorIs(t, err, gitops.ErrSymlink, p)
		require.ErrorContains(t, err, root)
		_, err = r.ReadDir(p)
		require.ErrorIs(t, err, gitops.ErrSymlink, p)
		require.False(t, r.Exists(p), p)
	}

	_, err = r.ReadFile("big/file")
	require.ErrorIs(t, err, gitops.ErrTooLarge)
	require.ErrorContains(t, err, "big/file is "+strconv.Itoa(gitops.MaxTreeFileSize+1)+" bytes")

	for _, p := range []string{"missing", "platform.yaml/below", "projects/demo/pipe", "projects/demo/pipe/below"} {
		_, err = r.ReadFile(p)
		require.ErrorIs(t, err, treeedit.ErrNotFound, p)
		names, err = r.ReadDir(p)
		require.NoError(t, err, p)
		require.Empty(t, names, p)
	}
	require.True(t, r.Exists("projects/demo"))
	require.True(t, r.Exists("platform.yaml"))
	require.False(t, r.Exists("platform.yaml/below"))
	_, err = r.ReadFile("../escape")
	require.Error(t, err)
}
