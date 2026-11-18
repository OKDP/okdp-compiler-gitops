package deploy_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/deploy"
	"github.com/okdp/okdp-gitops/internal/rulestest"
)

// renderFixture: a tree of a package app, whose chart (ref, in chartsDir of
// the cache) renders the objects its values extraResources names and, for
// the key ips, a Service with these externalIPs; the project demo, an
// instance web of it with the upstream values of config, a suffix for hosts.
func renderFixture(config string) (tree, charts fstest.MapFS) {
	chartDir := "charts.example.org/app-1.0.0/app"
	charts = fstest.MapFS{
		chartDir + "/Chart.yaml":            {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		chartDir + "/values.yaml":           {Data: []byte("extraResources: []\n")},
		chartDir + "/templates/extras.yaml": {Data: []byte("{{ range .Values.extraResources }}\n---\n{{ toYaml . }}\n{{ end }}\n")},
		chartDir + "/templates/svc.yaml": {Data: []byte(`{{ with .Values.ips }}apiVersion: v1
kind: Service
metadata: {name: x}
spec: {externalIPs: {{ toJson . }}}
{{ end }}`)},
	}
	tree = fstest.MapFS{
		"platform.yaml":                             {Data: []byte("global:\n  okdp:\n    ingress: {suffix: okdp.sandbox}\n")},
		"catalog/app/1.0.0/Chart.yaml":              {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\ndependencies:\n  - {name: app, repository: https://charts.example.org, version: 1.0.0}\n")},
		"catalog/app/1.0.0/okdp.yaml":               {Data: []byte("upstream:\n  app: {}\n")},
		"projects/demo/project.yaml":                {Data: []byte("{}\n")},
		"projects/demo/instances/web/instance.yaml": {Data: []byte("service: app\nversion: 1.0.0\n")},
		"projects/demo/instances/web/config.yaml":   {Data: []byte(config)},
	}
	return tree, charts
}

// compileFixture: the outcome of the instance web of renderFixture
// compiled against charts, the standard rule set edited.
func compileFixture(t *testing.T, config string, edit func(string) string) (error, *compiler.Tree) {
	t.Helper()
	tree, charts := renderFixture(config)
	rules := rulestest.Edited(t, func(s string) string {
		if edit != nil {
			return edit(s)
		}
		return s
	})
	tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}}, deploy.BuildOptions{BuildOptions: compiler.BuildOptions{Rules: rules}, Charts: deploy.NewCharts(deploy.StaticCharts(charts))})
	if err != nil {
		t.Fatal(err)
	}
	ti := tr.Instance("demo", "web")
	if ti == nil {
		t.Fatalf("no instance: problems %q", tr.Problems())
	}
	return ti.Err(), tr
}

// noDeny: the rule set without extraResources in deniedUpstreamKeys: only
// the render stands between the value and the cluster.
func noDeny(s string) string {
	if !strings.Contains(s, "      - extraResources\n") {
		panic("no extraResources in the rule set")
	}
	return strings.Replace(s, "      - extraResources\n", "", 1)
}

// TestRenderedExtraResources: extraResources refused by the render of the
// chart, with the key removed from the deny-list: an Ingress of a foreign
// host, a HelmRelease; what the project may deploy passes.
func TestRenderedExtraResources(t *testing.T) {
	const foreignIngress = `upstream:
  app:
    extraResources:
      - apiVersion: networking.k8s.io/v1
        kind: Ingress
        metadata: {name: x}
        spec:
          rules:
            - host: keycloak.okdp.sandbox
              http: {paths: [{path: /, pathType: Prefix, backend: {service: {name: keycloak, port: {number: 80}}}}]}
`
	const helmRelease = `upstream:
  app:
    extraResources:
      - apiVersion: helm.toolkit.fluxcd.io/v2
        kind: HelmRelease
        metadata: {name: x}
        spec: {chart: {spec: {chart: x}}}
`
	const allowed = `upstream:
  app:
    extraResources:
      - apiVersion: networking.k8s.io/v1
        kind: Ingress
        metadata: {name: x}
        spec:
          rules:
            - host: x.demo.okdp.sandbox
              http: {paths: [{path: /, pathType: Prefix, backend: {service: {name: web, port: {number: 80}}}}]}
      - apiVersion: v1
        kind: ConfigMap
        metadata: {name: c}
`
	if err, _ := compileFixture(t, foreignIngress, noDeny); err == nil || !strings.Contains(err.Error(), `upstream.app (rendered): objects: Ingress/x: spec.rules[0].host: host "keycloak.okdp.sandbox" is not served by demo`) {
		t.Errorf("a foreign host through extraResources: %v", err)
	}
	if err, _ := compileFixture(t, helmRelease, noDeny); err == nil || !strings.Contains(err.Error(), "upstream.app (rendered): objects: HelmRelease/x: an object of the deployer's kinds") {
		t.Errorf("a HelmRelease through extraResources: %v", err)
	}
	if err, _ := compileFixture(t, allowed, noDeny); err != nil {
		t.Errorf("what the project may deploy: %v", err)
	}
	// With the standard set, the deny-list refuses first, with the clear message.
	if err, _ := compileFixture(t, foreignIngress, nil); err == nil || !strings.Contains(err.Error(), "extraResources is denied to the instance's upstream values") {
		t.Errorf("the deny-list: %v", err)
	}
}

