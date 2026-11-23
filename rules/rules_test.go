package rules

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	security "github.com/okdp/okdp-compiler-security"
	"github.com/okdp/okdp-compiler/value"
)

// M is a values map.
type M = map[string]any

// TestStandard: the standard set is the mandatory one, the security set
// and this module's, in that order, read once: its tables and its hooks,
// after the sets before it.
func TestStandard(t *testing.T) {
	r := Standard()
	if got := r.Names(); !slices.Equal(got, []string{"core", "security", "gitops"}) {
		t.Errorf("the sets: %v", got)
	}
	if v, ok := r.SetVersion("gitops"); !ok || v != "1.0.0" {
		t.Errorf("the version: %q %v", v, ok)
	}
	if Or(nil) != r || Standard() != r {
		t.Error("Or(nil) is not the standard set, or it is read again")
	}
	if got := EngineNamespaces(nil); !slices.Equal(got, []string{"argocd", "flux-system", "okdp-releases"}) {
		t.Errorf("the engines' namespaces: %v", got)
	}
	if got := r.Hooks("namespace"); !slices.Equal(got, []string{"namespace", "reservedNamespace", "engineNamespace"}) {
		t.Errorf("the namespace hook: %v", got)
	}
	if got := r.Hooks("release"); !slices.Equal(got, []string{"platformObjectRelease"}) {
		t.Errorf("the release hook: %v", got)
	}
	for table, v := range map[string]string{
		"platformObjectNames":    "okdp-dcr-token",
		"platformObjectSettings": "oidc.dcr.tokenSecret",
	} {
		if !slices.Contains(r.List(table), v) {
			t.Errorf("%s: no %s in %v", table, v, r.List(table))
		}
	}
	// The sets below have none of it.
	sec := securityRules(t)
	if sec.List(TableEngineNamespaces) != nil || len(sec.Hooks("release")) != 0 || EngineNamespaces(compiler.Mandatory()) != nil {
		t.Error("the security or the mandatory set gives the engines' namespaces or a release hook")
	}
}

// securityRules: the mandatory set with the security set alone.
func securityRules(t *testing.T) *compiler.RuleSet {
	t.Helper()
	r, err := security.Rules()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestEngineNamespaces: a namespace of a deployer engine, or one its
// target writes in, is no project's with the standard set, privileged or
// not; the mandatory set alone accepts it, the security set too. The sets
// before it still apply: the mandatory set's names, then the platform's
// reservations.
func TestEngineNamespaces(t *testing.T) {
	ctx := context.Background()
	privileged := M{"global": M{"okdp": M{"policy": M{"privileged": []any{"argocd", "flux-system", "okdp-releases"}}}}}
	for _, ns := range []string{"argocd", "flux-system", "okdp-releases"} {
		err := compiler.CheckNamespace(ctx, Standard(), ns, privileged)
		if want := `namespace "` + ns + `": the namespace is reserved: a deployer engine's, or the namespace its target writes in (argocd, flux-system, okdp-releases): a project's releases there would drive the engine`; err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %s", ns, err, want)
		}
		for name, r := range map[string]*compiler.RuleSet{"mandatory": compiler.Mandatory(), "security": securityRules(t)} {
			if err := compiler.CheckNamespace(ctx, r, ns, nil); err != nil {
				t.Errorf("%s, the %s set: %v", ns, name, err)
			}
		}
	}
	reserved := M{"global": M{"okdp": M{"policy": M{"reserved": []any{"openbao"}}}}}
	for ns, want := range map[string]string{
		"kube-system": "kube-* is Kubernetes' own",
		"platform":    "platform/ holds the platform's keys",
		"openbao":     "reserved by the platform (global.okdp.policy.reserved)",
		"demo":        "",
	} {
		err := compiler.CheckNamespace(ctx, Standard(), ns, reserved)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: got %v, want %q", ns, err, want)
		}
	}
}

