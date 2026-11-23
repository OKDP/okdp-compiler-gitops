package rules

import (
	"slices"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
)

// TestStandard: the standard set holds a platform's rules.
func TestStandard(t *testing.T) {
	r := Standard()
	if r.Version() == "" || !slices.Contains(compiler.EngineNamespaces(r), "argocd") {
		t.Errorf("the standard set: version %q", r.Version())
	}
	if Or(nil) != r {
		t.Error("Or(nil) is not the standard set")
	}
}