// TestRenderedExternalIPs: a Service with spec.externalIPs refused by the
// render, without the key on the deny-list.
func TestRenderedExternalIPs(t *testing.T) {
	err, _ := compileFixture(t, "upstream:\n  app:\n    ips: [1.2.3.4]\n", nil)
	if err == nil || !strings.Contains(err.Error(), "upstream.app (rendered): objects: Service/x: spec.externalIPs") {
		t.Errorf("got %v", err)
	}
}

// TestRenderOnlyTenantKeys: a key the instance gives no upstream values to
// is not rendered: the chart failing to fetch does not refuse it, a chart
// that cannot render neither.
func TestRenderOnlyTenantKeys(t *testing.T) {
	tree, charts := renderFixture("")
	delete(charts, "charts.example.org/app-1.0.0/app/Chart.yaml")
	tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(deploy.StaticCharts(charts))})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Instance("demo", "web").Err(); err != nil {
		t.Errorf("no upstream values, no render: %v", err)
	}
}

// failedCharts: a ChartSource that can fetch none of the charts.
type failedCharts struct{ err error }

func (f failedCharts) Get(context.Context, compiler.ChartRef) error { return f.err }
func (f failedCharts) FS() fs.FS                                    { return fstest.MapFS{} }

// TestRenderRequiresFetchedChart: upstream values given to a chart that
// could not be fetched refuse that instance alone: others compile.
func TestRenderRequiresFetchedChart(t *testing.T) {
	tree, _ := renderFixture("upstream:\n  app:\n    something: 1\n")
	tree["projects/demo/instances/other/instance.yaml"] = &fstest.MapFile{Data: []byte("service: app\nversion: 1.0.0\n")}
	tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(failedCharts{errors.New("the registry is away")})})
	if err != nil {
		t.Fatal(err)
	}
	web := tr.Instance("demo", "web").Err()
	if web == nil || !strings.Contains(web.Error(), "upstream.app: the chart app-1.0.0 could not be fetched") {
		t.Errorf("web: %v", web)
	}
	if err := tr.Instance("demo", "other").Err(); err != nil {
		t.Errorf("an instance without upstream values: %v", err)
	}
}

// TestWithoutRender: the option of the long-running compiles: the chart is
// not rendered (a template that fails passes), a chart that could not be
// fetched refuses nothing; the checks of the values stay (the deny-list).
func TestWithoutRender(t *testing.T) {
	tree, charts := renderFixture("upstream:\n  app:\n    something: 1\n")
	charts["charts.example.org/app-1.0.0/app/templates/fail.yaml"] = &fstest.MapFile{Data: []byte(`{{ fail "rendered" }}`)}
	build := func(tree fstest.MapFS, source deploy.ChartSource, skip bool) error {
		t.Helper()
		tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(source), SkipRender: skip})
		if err != nil {
			t.Fatal(err)
		}
		return tr.Instance("demo", "web").Err()
	}
	if err := build(tree, deploy.StaticCharts(charts), false); err == nil || !strings.Contains(err.Error(), "rendered") {
		t.Errorf("the default renders: %v", err)
	}
	if err := build(tree, deploy.StaticCharts(charts), true); err != nil {
		t.Errorf("without the render: %v", err)
	}
	if err := build(tree, failedCharts{errors.New("the registry is away")}, true); err != nil {
		t.Errorf("a chart that could not be fetched, without the render: %v", err)
	}
	denied, _ := renderFixture("upstream:\n  app:\n    extraResources: [{kind: ConfigMap}]\n")
	if err := build(denied, deploy.StaticCharts(charts), true); err == nil || !strings.Contains(err.Error(), "extraResources is denied") {
		t.Errorf("the deny-list stays: %v", err)
	}
}
