// Package rules gives okdp-gitops' compiles their rule set: the
// compiler's mandatory one with the security set of okdp-compiler-security
// and the set of okdp-gitops added, in that order, which together hold
// what a compile refuses of a platform. The set of okdp-gitops is the
// directory set, embedded (FS): the namespaces of the deployer engines and
// the names of the objects the targets write in every namespace, which no
// project may take, and the platform's settings the targets read. A host
// gives the standard set to its compiles (tree.BuildOptions.Rules);
// deploy.Build does.
package rules

import (
	"embed"
	"io/fs"
	"sync"

	compiler "github.com/okdp/okdp-compiler"
	security "github.com/okdp/okdp-compiler-security"
)

// TableEngineNamespaces: the table of the set that lists the namespaces of
// the deployer engines and of the objects the targets write in them, which
// no project may have.
const TableEngineNamespaces = "engineNamespaces"

//go:embed set
var files embed.FS

// FS: the set of okdp-gitops, rules.yaml at the root, read after the
// security set (Standard): it adds to the security set's access and
// policy settings.
func FS() fs.FS {
	sub, err := fs.Sub(files, "set")
	if err != nil {
		panic(err)
	}
	return sub
}

// Or: r, or the standard rule set when r is nil: what a host that lets its
// caller choose a rule set reads.
func Or(r *compiler.RuleSet) *compiler.RuleSet {
	if r != nil {
		return r
	}
	return Standard()
}

// standard: the standard set, read once.
var standard = sync.OnceValues(func() (*compiler.RuleSet, error) { return compiler.LoadRules(security.FS(), FS()) })

// Standard: the rule set of an OKDP platform, the compiler's mandatory one,
// the security set and the set of okdp-gitops, read once and shared. It
// panics when they do not load: their files are embedded in the modules,
// and their tests and this package's load them, so that is a broken
// build, as for compiler.Mandatory.
func Standard() *compiler.RuleSet {
	r, err := standard()
	if err != nil {
		panic(err)
	}
	return r
}

// EngineNamespaces: the namespaces r reserves to the deployer engines and
// to the objects the targets write in them (TableEngineNamespaces), none
// when r is a set without the set of okdp-gitops.
func EngineNamespaces(r *compiler.RuleSet) []string { return Or(r).List(TableEngineNamespaces) }
