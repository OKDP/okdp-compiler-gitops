package main

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/okdp/okdp-gitops/gitops"
	"github.com/okdp/okdp-gitops/internal/buildinfo"
)

// stdoutOf runs fn and returns what it wrote on stdout.
func stdoutOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()
	defer func() { os.Stdout = saved }()
	fn()
	require.NoError(t, w.Close())
	return string(<-done)
}

// bare creates a bare repository under dir, its file:// URL and a store of
// it.
func bare(t *testing.T, dir, name string) (string, *gitops.GitStore) {
	t.Helper()
	repo := filepath.Join(dir, name+".git")
	_, err := git.PlainInit(repo, true)
	require.NoError(t, err)
	url := "file://" + repo
	store, err := gitops.NewGitStore(gitops.GitOptions{URL: url, Branch: "main", AuthorName: "a person", AuthorEmail: "person@example.com"})
	require.NoError(t, err)
	return url, store
}

// commitFiles commits files to a store.
func commitFiles(t *testing.T, store *gitops.GitStore, subject string, files map[string]string) {
	t.Helper()
	_, err := store.Update(context.Background(), gitops.Commit{Subject: subject}, func(tx gitops.Tx) error {
		for p, content := range files {
			if err := tx.WriteFile(p, []byte(content)); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
}

// filesOf reads every file of a store, at its latest revision.
func filesOf(t *testing.T, store *gitops.GitStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.Sync(ctx))
	files := map[string]string{}
	var walk func(r treeedit.Reader, dir string) error
	walk = func(r treeedit.Reader, dir string) error {
		names, err := r.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, n := range names {
			p := path.Join(dir, n)
			if data, err := r.ReadFile(p); err == nil {
				files[p] = string(data)
			} else if err := walk(r, p); err != nil {
				return err
			}
		}
		return nil
	}
	require.NoError(t, store.View(ctx, func(r treeedit.Reader) error { return walk(r, ".") }))
	return files
}

// The output records the sources it is the compile of: a run whose
// sources, catalogs, okdp-gitops and output did not move stops before the
// compile; any of them moved, it compiles again. A compile of a local
// catalog removes the record.
func TestARunOfSourcesThatDidNotMoveStopsEarly(t *testing.T) {
	t.Setenv(gitops.TokenEnv, "")
	t.Setenv(gitops.CredentialsDirEnv, "")
	saved := buildinfo.Version
	buildinfo.Version = "9.9.9"
	t.Cleanup(func() { buildinfo.Version = saved })
	compiles := 0
	beforeWrite = func() { compiles++ }
	t.Cleanup(func() { beforeWrite = func() {} })

	dir := t.TempDir()
	catalogURL, catalogStore := bare(t, dir, "test-charts")
	charts, err := readTree(catalogFixture)
	require.NoError(t, err)
	commitFiles(t, catalogStore, "the catalog", charts)
	sourcesURL, sourcesStore := bare(t, dir, "okdp-sources")
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	root := tree[gitops.ArgoCDRootPath]
	delete(tree, gitops.ArgoCDRootPath)
	tree[treeedit.CatalogsFile] = strings.Replace(tree[treeedit.CatalogsFile], "https://git.example.org/okdp/test-charts.git", catalogURL, 1)
	commitFiles(t, sourcesStore, "the sources", tree)
	outputURL, outputStore := bare(t, dir, "okdp-output")
	commitFiles(t, outputStore, "argocd", map[string]string{gitops.ArgoCDRootPath: root})

	topology := filepath.Join(dir, "topology.yaml")
	write(t, topology, "platform: {url: '"+sourcesURL+"'}\nprojects: {url: '"+sourcesURL+"', path: 'projects/{project}'}\noutput: {url: '"+outputURL+"'}\n")
	args := []string{"compile", "--config", topology}
	compile := func(want int, extra ...string) string {
		t.Helper()
		var code int
		out := stdoutOf(t, func() { code = run(append(append([]string{}, args...), extra...)) })
		require.Equal(t, want, code, out)
		return out
	}
	record := func() sources {
		t.Helper()
		raw, ok := filesOf(t, outputStore)[gitops.SourcesFile]
		require.True(t, ok, "no record")
		require.True(t, strings.HasPrefix(raw, gitops.GeneratedHeader+"\n"), raw)
		var rec sources
		require.NoError(t, yaml.Unmarshal([]byte(raw), &rec))
		return rec
	}

	compile(0)
	require.Equal(t, 1, compiles)
	rec := record()
	require.Equal(t, "9.9.9", rec.Version)
	require.Len(t, rec.Repositories, 1)
	require.Equal(t, revision{URL: sourcesURL, Branch: "main", Revision: sourcesStore.Head()}, rec.Repositories[0])
	require.Len(t, rec.Catalogs, 1)
	require.Equal(t, revision{Name: "test-charts", URL: catalogURL, Ref: "main", Revision: catalogStore.Head()}, rec.Catalogs[0])

	upToDate := "okdp-gitops: up to date: the sources did not move since the compile sources.yaml records\n"
	require.Equal(t, upToDate, compile(0))
	require.Equal(t, 1, compiles, "no compile")

	// A commit of the sources that changes no compiled file: the record
	// only.
	before := filesOf(t, outputStore)
	commitFiles(t, sourcesStore, "notes", map[string]string{"README.md": "notes\n"})
	compile(0)
	require.Equal(t, 2, compiles)
	after := filesOf(t, outputStore)
	require.NotEqual(t, before[gitops.SourcesFile], after[gitops.SourcesFile])
	delete(before, gitops.SourcesFile)
	delete(after, gitops.SourcesFile)
	require.Equal(t, before, after, "the record only")
	require.Equal(t, sourcesStore.Head(), record().Repositories[0].Revision)
	require.Equal(t, upToDate, compile(0))

	// A commit of the catalog.
	commitFiles(t, catalogStore, "notes", map[string]string{"README.md": "notes\n"})
	compile(0)
	require.Equal(t, 3, compiles)
	require.Equal(t, upToDate, compile(0))

	// The output changed by another hand: compiled again, the change gone.
	commitFiles(t, outputStore, "a hand", map[string]string{"compiled/projects/extra.yaml": "kind: ConfigMap\n"})
	compile(0)
	require.Equal(t, 4, compiles)
	require.NotContains(t, filesOf(t, outputStore), "compiled/projects/extra.yaml")

	// Another okdp-gitops.
	buildinfo.Version = "9.9.10"
	compile(0)
	require.Equal(t, 5, compiles)
	require.Equal(t, "9.9.10", record().Version)
	require.Equal(t, upToDate, compile(0))

	// Another topology: the chart checks, a path prefix.
	compile(0, "--path-prefix", "deploy")
	require.Equal(t, 6, compiles)
	require.Equal(t, upToDate, compile(0, "--path-prefix", "deploy"))

	// A local catalog: compiled, the record removed; then back.
	compile(0, "--path-prefix", "deploy", "--catalog", "test-charts="+catalogFixture)
	require.Equal(t, 7, compiles)
	require.NotContains(t, filesOf(t, outputStore), gitops.SourcesFile)
	compile(0, "--path-prefix", "deploy")
	require.Equal(t, 8, compiles)
	record()

	// --check never stops early, nor writes the record.
	commitFiles(t, sourcesStore, "notes", map[string]string{"README.md": "more notes\n"})
	require.Contains(t, compile(0, "--path-prefix", "deploy", "--check"), "okdp-gitops: up to date\n")

	// A project kept: no record, the next run compiles again; the report
	// names the revision of the sources the kept output is the compile
	// of, the record's, and the files written all the same.
	compiled := record().Repositories[0].Revision
	commitFiles(t, sourcesStore, "a broken instance", map[string]string{"projects/demo/instances/legacy/config.yaml": "database: nowhere\n"})
	since := "okdp-gitops: demo: kept since " + compiled + " of " + sourcesURL + "#main (sources.yaml), 11 file(s) re-derived\n"
	require.Contains(t, stderrOf(t, func() { compile(1, "--path-prefix", "deploy") }), since)
	require.Contains(t, stderrOf(t, func() { compile(1, "--path-prefix", "deploy") }), since, "the record as it was")
	require.Equal(t, 10, compiles)
}
