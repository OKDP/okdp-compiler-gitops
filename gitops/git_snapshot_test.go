package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/internal/gittest"
)

// bigRepo serves a repository whose last commit holds files (a path, its
// content) and, of zero bytes, the sparse files zeros (a path, its size):
// a push of some kilobytes that weighs tens of megabytes inflated. It
// returns the URL of the repository.
func bigRepo(t *testing.T, files map[string]string, zeros map[string]int64) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git binary to build the repository")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		file := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, size := range zeros {
		file := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	run(work, "init", "-q", "-b", "main")
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "seed")
	run(root, "init", "-q", "--bare", "-b", "main", "remote.git")
	run(work, "push", "-q", filepath.Join(root, "remote.git"), "main")
	return gittest.Serve(t, root) + "/remote.git"
}

// retained is the bytes of blobs a snapshot holds in memory.
func retained(s *gitSnapshot) (total int) {
	for _, b := range s.blobs {
		total += len(b)
	}
	return total
}

// onDisk opens the store of url on a clone on disk, which reads a blob
// by its header.
func onDisk(t *testing.T, url string, layout func() []string) *GitStore {
	t.Helper()
	s, err := NewGitStore(GitOptions{URL: url, Branch: "main", CloneDir: filepath.Join(t.TempDir(), "clone"), Depth: 1, Layout: layout})
	require.NoError(t, err)
	return s
}

// The audit's probe of GIT-10: a tenant's push of a 64 MiB file of zeros
// (some kilobytes compressed) never makes it into the snapshot, in the
// layout or beside it, and a file of the layout over the bound is refused
// by its name.
func TestASnapshotNeverReadsAFileOverTheBound(t *testing.T) {
	url := bigRepo(t,
		map[string]string{"platform.yaml": "global: {}\n", "projects/demo/project.yaml": "{}\n", "assets/notes.txt": "beside\n"},
		map[string]int64{"assets/disk.iso": 64 << 20, "projects/demo/big.bin": 65 << 20})
	inside := fmt.Sprintf("projects/demo/big.bin is %d bytes, more than the %d a file of the tree may weigh", 65<<20, MaxTreeFileSize)
	ctx := context.Background()

	// The layout: the file beside it is not even listed.
	s := onDisk(t, url, func() []string { return []string{"platform.yaml", "projects"} })
	snap, err := s.Snapshot(ctx)
	require.NoError(t, err)
	require.LessOrEqual(t, retained(s.snap.Load()), MaxTreeFileSize, "no blob over the bound is read")
	require.False(t, snap.Exists("assets"))
	top, err := snap.ReadDir(".")
	require.NoError(t, err)
	require.Equal(t, []string{"platform.yaml", "projects"}, top)
	data, err := snap.ReadFile("projects/demo/project.yaml")
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(data))
	_, err = snap.ReadFile("projects/demo/big.bin")
	require.ErrorIs(t, err, ErrTooLarge)
	require.ErrorContains(t, err, inside)
	require.True(t, snap.Exists("projects/demo/big.bin"))
	names, err := snap.ReadDir("projects/demo")
	require.NoError(t, err)
	require.Equal(t, []string{"big.bin", "project.yaml"}, names)

	// No layout: every path is listed, the files over the bound are not read.
	s = onDisk(t, url, nil)
	snap, err = s.Snapshot(ctx)
	require.NoError(t, err)
	require.LessOrEqual(t, retained(s.snap.Load()), MaxTreeFileSize)
	_, err = snap.ReadFile("assets/disk.iso")
	require.ErrorIs(t, err, ErrTooLarge)
	require.ErrorContains(t, err, fmt.Sprintf("assets/disk.iso is %d bytes", 64<<20))
	data, err = snap.ReadFile("assets/notes.txt")
	require.NoError(t, err)
	require.Equal(t, "beside\n", string(data))
}

// The files read stay within MaxTreeSize together: past it a file is not
// read, a blob two paths share counts once.
func TestASnapshotReadsWithinTheBoundOfTheTree(t *testing.T) {
	file := func(c byte) string { return strings.Repeat(string(c), 1000) }
	url := bigRepo(t, map[string]string{"a.yaml": file('a'), "b.yaml": file('b'), "c.yaml": file('c'), "same.yaml": file('a')}, nil)
	s := onDisk(t, url, nil)
	s.maxFile, s.maxTree = 1500, 2500
	snap, err := s.Snapshot(context.Background())
	require.NoError(t, err)
	for _, ok := range []string{"a.yaml", "b.yaml", "same.yaml"} {
		_, err := snap.ReadFile(ok)
		require.NoError(t, err, ok)
	}
	_, err = snap.ReadFile("c.yaml")
	require.ErrorIs(t, err, ErrTooLarge)
	require.EqualError(t, err, "the tree holds more than 2500 bytes")
	require.Equal(t, 2000, retained(s.snap.Load()))
}

// A layout that changed (the platform's projects.yaml placed a project in
// the repository) is read again from the clone, at the revision already
// read: nothing is fetched, a snapshot taken before still reads.
func TestAChangedLayoutIsReadAgainAtTheSameRevision(t *testing.T) {
	url := bigRepo(t, map[string]string{"platform.yaml": "global: {}\n", "teams/data/project.yaml": "{}\n", "other/x.yaml": "x\n"}, nil)
	layout := []string{"platform.yaml"}
	s := onDisk(t, url, func() []string { return layout })
	first, err := s.Snapshot(context.Background())
	require.NoError(t, err)
	require.False(t, first.Exists("teams"))

	layout = []string{"platform.yaml", "teams/data"}
	again, err := s.Reread()
	require.NoError(t, err)
	require.Equal(t, first.Revision(), again.Revision())
	data, err := again.ReadFile("teams/data/project.yaml")
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(data))
	require.False(t, again.Exists("other"))
	require.False(t, first.Exists("teams"), "a snapshot is never changed")

	got, err := s.Snapshot(context.Background())
	require.NoError(t, err)
	require.True(t, got.Exists("teams/data/project.yaml"))
	names, err := got.ReadDir("teams")
	require.NoError(t, err)
	require.Equal(t, []string{"data"}, names)
	_, err = got.ReadFile("other/x.yaml")
	require.ErrorIs(t, err, treeedit.ErrNotFound)
}
