package render

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/okdp/okdp-compiler/chart"
)

// chartFS: a chart cache holding files under the chart directory of ref.
func chartFS(ref chart.Ref, files map[string]string) fstest.MapFS {
	dir, err := ref.Dir()
	if err != nil {
		panic(err)
	}
	out := fstest.MapFS{}
	for name, data := range files {
		out[dir+"/"+name] = &fstest.MapFile{Data: []byte(data)}
	}
	return out
}

var appRef = chart.Ref{Repository: "https://charts.example.org", Chart: "app", Version: "1.0.0"}

const chartYAML = "apiVersion: v2\nname: app\nversion: 1.0.0\n"

// TestObjects: what `helm template` of a key gives: the release and
// namespace of the request, the values given, the chart's alias; the
// manifests split, hooks and crds/ kept; NOTES.txt and partials not.
func TestObjects(t *testing.T) {
	charts := chartFS(appRef, map[string]string{
		"Chart.yaml":            chartYAML,
		"values.yaml":           "port: 80\nreplicas: 1\n",
		"templates/_helper.tpl": `{{ define "app.name" }}{{ .Release.Name }}-{{ .Chart.Name }}{{ end }}`,
		"templates/web.yaml": `---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "app.name" . }}
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.replicas }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "app.name" . }}
spec:
  ports: [{port: {{ .Values.port }}}]
`,
		"templates/hook.yaml": `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Release.Name }}-hook
  annotations: {"helm.sh/hook": pre-install}
`,
		"templates/NOTES.txt":         "not an object: {{ .Release.Name }}",
		"crds/thing.yaml":             "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: things.example.org}\n",
		"charts/sub/Chart.yaml":       "apiVersion: v2\nname: sub\nversion: 2.0.0\n",
		"charts/sub/templates/x.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: sub-{{ .Release.Name }}}\n",
	})
	objects, err := Objects(Request{Charts: charts, Ref: appRef, Key: "web", Values: map[string]any{"replicas": 3}, Release: "r1", Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, o := range objects {
		by[fmt.Sprint(o["kind"])+"/"+fmt.Sprint(o["metadata"].(map[string]any)["name"])] = o
	}
	for _, name := range []string{"Deployment/r1-web", "Service/r1-web", "Job/r1-hook", "CustomResourceDefinition/things.example.org", "ConfigMap/sub-r1"} {
		if by[name] == nil {
			t.Errorf("%s not rendered: %v", name, by)
		}
	}
	if len(objects) != 5 {
		t.Errorf("%d objects: %v", len(objects), objects)
	}
	if got := by["Deployment/r1-web"]["spec"].(map[string]any)["replicas"]; got != 3.0 {
		t.Errorf("replicas %v, want the value given over the chart's", got)
	}
	if got := by["Deployment/r1-web"]["metadata"].(map[string]any)["namespace"]; got != "demo" {
		t.Errorf("namespace %v", got)
	}
}

// TestObjectsFailures: a template that fails, a manifest that does not
// parse, a chart that does not load: errors.
func TestObjectsFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"a template that fails": {map[string]string{"Chart.yaml": chartYAML, "templates/a.yaml": `{{ fail "no" }}`}, "no"},
		"not yaml":              {map[string]string{"Chart.yaml": chartYAML, "templates/a.yaml": "a: [\n"}, "not valid YAML"},
		"no Chart.yaml":         {map[string]string{"templates/a.yaml": "x"}, "loading the chart"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Objects(Request{Charts: chartFS(appRef, tc.files), Ref: appRef, Key: "web", Release: "r1", Namespace: "demo"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, err := Objects(Request{Charts: fstest.MapFS{}, Ref: appRef, Release: "r1", Namespace: "demo"}); err == nil {
		t.Error("a chart that is not there")
	}
}

// blockedFS: an fs.FS whose every Open waits for release: a render that
// never returns (a template that loops).
type blockedFS struct{ release chan struct{} }

func (b blockedFS) Open(string) (fs.File, error) {
	<-b.release
	return nil, errors.New("released")
}

// TestObjectsTimeout: a render past its timeout fails, its goroutine
// abandoned.
func TestObjectsTimeout(t *testing.T) {
	b := blockedFS{make(chan struct{})}
	defer close(b.release)
	start := time.Now()
	_, err := Objects(Request{Charts: b, Ref: appRef, Release: "r1", Namespace: "demo", Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "rendering took more than") || time.Since(start) > 10*time.Second {
		t.Errorf("got %v after %s", err, time.Since(start))
	}
}
