package target

import (
	"context"
	"slices"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/value"
)

// ProjectAccess: the roles of a project's groups, by group name, and the prefix
// the Kubernetes API server gives the groups of its users (its
// --oidc-groups-prefix, global.okdp.access.groupPrefix): a RoleBinding's
// subject is the prefix and the name, an Argo CD role's group the name.
type ProjectAccess struct {
	Groups      map[string]compiler.AccessRole
	GroupPrefix string
}

// ServiceAccount: a ServiceAccount, by namespace and name.
type ServiceAccount struct {
	Namespace, Name string
}

// Network: the network policy of a project's namespace, global.okdp.network
// with policy true: every ingress to its pods denied but from the namespace
// itself, from Ingress and from Claimers.
type Network struct {
	Ingress  []string // global.okdp.network.ingressNamespaces, in their order
	Claimers []string // the namespaces of the claims the project's instances provide, sorted, once each, its own left out
}

// Project: what the targets write a project's manifests from, the
// platform's and the project's decisions, not its instances'.
type Project struct {
	Namespace    string
	Privileged   bool
	SecretStore  *compiler.SecretStore
	Access       ProjectAccess
	ControlPlane *ServiceAccount
	Network      *Network
	// APIGroups: global.okdp.policy.projectApiGroups, what the platform
	// adds to the API groups of the Role its releases are installed with
	// (flux.ServiceAccount, the rule set's document deployerRole).
	APIGroups []string
}

// AccessError: a project's access the compile refuses: ProjectOf gives the
// rest of the project all the same, no group a role.
type AccessError struct{ Err error }

func (e *AccessError) Error() string { return e.Err.Error() }
func (e *AccessError) Unwrap() error { return e.Err }

// controlPlaneOf: the control plane's identity of the platform's settings;
// nil when not set, and the targets bind none.
func controlPlaneOf(okdp map[string]any) *ServiceAccount {
	sa := value.AsMap(value.Dig(okdp, "controlPlane.serviceAccount"))
	if len(sa) == 0 {
		return nil
	}
	return &ServiceAccount{Namespace: value.Str(sa["namespace"]), Name: value.Str(sa["name"])}
}

// networkOf: the Network of the platform's settings, without claimers; nil
// unless global.okdp.network.policy is true.
func networkOf(okdp map[string]any) *Network {
	if value.Dig(okdp, "network.policy") != true {
		return nil
	}
	n := &Network{}
	for _, ns := range value.AsList(value.Dig(okdp, "network.ingressNamespaces")) {
		n.Ingress = append(n.Ingress, value.Str(ns))
	}
	return n
}

// apiGroups: global.okdp.policy.projectApiGroups, none when unset.
func apiGroups(okdp map[string]any) []string {
	var out []string
	for _, g := range value.AsList(value.Dig(okdp, "policy.projectApiGroups")) {
		out = append(out, value.Str(g))
	}
	return out
}

// groupPrefix: global.okdp.access.groupPrefix, "" when unset.
func groupPrefix(okdp map[string]any) string {
	return value.Str(value.Dig(okdp, "access.groupPrefix"))
}

// claimers: the namespaces of claims, sorted, once each, namespace's left
// out: those that reach a provider of namespace (Network.Claimers).
func claimers(namespace string, claims []compiler.Claim) []string {
	var out []string
	for _, c := range claims {
		if c.Namespace != namespace && !slices.Contains(out, c.Namespace) {
			out = append(out, c.Namespace)
		}
	}
	slices.Sort(out)
	return out
}

// ProjectOf: the Project of the namespace of in, of its context alone, as
// the compile of any of its instances gives it: today's settings of the
// platform and of the project, whether its instances compile or not, for
// the manifests of a project whose instances are kept. An access that is
// refused is an *AccessError, the Project given all the same, no group a
// role in it. Its network policy has the claimers of in.Claims alone: the
// caller adds those of the project's other providers.
func ProjectOf(in compiler.Inputs) (Project, error) {
	settings, err := compiler.PlatformOf(context.Background(), in)
	if err != nil {
		return Project{}, err
	}
	store, err := compiler.SecretStoreOf(context.Background(), in, settings)
	if err != nil {
		return Project{}, err
	}
	p := Project{Namespace: in.Namespace, Privileged: compiler.Privileged(settings, in.Namespace), SecretStore: store, Access: ProjectAccess{GroupPrefix: groupPrefix(settings)}, ControlPlane: controlPlaneOf(settings), Network: networkOf(settings), APIGroups: apiGroups(settings)}
	if p.Network != nil {
		p.Network.Claimers = claimers(in.Namespace, in.Claims)
	}
	groups, err := compiler.ProjectGroups(in)
	if err != nil {
		p.Access.Groups = map[string]compiler.AccessRole{}
		return p, &AccessError{err}
	}
	p.Access.Groups = groups
	return p, nil
}
