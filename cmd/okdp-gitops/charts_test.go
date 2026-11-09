package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// With --charts, the values are checked against the upstream charts of the
// cache, as the control plane checks them: a value the chart's schema
// refuses fails the compile, and nothing is written.
func TestChartsCheckTheValuesAgainstTheUpstreamCharts(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	cache := t.TempDir()
	chart := func(dir, name, version, schema string) {
		// The archive beside the chart: the mark of a chart the cache holds whole.
		write(t, filepath.Join(cache, dir, name+"-"+version, name+"-"+version+".tgz"), "the archive")
		write(t, filepath.Join(cache, dir, name+"-"+version, name, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: "+version+"\n")
		write(t, filepath.Join(cache, dir, name+"-"+version, name, "values.yaml"), "{}\n")
		write(t, filepath.Join(cache, dir, name+"-"+version, name, "values.schema.json"), schema)
	}
	chart("charts.example.org", "cluster", "0.3.0", `{"type": "object"}`)
	chart("charts.example.org", "app", "1.8.0", `{"type": "object", "properties": {"database": {"type": "string"}}}`)
	args := []string{"compile", "--tree", root, "--output", out, "--path-prefix", "", "--catalog", "test-charts=" + catalogFixture, "--charts", cache}

	require.Equal(t, 1, run(args), "the app chart's database, an object, is no string")
	got, err := readTree(out)
	require.NoError(t, err)
	require.NotContains(t, got, "compiled/projects/demo/instances/demo-app/instance.values.yaml", "a compile that fails writes nothing")

	chart("charts.example.org", "app", "1.8.0", `{"type": "object", "properties": {"database": {"type": "object"}}}`)
	require.Equal(t, 0, run(args))
	require.Equal(t, 0, run(append(args, "--check")))
}

// The CI's compile renders: a chart an instance gives upstream values to
// is rendered, what it deploys checked, the chart's failure an instance's
// failure. It is the boundary the control plane relies on
// (gitops.Compiler.SkipRender is the control plane's, not the CI's).
func TestTheCompileRendersWhatAProjectGivesUpstreamValuesTo(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	write(t, filepath.Join(root, "projects/demo/instances/app/config.yaml"), "upstream:\n  app:\n    anything: 1\n")
	cache := t.TempDir()
	chart := func(name, version, template string) {
		dir := filepath.Join(cache, "charts.example.org", name+"-"+version)
		write(t, filepath.Join(dir, name+"-"+version+".tgz"), "the archive")
		write(t, filepath.Join(dir, name, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: "+version+"\n")
		write(t, filepath.Join(dir, name, "values.yaml"), "{}\n")
		write(t, filepath.Join(dir, name, "templates", "t.yaml"), template)
	}
	chart("cluster", "0.3.0", "")
	chart("app", "1.8.0", `{{ fail "the chart was rendered" }}`)
	args := []string{"compile", "--tree", root, "--output", out, "--path-prefix", "", "--catalog", "test-charts=" + catalogFixture, "--charts", cache}

	require.Equal(t, 1, run(args), "the render of the app chart fails: the instance is refused")
	got, err := readTree(out)
	require.NoError(t, err)
	require.NotContains(t, got, "compiled/projects/demo/instances/demo-app/instance.values.yaml")

	chart("app", "1.8.0", "")
	require.Equal(t, 0, run(args), "the same compile, the chart rendering")
}
