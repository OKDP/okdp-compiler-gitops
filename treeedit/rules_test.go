package treeedit

import (
	"strings"
	"testing"

	"github.com/okdp/okdp-gitops/internal/rulestest"
)

// TestCheckProjectNameRules: a project's name is checked with the caller's
// rule set, the standard one without.
func TestCheckProjectNameRules(t *testing.T) {
	set := rulestest.Edited(t, func(s string) string {
		return strings.Replace(s, "[argocd, flux-system, okdp-releases]", "[argocd, flux-system, okdp-releases, lab]", 1)
	})
	if err := CheckProjectName(nil, memTree{}, "lab"); err != nil {
		t.Errorf("the standard set: %v", err)
	}
	if err := CheckProjectName(set, memTree{}, "lab"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("the caller's set: %v", err)
	}
}
