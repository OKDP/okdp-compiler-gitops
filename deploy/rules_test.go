package deploy_test

import (
	"testing"

	"github.com/okdp/okdp-gitops/deploy"
	"github.com/okdp/okdp-gitops/internal/rulestest"
)

// TestProjectAccessRules: the roles of a project are read with the caller's
// rule set, the standard one without; the roles are the compiler's mandatory
// set's, whichever set is given.
func TestProjectAccessRules(t *testing.T) {
	set := rulestest.Edited(t, func(s string) string { return s })
	file := []byte("access: {ops: editor}\n")
	if _, err := deploy.ProjectAccess(nil, file, "demo"); err != nil {
		t.Errorf("the standard set: %v", err)
	}
	if got, err := deploy.ProjectAccess(set, file, "demo"); err != nil || got["ops"] != "editor" {
		t.Errorf("the caller's set: %v, %v", got, err)
	}
	if _, err := deploy.ProjectAccess(set, []byte("access: {ops: owner}\n"), "demo"); err == nil {
		t.Error("a role of no rule set is given")
	}
}
