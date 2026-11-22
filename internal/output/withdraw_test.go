package output

import (
	"github.com/okdp/okdp-gitops/tree"

	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/rules"
)

// TestViolationHosts: what a refused instance deployed is checked as a
// compile checks it, the platform's settings reaching every check: a host
// of its deployed values (the finalValues hook), the objects its chart
// renders with them (check object first on every object, then the objects
// hook), a host it serves passing.
func TestViolationHosts(t *testing.T) {
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
		"charts.example.org/app-1.0.0/app/templates/service.yaml": {Data: []byte(`{{- with .Values.dnsHost }}
apiVersion: v1
kind: Service
metadata:
  name: web
  annotations:
    external-dns.alpha.kubernetes.io/hostname: {{ . }}
spec: {}
{{- end }}
`)},
		"charts.example.org/app-1.0.0/app/templates/configmap.yaml": {Data: []byte(`{{- with .Values.cmNamespace }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: z
  namespace: {{ . }}
{{- end }}
`)},
	}
	settings := compiler.ContextLayer{Level: 0, Values: map[string]any{"global": map[string]any{"okdp": map[string]any{"ingress": map[string]any{"suffix": "okdp.sandbox"}}}}}
	for name, tc := range map[string]struct {
		values string // the deployed and given upstream values of the key app
		want   string // "" when it passes
	}{
		"a host it serves":          {"app:\n  ingress: {host: web.demo.okdp.sandbox}\n  renderHost: web.demo.okdp.sandbox\n", ""},
		"a host of its values":      {"app:\n  ingress: {host: web.example.org}\n", "app: ingress.host: host \"web.example.org\" is not served by demo: a host <label>.demo.okdp.sandbox (one DNS label), or one listed for it in global.okdp.policy.ingressHosts; the platform's projects serve any host (global.okdp.policy.privileged), demo is not one of them"},
		"a host its chart renders":  {"app:\n  renderHost: web.example.org\n", "upstream.app (rendered): objects: Ingress/web: spec.rules[0].host: host \"web.example.org\" is not served by demo: a host <label>.demo.okdp.sandbox (one DNS label), or one listed for it in global.okdp.policy.ingressHosts; the platform's projects serve any host (global.okdp.policy.privileged), demo is not one of them"},
		"a host of an annotation":   {"app:\n  dnsHost: web.example.org\n", `upstream.app (rendered): objects: Service/web: metadata.annotations["external-dns.alpha.kubernetes.io/hostname"]: host "web.example.org" is not served by demo: a host <label>.demo.okdp.sandbox (one DNS label), or one listed for it in global.okdp.policy.ingressHosts; the platform's projects serve any host (global.okdp.policy.privileged), demo is not one of them`},
		"an object's namespace too": {"app:\n  renderHost: web.example.org\n  cmNamespace: other\n", "upstream.app (rendered): objects: ConfigMap/z: namespace other, not the instance's (demo): the platform's projects write other namespaces (global.okdp.policy.privileged), demo is not one of them"},
	} {
		t.Run(name, func(t *testing.T) {
			var given map[string]any
			if err := yaml.Unmarshal([]byte(tc.values), &given); err != nil {
				t.Fatal(err)
			}
			ti := &tree.Instance{Name: "web", Inputs: compiler.Inputs{Rules: rules.Standard(), Namespace: "demo", Release: "demo-web", Context: []compiler.ContextLayer{settings}, Values: []map[string]any{{"upstream": given}}}}
			files := map[string][]byte{
				"projects/demo/instances/demo-web/Chart.yaml":           []byte("dependencies:\n  - {name: app, repository: https://charts.example.org, version: 1.0.0}\n"),
				"projects/demo/instances/demo-web/instance.values.yaml": []byte(tc.values),
			}
			got := ""
			if err := violation(ti, files, charts); err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Errorf("violation:\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
