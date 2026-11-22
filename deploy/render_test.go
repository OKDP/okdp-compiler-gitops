package deploy_test

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/deploy"
)

// renderFixture: a tree of a package app, whose chart (ref, in chartsDir of
// the cache) renders the objects its values extras names (a key no rule
// denies, as extraResources is) and, for
// the key ips, a Service with these externalIPs; the project demo, an
// instance web of it with the upstream values of config, a suffix for hosts.
func renderFixture(config string) (src, charts fstest.MapFS) {
	chartDir := "charts.example.org/app-1.0.0/app"
	charts = fstest.MapFS{
		chartDir + "/Chart.yaml":            {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		chartDir + "/values.yaml":           {Data: []byte("extras: []\nextraResources: []\n")},
		chartDir + "/templates/extras.yaml": {Data: []byte("{{ range .Values.extras }}\n---\n{{ toYaml . }}\n{{ end }}\n")},
		chartDir + "/templates/svc.yaml": {Data: []byte(`{{ with .Values.ips }}apiVersion: v1
kind: Service
metadata: {name: x}
spec: {externalIPs: {{ toJson . }}}
{{ end }}`)},
	}
	src = fstest.MapFS{
		"platform.yaml":                             {Data: []byte("global:\n  okdp:\n    ingress: {suffix: okdp.sandbox}\n")},
		"catalog/app/1.0.0/Chart.yaml":              {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\ndependencies:\n  - {name: app, repository: https://charts.example.org, version: 1.0.0}\n")},
		"catalog/app/1.0.0/okdp.yaml":               {Data: []byte("upstream:\n  app: {}\n")},
		"projects/demo/project.yaml":                {Data: []byte("{}\n")},
		"projects/demo/instances/web/instance.yaml": {Data: []byte("service: app\nversion: 1.0.0\n")},
		"projects/demo/instances/web/config.yaml":   {Data: []byte(config)},
	}
	return src, charts
}

// compileFixture: the outcome of the instance web of renderFixture
// compiled against charts, with the standard rule set.
func compileFixture(t *testing.T, config string) (*tree.Tree, error) {
	t.Helper()
	src, charts := renderFixture(config)
	tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: ".", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(deploy.StaticCharts(charts))})
	if err != nil {
		t.Fatal(err)
	}
	ti := tr.Instance("demo", "web")
	if ti == nil {
		t.Fatalf("no instance: problems %q", tr.Problems())
	}
	return tr, ti.Err()
}

// TestRenderedExtraResources: objects a chart renders from its values
// refused by the render, under a key no rule denies (extras): an Ingress of
// a foreign host, a HelmRelease; what the project may deploy passes.
func TestRenderedExtraResources(t *testing.T) {
	const foreignIngress = `upstream:
  app:
    extras:
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
    extras:
      - apiVersion: helm.toolkit.fluxcd.io/v2
        kind: HelmRelease
        metadata: {name: x}
        spec: {chart: {spec: {chart: x}}}
`
	const allowed = `upstream:
  app:
    extras:
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
	if _, err := compileFixture(t, foreignIngress); err == nil || !strings.Contains(err.Error(), `upstream.app (rendered): objects: Ingress/x: spec.rules[0].host: host "keycloak.okdp.sandbox" is not served by demo`) {
		t.Errorf("a foreign host through extraResources: %v", err)
	}
	if _, err := compileFixture(t, helmRelease); err == nil || !strings.Contains(err.Error(), "upstream.app (rendered): objects: HelmRelease/x: an object of the deployer's kinds") {
		t.Errorf("a HelmRelease through extraResources: %v", err)
	}
	if _, err := compileFixture(t, allowed); err != nil {
		t.Errorf("what the project may deploy: %v", err)
	}
	// Under extraResources, the deny-list refuses first, with the clear message.
	if _, err := compileFixture(t, strings.Replace(foreignIngress, "extras:", "extraResources:", 1)); err == nil || !strings.Contains(err.Error(), "extraResources is denied to the instance's upstream values") {
		t.Errorf("the deny-list: %v", err)
	}
}

// TestRenderedExternalIPs: a Service with spec.externalIPs refused by the
// render, without the key on the deny-list.
func TestRenderedExternalIPs(t *testing.T) {
	_, err := compileFixture(t, "upstream:\n  app:\n    ips: [1.2.3.4]\n")
	if err == nil || !strings.Contains(err.Error(), "upstream.app (rendered): objects: Service/x: spec.externalIPs") {
		t.Errorf("got %v", err)
	}
}

// TestRenderOnlyTenantKeys: a key the instance gives no upstream values to
// is not rendered: the chart failing to fetch does not refuse it, a chart
// that cannot render neither.
func TestRenderOnlyTenantKeys(t *testing.T) {
	src, charts := renderFixture("")
	delete(charts, "charts.example.org/app-1.0.0/app/Chart.yaml")
	tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: ".", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(deploy.StaticCharts(charts))})
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
	src, _ := renderFixture("upstream:\n  app:\n    something: 1\n")
	src["projects/demo/instances/other/instance.yaml"] = &fstest.MapFile{Data: []byte("service: app\nversion: 1.0.0\n")}
	tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: ".", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(failedCharts{errors.New("the registry is away")})})
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
	src, charts := renderFixture("upstream:\n  app:\n    something: 1\n")
	charts["charts.example.org/app-1.0.0/app/templates/fail.yaml"] = &fstest.MapFile{Data: []byte(`{{ fail "rendered" }}`)}
	build := func(src fstest.MapFS, source deploy.ChartSource, skip bool) error {
		t.Helper()
		tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: ".", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{Charts: deploy.NewCharts(source), SkipRender: skip})
		if err != nil {
			t.Fatal(err)
		}
		return tr.Instance("demo", "web").Err()
	}
	if err := build(src, deploy.StaticCharts(charts), false); err == nil || !strings.Contains(err.Error(), "rendered") {
		t.Errorf("the default renders: %v", err)
	}
	if err := build(src, deploy.StaticCharts(charts), true); err != nil {
		t.Errorf("without the render: %v", err)
	}
	if err := build(src, failedCharts{errors.New("the registry is away")}, true); err != nil {
		t.Errorf("a chart that could not be fetched, without the render: %v", err)
	}
	denied, _ := renderFixture("upstream:\n  app:\n    extraResources: [{kind: ConfigMap}]\n")
	if err := build(denied, deploy.StaticCharts(charts), true); err == nil || !strings.Contains(err.Error(), "extraResources is denied") {
		t.Errorf("the deny-list stays: %v", err)
	}
}
