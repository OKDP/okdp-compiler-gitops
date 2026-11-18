// Package rulestest gives this module's tests a rule set of their own: a
// set added to the compiler's mandatory one, which adds nothing, so a test
// can tell it from the standard set. Only tests import it.
package rulestest

import (
	"testing"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/internal/mapfs"
)

// Added: a rule set of its own, the mandatory set's rules alone.
func Added(t *testing.T) *compiler.RuleSet {
	t.Helper()
	r, err := compiler.LoadRules(mapfs.FS{"rules.yaml": &mapfs.File{Data: []byte("apiVersion: okdp-compiler/rules.v1\nversion: 1.0.0\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