// TestReleaseNamedLikeAPlatformObject: a release that is the name of an
// object the platform writes in every namespace (the Role and the
// NetworkPolicy okdp-project, okdp-control-plane, the RoleBindings
// okdp-access-*, the secret store's ServiceAccount okdp-secrets, the
// Secret of the security set's initial access token okdp-dcr-token), or
// the prefix, up to a "-", of one, is refused, a privileged project's
// too: the objects of a package are named after the release, <release>,
// <release>-x, and would take the platform's place in the namespace (the
// project okdp's instance project, release okdp-project). So are the
// platform's own names for them (secrets.store, secrets.serviceAccount,
// oidc.dcr.tokenSecret). The mandatory set alone refuses none.
func TestReleaseNamedLikeAPlatformObject(t *testing.T) {
	ctx := context.Background()
	named := M{"secrets": M{"store": "vault-store", "serviceAccount": "vault-sa"}, "oidc": M{"dcr": M{"tokenSecret": "dcr-creds"}}}
	for _, tc := range []struct {
		release  string
		settings M
		want     string // the error, "" for none
	}{
		{"okdp-project", nil, `Inputs.Release "okdp-project": the name of an object the platform writes in every namespace (okdp-project), or its beginning up to a "-": the objects of a release are named <release> and <release>-<x>, and would take its place in the namespace`},
		{"okdp-control-plane", nil, "(okdp-control-plane)"},
		{"okdp-access-editor", nil, "(okdp-access-editor)"},
		{"okdp-secrets", nil, "(okdp-secrets)"},
		{"okdp-dcr-token", nil, "(okdp-dcr-token)"},
		// A prefix up to a "-": <release>-admin is the platform's.
		{"okdp-access", nil, "(okdp-access-admin)"},
		{"okdp-control", nil, "(okdp-control-plane)"},
		{"okdp-dcr", nil, "(okdp-dcr-token)"},
		{"okdp", nil, "(okdp-access-admin)"},
		// The platform's own names for them.
		{"vault-store", named, "(vault-store)"},
		{"vault-sa", named, "(vault-sa)"},
		{"vault", named, "(vault-sa)"},
		{"dcr-creds", named, "(dcr-creds)"},
		// Not one.
		{"demo-app", nil, ""},
		{"okdp-system-app", nil, ""},
		{"okdp-projects", nil, ""},
		{"okdp-secrets-x", nil, ""},
		{"okdp-dcr-token-x", nil, ""},
		{"vault-store", nil, ""},
		{"dcr-creds", nil, ""},
	} {
		t.Run(tc.release, func(t *testing.T) {
			privileged := maps.Clone(tc.settings)
			if privileged == nil {
				privileged = M{}
			}
			privileged["policy"] = M{"privileged": []any{"demo"}}
			for _, settings := range []M{tc.settings, privileged} {
				err := Standard().RunHook(ctx, "release", M{"field": "Inputs.Release", "release": tc.release, "settings": settings})
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Errorf("settings %v: got %v, want %q", settings, err, tc.want)
				}
			}
			if err := compiler.Mandatory().RunHook(ctx, "release", M{"field": "Inputs.Release", "release": tc.release, "settings": tc.settings}); err != nil {
				t.Errorf("the mandatory set: %v", err)
			}
		})
	}
}

// TestSchema: the platform's settings the set adds, those the targets
// read: network and controlPlane closed maps, access.groupPrefix and
// policy.projectApiGroups beside the security set's keys of access and
// policy, which stay closed. The security set alone refuses them where it
// closes the map; the mandatory set alone reads none of them.
func TestSchema(t *testing.T) {
	settings := func(r *compiler.RuleSet, okdp M) (M, error) {
		return compiler.SettingsOf(context.Background(), compiler.Inputs{Rules: r, Namespace: "demo", Values: []map[string]any{{"global": M{"okdp": okdp}}}})
	}
	given := M{
		"network":      M{"policy": true, "ingressNamespaces": []any{"ingress-nginx"}},
		"controlPlane": M{"serviceAccount": M{"namespace": "okdp-system", "name": "okdp-control-plane-server"}},
		"access":       M{"groupPrefix": "oidc:", "groups": M{"devs": "editor"}, "admins": []any{"ops"}},
		"policy":       M{"projectApiGroups": []any{"keda.sh"}, "privileged": []any{"cert-manager"}},
	}
	got, err := settings(Standard(), given)
	if err != nil || value.Dig(got, "access.groupPrefix") != "oidc:" || value.Dig(got, "network.policy") != true {
		t.Errorf("the settings the set adds: %v %v", err, got)
	}
	for name, tc := range map[string]struct {
		okdp M
		want string
	}{
		"a network key":          {M{"network": M{"policies": true}}, "additional properties 'policies' not allowed"},
		"an ingress namespace":   {M{"network": M{"ingressNamespaces": []any{"Nginx"}}}, "/global/okdp/network/ingressNamespaces/0"},
		"a policy of a string":   {M{"network": M{"policy": "true"}}, "/global/okdp/network/policy"},
		"no ServiceAccount name": {M{"controlPlane": M{"serviceAccount": M{"namespace": "okdp-system"}}}, "missing property 'name'"},
		"a control plane key":    {M{"controlPlane": M{"token": "x"}}, "additional properties 'token' not allowed"},
		"a group prefix":         {M{"access": M{"groupPrefix": "a b"}}, "/global/okdp/access/groupPrefix"},
		"policy misspelt": {M{"policy": M{"projectApiGroup": []any{"x"}, "projectApiGroups": "x"}},
			"- at '/global/okdp/policy': additional properties 'projectApiGroup' not allowed\n- at '/global/okdp/policy/projectApiGroups': got string, want array"},
		"another key of access": {M{"access": M{"prefix": "x"}}, "additional properties 'prefix' not allowed"},
		"another key of policy": {M{"policy": M{"hosts": M{}}}, "additional properties 'hosts' not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := settings(Standard(), tc.okdp); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
	for name, okdp := range map[string]M{
		"access.groupPrefix":      {"access": M{"groupPrefix": "oidc:"}},
		"policy.projectApiGroups": {"policy": M{"projectApiGroups": []any{"keda.sh"}}},
	} {
		if _, err := settings(securityRules(t), okdp); err == nil {
			t.Errorf("%s: the security set alone accepts it", name)
		}
	}
	if _, err := settings(compiler.Mandatory(), given); err != nil {
		t.Errorf("the mandatory set reads none of them: %v", err)
	}
}
