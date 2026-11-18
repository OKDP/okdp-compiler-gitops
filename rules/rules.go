// Package rules gives okdp-gitops' compiles their rule set: the
// compiler's mandatory one (compiler.Mandatory), which holds what a
// compile refuses of a platform. A host gives it to its compiles
// (compiler.BuildOptions.Rules); deploy.Build does.
package rules

import compiler "github.com/okdp/okdp-compiler"

// Or: r, or the standard rule set when r is nil: what a host that lets its
// caller choose a rule set reads.
func Or(r *compiler.RuleSet) *compiler.RuleSet {
	if r != nil {
		return r
	}
	return Standard()
}

// Standard: the rule set of an OKDP platform, the compiler's mandatory
// one.
func Standard() *compiler.RuleSet { return compiler.Mandatory() }
