package chartcheck

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
)

var app = chart.Ref{Repository: "https://x", Chart: "app", Version: "1.0.0"}

// final: the final values of the key app, rendered by app, its appended
// lists.
func final(values M, appended ...[]string) compiler.FinalValuesOf {
	f := compiler.FinalValuesOf{Values: map[string]map[string]any{"app": values}, Charts: map[string]chart.Ref{"app": app}}
	if appended != nil {
		f.Appended = map[string][][]string{"app": appended}
	}
	return f
}

// TestCheck: the schema of each chart checked over its defaults, every
// violation reported with its path under the key and the chart; an
// appended list that leaves out items of the chart's default list warned
// of, once per path.
func TestCheck(t *testing.T) {
	cache := fstest.MapFS{
		"x/app-1.0.0/app/values.yaml":        {Data: []byte("replicas: 1\nenv: [A, B]\nargs: [a]\nextra: []\n")},
		"x/app-1.0.0/app/values.schema.json": {Data: []byte(`{"required": ["replicas"], "properties": {"replicas": {"maximum": 3}}}`)},
	}
	for name, tc := range map[string]struct {
		final    compiler.FinalValuesOf
		warnings []string
	}{
		"no list":        {final(M{"image": "x"}), nil},
		"a list kept":    {final(M{"env": []any{"A", "B", "C"}}, []string{"env"}), nil},
		"an item lost":   {final(M{"env": []any{"B", "C"}}, []string{"env"}), []string{"upstream.app.env: appended to the package's list, not to the default list of app-1.0.0, which Helm replaces: the final list lacks 1 of its 2 items; restate them in the package's values of app (its defaults or values file) to keep them"}},
		"all items lost": {final(M{"env": []any{"C"}, "args": []any{"b"}}, []string{"env"}, []string{"args"}), []string{"upstream.app.env: appended to the package's list, not to the default list of app-1.0.0, which Helm replaces: the final list lacks all its items; restate them in the package's values of app (its defaults or values file) to keep them", "upstream.app.args: appended to the package's list, not to the default list of app-1.0.0, which Helm replaces: the final list lacks all its items; restate them in the package's values of app (its defaults or values file) to keep them"}},
		"not appended":   {final(M{"env": []any{"C"}}), nil},
		"empty default":  {final(M{"extra": []any{"x"}}, []string{"extra"}), nil},
	} {
		t.Run(name, func(t *testing.T) {
			warnings, err := New(Static(cache)).Check(t.Context(), tc.final)
			if err != nil || !reflect.DeepEqual(warnings, tc.warnings) {
				t.Errorf("got %v, warnings:\n got %q\nwant %q", err, warnings, tc.warnings)
			}
		})
	}
	_, err := New(Static(cache)).Check(t.Context(), final(M{"replicas": float64(5)}))
	if want := "compiled values don't meet the specifications of the schema(s) in the following chart(s):\napp (app-1.0.0):\n- at '/replicas': maximum: got 5, want 3"; err == nil || err.Error() != want {
		t.Errorf("schema: got %v, want %q", err, want)
	}
	// Nil charts check nothing.
	if w, err := (*Charts)(nil).Check(t.Context(), final(M{"replicas": float64(5)})); w != nil || err != nil {
		t.Errorf("nil charts: %q %v", w, err)
	}
}

// TestSchemaError: the charts with violations, in name order, none
// without.
func TestSchemaError(t *testing.T) {
	err := SchemaError("values", map[string]string{"b": "- at '/x': b", "a": "- at '/y': a", "c": ""})
	if want := "values don't meet the specifications of the schema(s) in the following chart(s):\na:\n- at '/y': a\nb:\n- at '/x': b"; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
	if err := SchemaError("values", map[string]string{"c": ""}); err != nil {
		t.Errorf("no violation: got %v", err)
	}
}

// TestCheckMissing: a chart a static cache lacks fails with the command
// that pulls it, which the catalogs' compile-charts.sh runs.
func TestCheckMissing(t *testing.T) {
	_, err := New(Static(fstest.MapFS{})).Check(t.Context(), final(M{}))
	if want := "app: chart app-1.0.0 is not in the chart cache: helm pull app --repo https://x --version 1.0.0 --untar -d <chart cache>/x/app-1.0.0"; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// TestCheckGlobal: values without a global are checked with an empty one,
// as Helm gives every subchart: a schema that refuses it fails.
func TestCheckGlobal(t *testing.T) {
	cache := fstest.MapFS{"x/app-1.0.0/app/values.schema.json": {Data: []byte(`{"additionalProperties": false, "properties": {"a": {}}}`)}}
	_, err := New(Static(cache)).Check(t.Context(), final(M{"a": 1.0}))
	if want := "- at '': additional properties 'global' not allowed"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("got %v, want an error ending with %q", err, want)
	}
}

