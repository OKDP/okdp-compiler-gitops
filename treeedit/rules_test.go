package treeedit

import (
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
)

// TestCheckProjectNameRules: a project's name is checked with the caller's
// rule set, the standard one without.
func TestCheckProjectNameRules(t *testing.T) {
	if err := CheckProjectName(nil, memTree{}, "lab"); err != nil {
		t.Errorf("the standard set: %v", err)
	}
	if err := CheckProjectName(&compiler.RuleSet{}, memTree{}, "lab"); err == nil || !strings.Contains(err.Error(), "no rule set was loaded") {
		t.Errorf("the caller's set, not loaded: %v", err)
	}
}
