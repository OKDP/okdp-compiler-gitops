package helm

import (
	"reflect"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/value"
)

// M: a values map.
type M = map[string]any

// TestKeepSynced: an object Helm keeps, or an item of a List, gets Argo
// CD's sync options, an option it sets of the same name left as it is;
// the annotation read as Helm reads it; the others unchanged.
func TestKeepSynced(t *testing.T) {
	shared := M{keepAnnotation: "keep"}
	objects := []map[string]any{
		{"kind": "Database", "metadata": M{"name": "a", "annotations": M{keepAnnotation: "keep"}}},
		{"kind": "Secret", "metadata": M{"name": "b", "annotations": M{keepAnnotation: " Keep ", syncOptionsAnnotation: "ServerSideApply=true"}}},
		{"kind": "Secret", "metadata": M{"name": "c", "annotations": M{keepAnnotation: "keep", syncOptionsAnnotation: "Prune=false, Replace=true"}}},
		{"kind": "Secret", "metadata": M{"name": "d", "annotations": M{keepAnnotation: "keep", syncOptionsAnnotation: "Prune=confirm,Delete=false"}}},
		{"kind": "Secret", "metadata": M{"name": "e", "annotations": shared}},
		{"kind": "Secret", "metadata": M{"name": "f", "annotations": shared}},
		{"kind": "Secret", "metadata": M{"name": "g", "annotations": M{keepAnnotation: "delete"}}},
		{"kind": "Secret", "metadata": M{"name": "h"}},
		{"kind": "Secret"},
		{"kind": "List", "items": []any{M{"kind": "Database", "metadata": M{"name": "i", "annotations": M{keepAnnotation: "keep"}}}}},
	}
	keepSynced(objects)
	want := map[string]any{
		"a": "Prune=false,Delete=false",
		"b": "ServerSideApply=true,Prune=false,Delete=false",
		"c": "Prune=false, Replace=true,Delete=false",
		"d": "Prune=confirm,Delete=false",
		"e": "Prune=false,Delete=false",
		"f": "Prune=false,Delete=false",
		"g": nil,
		"h": nil,
	}
	if got := objects[9]["items"].([]any)[0].(M)["metadata"].(M)["annotations"].(M)[syncOptionsAnnotation]; got != "Prune=false,Delete=false" {
		t.Errorf("an item of a List: got %v", got)
	}
	for _, o := range objects {
		meta, _ := o["metadata"].(M)
		name, _ := meta["name"].(string)
		annotations, _ := meta["annotations"].(M)
		if got := annotations[syncOptionsAnnotation]; name != "" && got != want[name] {
			t.Errorf("%s: got %v, want %v", name, got, want[name])
		}
	}
	if !reflect.DeepEqual(objects[8], map[string]any{"kind": "Secret"}) {
		t.Errorf("an object without metadata: %v", objects[8])
	}
}

// TestKept: the umbrella's values and those of a plain helm install give
// an object Helm keeps Argo CD's sync options; the Result is not changed.
func TestKept(t *testing.T) {
	res := &compiler.Result{
		Objects:       []map[string]any{{"kind": "Database", "metadata": M{"name": "a", "annotations": M{keepAnnotation: "keep"}}}},
		ObjectsSource: objectsSource,
	}
	before := value.Copy(res.Objects[0])
	umbrella, err := Write(res)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := WriteValues(res)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{ValuesFile: umbrella[ValuesFile], ObjectsValues: plain[ObjectsValues]} {
		if !strings.Contains(string(b), "argocd.argoproj.io/sync-options: Prune=false,Delete=false\n") {
			t.Errorf("%s:\n%s", name, b)
		}
	}
	if !reflect.DeepEqual(res.Objects[0], before) {
		t.Errorf("the Result changed: %v", res.Objects[0])
	}
}
