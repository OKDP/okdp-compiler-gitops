package deploy_test

import (
	"strings"
	"testing"

	"github.com/okdp/okdp-gitops/deploy"
	"github.com/okdp/okdp-gitops/internal/rulestest"
)

// TestProjectAccessRules: the roles of a project are read with the caller's
// rule set, the standard one without; one that does not hold the editor
// gives the default group nothing.
func TestProjectAccessRules(t *testing.T) {
	viewers := rulestest.Edited(t, func(s string) string { return strings.Replace(s, "[viewer, editor, admin]", "[viewer]", 1) })
	file := []byte("access: {ops: editor}\n")
	if _, err := deploy.ProjectAccess(nil, file, "demo"); err != nil {
		t.Errorf("the standard set: %v", err)
	}
	if _, err := deploy.ProjectAccess(viewers, file, "demo"); err == nil {
		t.Error("a role the caller's set does not hold is given")
	}
	if got, err := deploy.ProjectAccess(viewers, nil, "demo"); err != nil || len(got) != 0 {
		t.Errorf("no access, a set without the editor: %v, %v", got, err)
	}
}
