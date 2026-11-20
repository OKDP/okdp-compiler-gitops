package gitops

import (
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/deploy"
)

// TestCompilerHosts: the hosts of an instance's final values are checked
// before the objects its chart renders with them, both with the platform's
// settings; SkipRender keeps the check of the values.
func TestCompilerHosts(t *testing.T) {
	charts := fstest.MapFS{
		"charts.example.org/app-1.0.0/app/Chart.yaml":  {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
		"charts.example.org/app-1.0.0/app/values.yaml": {Data: []byte("{}\n")},
		"charts.example.org/app-1.0.0/app/templates/ingress.yaml": {Data: []byte(`{{- with .Values.renderHost }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: web
spec:
  rules:
    - host: {{ . }}
{{- end }}
`)},
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
	const valuesHost = `app: ingress.host: host "web.example.org" is not served by demo: a host <label>.demo.okdp.sandbox (one DNS label), or one listed for it in global.okdp.policy.ingressHosts; the platform's projects serve any host (global.okdp.policy.privileged), demo is not one of them`
	const renderedHost = `upstream.app (rendered): objects: Ingress/web: spec.rules[0].host: host "other.example.org" is not served by demo: a host <label>.demo.okdp.sandbox (one DNS label), or one listed for it in global.okdp.policy.ingressHosts; the platform's projects serve any host (global.okdp.policy.privileged), demo is not one of them`
	for name, tc := range map[string]struct {
		config     string
		want, skip string // the instance's error, rendered and with SkipRender; "" for none
	}{
		"served":            {"upstream:\n  app:\n    ingress: {host: web.demo.okdp.sandbox}\n    renderHost: web.demo.okdp.sandbox\n", "", ""},
		"a host of a value": {"upstream:\n  app:\n    ingress: {host: web.example.org}\n", valuesHost, valuesHost},
		"a host rendered":   {"upstream:\n  app:\n    renderHost: other.example.org\n", renderedHost, ""},
		"the values first":  {"upstream:\n  app:\n    ingress: {host: web.example.org}\n    renderHost: other.example.org\n", valuesHost, valuesHost},
	} {
		t.Run(name, func(t *testing.T) {
			for _, skip := range []bool{false, true} {
				c := NewCompiler(&catalog.Loader{})
				c.Charts = deploy.StaticCharts(charts)
				c.SkipRender = skip
				got, _, err := c.buildTree(t.Context(), []compiler.Repository{{Name: ".", FS: tree(tc.config), Role: compiler.RoleTree}})
				if err != nil {
					t.Fatal(err)
				}
				msg, want := "", tc.want
				if skip {
					want = tc.skip
				}
				if err := got.Instance("demo", "web").Err(); err != nil {
					msg = err.Error()
				}
				if msg != want {
					t.Errorf("SkipRender %v:\n%s\nwant\n%s", skip, msg, want)
				}
			}
		})
	}
}
