// Package rules holds the standard rule set of an OKDP platform: what a
// compile refuses of a platform, as data read by the compiler
// (compiler.LoadRules) and added to its mandatory set, both then applying.
// A host gives it to its compiles (compiler.BuildOptions.Rules);
// deploy.Build does.
package rules

import (
	"embed"
	"io/fs"
	"sync"

	compiler "github.com/okdp/okdp-compiler"
)

//go:embed v1
var files embed.FS

// Or: r, or the standard rule set when r is nil: what a host that lets its
// caller choose a rule set reads.
func Or(r *compiler.RuleSet) *compiler.RuleSet {
	if r != nil {
		return r
	}
	return Standard()
}

// Standard: the standard rule set, read once.
func Standard() *compiler.RuleSet { return standard() }

var standard = sync.OnceValue(func() *compiler.RuleSet {
	sub, err := fs.Sub(files, "v1")
	if err != nil {
		panic(err)
	}
	r, err := compiler.LoadRules(sub)
	if err != nil {
		panic(err)
	}
	return r
})
