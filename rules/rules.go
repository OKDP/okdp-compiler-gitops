// Package rules gives okdp-gitops' compiles their rule set: the
// compiler's mandatory one with the security set of okdp-compiler-security
// added (security.Rules), which together hold what a compile refuses of a
// platform. A host gives it to its compiles (compiler.BuildOptions.Rules);
// deploy.Build does.
package rules

import (
	compiler "github.com/okdp/okdp-compiler"
	security "github.com/okdp/okdp-compiler-security"
)

// Or: r, or the standard rule set when r is nil: what a host that lets its
// caller choose a rule set reads.
func Or(r *compiler.RuleSet) *compiler.RuleSet {
	if r != nil {
		return r
	}
	return Standard()
}

// Standard: the rule set of an OKDP platform, the compiler's mandatory one
// and the security set, read once and shared. It panics when the security
// set does not load: its files are embedded in the module, and its tests
// and this package's load them, so that is a broken build, as for
// compiler.Mandatory.
func Standard() *compiler.RuleSet {
	r, err := security.Rules()
	if err != nil {
		panic(err)
	}
	return r
}
