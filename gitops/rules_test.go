package gitops

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/internal/rulestest"
)

// TestCompilerRules: a Compiler's rule set reaches its compiles and the
// check of a project's name.
func TestCompilerRules(t *testing.T) {
	set := rulestest.Edited(t, func(s string) string {
		return strings.Replace(s, "[argocd, flux-system, okdp-releases]", "[argocd, flux-system, okdp-releases, lab]", 1)
	})
	var got *compiler.RuleSet
	c := &Compiler{Rules: set, build: func(_ context.Context, _ []compiler.Repository, o compiler.BuildOptions) (*compiler.Tree, error) {
		got = o.Rules
		return &compiler.Tree{}, nil
	}}
	if _, err := c.buildTree(t.Context(), nil); err != nil || got != set {
		t.Errorf("the compile's rule set: %p, %v", got, err)
	}
	d, _ := testDeployments(t)
	if err := d.CheckProjectName(t.Context(), "lab"); err != nil {
		t.Errorf("the standard set: %v", err)
	}
	d.Compiler = c
	if err := d.CheckProjectName(t.Context(), "lab"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("the Compiler's set: %v", err)
	}
}

// TestCompilerSkipRender: a Compiler renders the charts an instance gives
// upstream values to by default (the CI's), SkipRender leaves that out (the
// control plane's): its chart's template that fails passes, what the
// compile checks of the values still refused.
func TestCompilerSkipRender(t *testing.T) {
	charts := fstest.MapFS{
		"charts.example.org/app-1.0.0/app/Chart.yaml":       {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		"charts.example.org/app-1.0.0/app/values.yaml":      {Data: []byte("{}\n")},
		"charts.example.org/app-1.0.0/app/templates/t.yaml": {Data: []byte(`{{ fail "the chart was rendered" }}`)},
	}
	tree := func(config string) fstest.MapFS {
		return fstest.MapFS{
			"platform.yaml":                             {Data: []byte("global:\n  okdp:\n    ingress: {suffix: okdp.sandbox}\n")},
			"catalog/app/1.0.0/Chart.yaml":              {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\ndependencies:\n  - {name: app, repository: https://charts.example.org, version: 1.0.0}\n")},
			"catalog/app/1.0.0/okdp.yaml":               {Data: []byte("upstream:\n  app: {}\n")},
			"projects/demo/project.yaml":                {Data: []byte("{}\n")},
			"projects/demo/instances/web/instance.yaml": {Data: []byte("service: app\nversion: 1.0.0\n")},
			"projects/demo/instances/web/config.yaml":   {Data: []byte(config)},
		}
	}
	compile := func(c *Compiler, tree fstest.MapFS) error {
		t.Helper()
		got, err := c.buildTree(t.Context(), []compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}})
		if err != nil {
			t.Fatal(err)
		}
		return got.Instance("demo", "web").Err()
	}
	given := tree("upstream:\n  app:\n    something: 1\n")

	rendering := NewCompiler(&catalog.Loader{})
	rendering.Charts = compiler.StaticCharts(charts)
	if err := compile(rendering, given); err == nil || !strings.Contains(err.Error(), "the chart was rendered") {
		t.Errorf("the default renders: %v", err)
	}

	skipping := NewCompiler(&catalog.Loader{})
	skipping.Charts = compiler.StaticCharts(charts)
	skipping.SkipRender = true
	if err := compile(skipping, given); err != nil {
		t.Errorf("SkipRender: %v", err)
	}
	if err := compile(skipping, tree("upstream:\n  app:\n    extraResources: [{kind: ConfigMap}]\n")); err == nil || !strings.Contains(err.Error(), "extraResources is denied") {
		t.Errorf("the checks of the values stay: %v", err)
	}
}