// TestCheckSchemaRef: a schema's $ref to a URL is never loaded: the check
// fetches nothing a chart names.
func TestCheckSchemaRef(t *testing.T) {
	cache := fstest.MapFS{"x/app-1.0.0/app/values.schema.json": {Data: []byte(`{"$ref": "https://example.org/x.json"}`)}}
	_, err := New(Static(cache)).Check(t.Context(), final(M{}))
	if want := `no URLLoader registered for "https://example.org/x.json"`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want an error containing %q", err, want)
	}
}

// TestCheckNull: a null of the final values deletes the chart's default
// before the check, as Helm does: the schema does not see it.
func TestCheckNull(t *testing.T) {
	cache := fstest.MapFS{
		"x/app-1.0.0/app/values.yaml":        {Data: []byte("labels: {a: b}\n")},
		"x/app-1.0.0/app/values.schema.json": {Data: []byte(`{"properties": {"labels": {"type": "object"}}}`)},
	}
	if _, err := New(Static(cache)).Check(t.Context(), final(M{"labels": nil})); err != nil {
		t.Error(err)
	}
}

// source: a chart source that counts the charts it is asked for and fails
// those of failed; cancel, when set, is called on the first Get.
type source struct {
	mu     sync.Mutex
	asked  map[chart.Ref]int
	failed map[chart.Ref]error
	fsys   fs.FS
	cancel func()
}

func (s *source) Get(_ context.Context, ref chart.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked[ref]++
	if s.cancel != nil {
		s.cancel()
	}
	return s.failed[ref]
}

func (s *source) FS() fs.FS { return s.fsys }

// TestFetch: each chart fetched once per build; one that could not be
// leaves its keys unchecked and is reported, the others checked; a context
// that ends is the compile's error, not the chart's.
func TestFetch(t *testing.T) {
	gone := chart.Ref{Repository: "https://x", Chart: "gone", Version: "1.0.0"}
	src := &source{
		asked:  map[chart.Ref]int{},
		failed: map[chart.Ref]error{gone: errors.New("away")},
		fsys:   fstest.MapFS{"x/app-1.0.0/app/values.schema.json": {Data: []byte(`{"required": ["needed"]}`)}},
	}
	c := New(src)
	f := compiler.FinalValuesOf{
		Values: map[string]map[string]any{"app": {}, "gone": {"a": 1.0}},
		Charts: map[string]chart.Ref{"app": app, "gone": gone},
	}
	if _, err := c.Check(t.Context(), f); err == nil || !strings.Contains(err.Error(), "needed") {
		t.Fatalf("the fetched chart's schema: %v", err)
	}
	if _, err := c.Check(t.Context(), f); err == nil {
		t.Fatal("checked again: no error")
	}
	if src.asked[app] != 1 || src.asked[gone] != 1 {
		t.Errorf("asked %v, want each chart once", src.asked)
	}
	if got := c.Failures(); len(got) != 1 || got[gone] == nil || c.Failed(app) != nil {
		t.Errorf("failures: %v", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	src = &source{asked: map[chart.Ref]int{}, fsys: fstest.MapFS{}, cancel: cancel}
	c = New(src)
	if err := c.Fetch(ctx, []chart.Ref{app}); !errors.Is(err, context.Canceled) || c.Failures() != nil {
		t.Errorf("a context cancelled: %v, failures %v", err, c.Failures())
	}
	if err := c.Fetch(t.Context(), []chart.Ref{app}); err != nil || src.asked[app] != 2 {
		t.Errorf("asked again after the cancel: %v, %d", err, src.asked[app])
	}
}

// TestCoalesce: Helm's coalescing: values over the defaults, maps merged,
// a null deleting the default; neither argument changed.
func TestCoalesce(t *testing.T) {
	defaults := M{"x": M{"a": 1.0, "b": 2.0}, "l": []any{1.0}}
	values := M{"x": M{"b": nil}, "l": []any{2.0}}
	got, err := Coalesce(defaults, values)
	if want := (M{"x": M{"a": 1.0}, "l": []any{2.0}}); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v %v, want %#v", got, err, want)
	}
	if defaults["x"].(M)["b"] != 2.0 || values["x"].(M)["b"] != nil || len(values["x"].(M)) != 1 {
		t.Errorf("an argument changed: %v %v", defaults, values)
	}
}
