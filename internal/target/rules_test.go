package target_test

import (
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/internal/rulestest"
	"github.com/okdp/okdp-gitops/internal/target"
	"github.com/okdp/okdp-gitops/internal/target/argocd"
	"github.com/okdp/okdp-gitops/tree"
)

// TestProjectRoles: the manifests of a project hold the roles of its
// groups (tree.Groups, Inputs.Settings), whatever rule set it compiles
// with.
func TestProjectRoles(t *testing.T) {
	in := compiler.Inputs{Rules: rulestest.Added(t), Namespace: "demo", Settings: tree.GroupsSettings(map[string]compiler.AccessRole{"ops": compiler.AccessViewer})}
	p, err := target.ProjectOf(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(target.Access(p)); !strings.Contains(got, "name: okdp-access-viewer\n") {
		t.Errorf("Access:\n%s", got)
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
