// Build: a tree loaded and compiled in one call, and the deployment graph
// of compiled instances.

package tree

import (
	"context"
	"slices"
	"time"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler-security/policy"

	"github.com/okdp/okdp-gitops/internal/graph"
)

type (
	// Graph is the deployment graph of compiled instances (NewGraph).
	Graph = graph.Graph
	// GraphNode is a release of a Graph, by namespace.
	GraphNode = graph.Node
	// GraphError is a problem of a Graph at one release.
	GraphError = graph.Error
)

// NewGraph builds the deployment graph of results, which are the compiled
// instances of their namespaces. A release waits for the releases its
// connection references point to (Result.Dependencies). NewGraph reports
// every problem at once, each a *GraphError: a dependency on a release
// that is missing or does not provide what is referenced, a cycle, a
// release given twice.
func NewGraph(results []*compiler.Result) (*Graph, error) { return graph.New(results) }

// BuildOptions is what Build sets on each instance of a tree.
type BuildOptions struct {
	CostLimit uint64 // Inputs.CostLimit of each instance; 0 sets no limit
	// InstanceTimeout is Inputs.Timeout of each instance; 0 sets no
	// limit. Past it, that instance is refused and the others are still
	// compiled. The deadline of ctx bounds the whole tree beside it.
	InstanceTimeout   time.Duration
	AllowPlainSecrets bool // Inputs.AllowPlainSecrets of each instance
	// Rules is Inputs.Rules of each instance. Nil applies the mandatory
	// rules alone.
	Rules    *compiler.RuleSet
	Finalize compiler.Finalize // Inputs.Finalize; nil is Helm's merge
	// CheckGlobals is the host's policy on the global of the layers the
	// platform does not review: a project's file, an instance's values
	// file (policy.CheckGlobals), called as the tree loads. What it
	// refuses is a problem of the project's (Tree.Refused). Nil checks
	// nothing.
	CheckGlobals func(context.Context, policy.GlobalsInput) error
	// Loaded, when set, is given the instances of the projects that load,
	// before they compile, with ctx: what a Finalize reads that the
	// compiler does not (the upstream charts of their Chart.yaml, fetched
	// there so that no fetch counts against InstanceTimeout).
	// It does not change them.
	Loaded func(context.Context, []compiler.Instance)
}

// Build loads the tree that repos make together (as Load reads it), sets
// opts on each instance and compiles the tree (Compile). ctx cancels it.
//
// A problem of the platform's parts (a repository, platform.yaml,
// catalogs.yaml, a catalog) fails the build with a *LoadError that lists
// every problem. A project that does not load is refused as a whole, but
// the tree is not: the project is listed in the tree's Refused and the
// other projects compile. A refused instance is not an error either: it
// is the tree's answer (Instance.Err).
func Build(ctx context.Context, repos []Repository, opts BuildOptions) (*Tree, error) {
	loaded, err := loadProjects(ctx, repos, opts.Rules, opts.CheckGlobals)
	if err != nil {
		return nil, err
	}
	instances := loaded.Instances
	for i := range instances {
		instances[i].Inputs.CostLimit = opts.CostLimit
		instances[i].Inputs.Timeout = opts.InstanceTimeout
		instances[i].Inputs.AllowPlainSecrets = opts.AllowPlainSecrets
		instances[i].Inputs.Finalize = opts.Finalize
		instances[i].Inputs.Rules = opts.Rules
	}
	if opts.Loaded != nil {
		opts.Loaded(ctx, slices.Clone(instances))
	}
	t := Compile(ctx, instances)
	for _, r := range loaded.Refused {
		r.Inputs.Rules = opts.Rules // compiler.SettingsOf reads them for a project that does not load
		t.Refused = append(t.Refused, r)
	}
	return t, nil
}

// LoadErrorOf returns why the projects of t that do not load
// (Tree.Refused) do not, as the *LoadError that the whole tree would give
// with them. It is nil when every project loads.
func LoadErrorOf(t *Tree) *LoadError {
	if t == nil {
		return nil
	}
	var errs []error
	for _, r := range t.Refused {
		errs = append(errs, r.Errs...)
	}
	return joinErrors(errs)
}
