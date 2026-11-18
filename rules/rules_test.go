package rules

import (
	"slices"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
)

// TestStandard: the standard set holds a platform's rules.
func TestStandard(t *testing.T) {
	r := Standard()
	if r.Version() == "" || !slices.Contains(compiler.EngineNamespaces(r), "argocd") || len(compiler.Roles(r)) != 3 {
		t.Errorf("the standard set: version %q, roles %v", r.Version(), compiler.Roles(r))
	}
	if Or(nil) != r {
		t.Error("Or(nil) is not the standard set")
	}
}
