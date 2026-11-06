package yamldoc

import (
	"reflect"
	"testing"

	"github.com/okdp/okdp-compiler/value"
)

// TestDecode: a document reads as Helm reads it, whatever its root: YAML
// 1.1 booleans, string keys, every number a float64, the last of a key
// given twice.
func TestDecode(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want any
	}{
		"map":           {"a: 1\nb: yes\n", value.M{"a": float64(1), "b": true}},
		"integer key":   {"1: a\n", value.M{"1": "a"}},
		"duplicate key": {"a: 1\na: 2\n", value.M{"a": float64(2)}},
		"empty":         {"# nothing\n", value.M{}},
		"block list":    {"- privileged: on\n  runAsUser: 0\n- 3\n", []any{value.M{"privileged": true, "runAsUser": float64(0)}, float64(3)}},
		"flow list":     {"[a, {b: no}]\n", []any{"a", value.M{"b": false}}},
		"anchors":       {"- &a {x: 1}\n- *a\n- <<: *a\n  z: 2\n", []any{value.M{"x": float64(1)}, value.M{"x": float64(1)}, value.M{"x": float64(1), "z": float64(2)}}},
		"quoted":        {"- 'yes'\n", []any{"yes"}},
		"scalar":        {"plain text\n", nil},
		"number":        {"3\n", nil},
	} {
		got, err := Decode([]byte(tc.doc))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", name, got, tc.want)
		}
	}
	for name, doc := range map[string]string{
		"not yaml": "a: [\n",
		"map key":  "? [a]\n: b\n",
	} {
		if v, err := Decode([]byte(doc)); err == nil {
			t.Errorf("%s: got %#v, want an error", name, v)
		}
	}
}
