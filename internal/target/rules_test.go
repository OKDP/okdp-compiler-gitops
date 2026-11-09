package target_test

import (
	"errors"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/internal/rulestest"
	"github.com/okdp/okdp-gitops/internal/target"
	"github.com/okdp/okdp-gitops/internal/target/argocd"
)

// TestProjectRoles: the manifests of a project hold what the rule set of its
// compile accepts, not the standard set's.
func TestProjectRoles(t *testing.T) {
	viewers := rulestest.Edited(t, func(s string) string { return strings.Replace(s, "[viewer, editor, admin]", "[viewer]", 1) })
	in := func(role string) compiler.Inputs {
		return compiler.Inputs{Rules: viewers, Namespace: "demo", Context: []compiler.ContextLayer{{Level: 1, Values: map[string]any{"access": map[string]any{"ops": role}}}}}
	}
	p, err := target.ProjectOf(in("viewer"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(target.Access(p)); !strings.Contains(got, "name: okdp-access-viewer\n") {
		t.Errorf("Access:\n%s", got)
	}
	_, err = target.ProjectOf(in("editor"))
	if accessErr := new(target.AccessError); !errors.As(err, &accessErr) {
		t.Errorf("a role the set does not hold: %v", err)
	}
}

// TestUnknownRole: a role no target knows gives no admin rights.
func TestUnknownRole(t *testing.T) {
	for name, f := range map[string]func(){
		"ClusterRole": func() { target.ClusterRole("owner") },
		"Policies":    func() { argocd.Policies("owner") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			f()
		})
	}
}
