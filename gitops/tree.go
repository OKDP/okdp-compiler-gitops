package gitops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/okdp/okdp-gitops/deploy"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/internal/mapfs"
)

// DefaultInstanceTimeout bounds the compile of one instance (the CI compile
// and the control plane's): past it the instance is refused and the others
// compiled, a package's expressions or a chart's render bounded per
// instance, not only by the run's --timeout. okdp-compile's --instance-timeout
// default.
const DefaultInstanceTimeout = time.Minute

// DefaultCostLimit bounds the CEL cost of the compile of one instance
// (compiler.Inputs.CostLimit): the server compiles packages it does not
// review, on values an instance gives. It is okdp-compile's default
// (compiler.DefaultCostLimit).
const DefaultCostLimit = compiler.DefaultCostLimit

// Compiler compiles the tree a treeedit.Reader holds with okdp-compiler: its parts
// and the catalogs of treeedit.CatalogsFile, given to deploy.Build. Safe for
// concurrent use.
type Compiler struct {
	// Catalogs fetches the catalog repositories of treeedit.CatalogsFile.
	Catalogs *catalog.Loader
	// Charts, when set, checks the final values against the upstream
	// charts it fetches: their schemas, the lists an instance appends to
	// (deploy.BuildOptions.Charts).
	Charts deploy.ChartSource
	// SkipRender leaves out the render of the charts a project gives
	// upstream values to (deploy.BuildOptions.SkipRender), for a long-running
	// process: a render past its timeout keeps its goroutine, Helm's engine
	// taking no context. False, the default, renders: the CI's compile,
	// the boundary, must.
	SkipRender bool
	// CostLimit bounds the CEL cost of each instance's compile, none when 0.
	CostLimit uint64
	// InstanceTimeout bounds each instance's compile, none when 0.
	InstanceTimeout time.Duration
	// Rules is the rule set of the compiles and of the checks of the edits;
	// nil, the standard one.
	Rules *compiler.RuleSet
	// OnCatalogs, when set, is told the catalogs each Compile reads: the
	// repositories of treeedit.CatalogsFile and what was read of each, in the same
	// order (okdp-gitops compile records their revisions).
	OnCatalogs func([]treeedit.CatalogRepository, []catalog.Catalog)

	mu       sync.Mutex
	memo     []memoEntry
	inflight map[string]*flight
	// build is deploy.Build, a test's slow one; waiting, when set, is
	// told a Compile waits on another's.
	build   func(context.Context, []compiler.Repository, deploy.BuildOptions) (*compiler.Tree, error)
	waiting func()
}

// flight is a compile under way, which the compiles of the same inputs
// wait on.
type flight struct {
	done chan struct{}
	tree *compiler.Tree
	err  error
}

// NewCompiler compiles with the catalogs loader fetches, DefaultCostLimit
// and DefaultInstanceTimeout per instance.
func NewCompiler(loader *catalog.Loader) *Compiler {
	return &Compiler{Catalogs: loader, CostLimit: DefaultCostLimit, InstanceTimeout: DefaultInstanceTimeout}
}

// memoSize bounds the compiles kept: a write compiles the tree before and
// after its change, a read the tree as it is.
const memoSize = 8

type memoEntry struct {
	key  string
	tree *compiler.Tree
}

// Compile compiles the tree r holds: the catalogs of its
// treeedit.CatalogsFile fetched, its parts read (platform.yaml,
// catalogs.yaml, projects/), compiled together, with the graph of those
// that compile (deploy.Build, its values checked against the charts of
// Charts when set: a warning when one cannot be fetched, the values left
// unchecked). A tree that does not load is a *compiler.LoadError, a
// catalogs.yaml whose menu names a service of no catalog too; a catalog
// that cannot be read, another error. An instance refused is the tree's
// answer, not an error (compiler.TreeInstance.Err,
// compiler.Tree.Problems). The same sources and catalog revisions give
// the tree compiled last; compiles of them at once, one compile, which
// they share. The compile runs on a copy of the tree, no lock of the
// store held.
func (c *Compiler) Compile(ctx context.Context, r treeedit.Reader) (*compiler.Tree, error) {
	cats, err := c.OpenCatalogs(ctx, r)
	if err != nil {
		return nil, err
	}
	if c.OnCatalogs != nil {
		repos, _ := treeedit.ReadCatalogRepositories(r) // read by OpenCatalogs already
		c.OnCatalogs(repos, cats.List())
	}
	tree, err := treeedit.ReadSources(r)
	if err != nil {
		return nil, err
	}
	repos := append([]compiler.Repository{{Name: ".", FS: tree, Role: compiler.RoleTree}}, catalogRepositories(cats)...)
	key := memoKey(tree, cats)
	if key == "" {
		t, _, err := c.buildTree(ctx, repos)
		return t, err
	}
	for {
		c.mu.Lock()
		if t := c.recall(key); t != nil {
			c.mu.Unlock()
			return t, nil
		}
		if f, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			if c.waiting != nil {
				c.waiting()
			}
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if isContextErr(f.err) && ctx.Err() == nil {
				continue // its caller gave up on it: compile again
			}
			return f.tree, f.err
		}
		f := &flight{done: make(chan struct{})}
		if c.inflight == nil {
			c.inflight = map[string]*flight{}
		}
		c.inflight[key] = f
		c.mu.Unlock()

		c.fly(ctx, key, f, repos)
		return f.tree, f.err
	}
}

// fly runs the compile of flight f, whose inputs key, repos are: kept
// when it compiled, its waiters told even when it panics.
func (c *Compiler) fly(ctx context.Context, key string, f *flight, repos []compiler.Repository) {
	var unchecked bool
	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		// A tree compiled without the checks against an upstream chart
		// (one that could not be fetched, Charts.Failures) is not kept: the
		// next compile tries again, rather than serving it until the tree
		// changes.
		if f.err == nil && f.tree != nil && !unchecked {
			c.remember(key, f.tree)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.err = errors.New("the compile failed")
	f.tree, unchecked, f.err = c.buildTree(ctx, repos)
}

// buildTree is deploy.Build of the repositories of a compile. unchecked
// says that a chart could not be fetched: the instances that name it
// compiled without the checks against it.
func (c *Compiler) buildTree(ctx context.Context, repos []compiler.Repository) (t *compiler.Tree, unchecked bool, err error) {
	build := c.build
	if build == nil {
		build = deploy.Build
	}
	charts := deploy.NewCharts(c.Charts)
	opts := deploy.BuildOptions{
		BuildOptions: compiler.BuildOptions{CostLimit: c.CostLimit, InstanceTimeout: c.InstanceTimeout, Rules: c.Rules},
		Charts:       charts,
		SkipRender:   c.SkipRender,
	}
	if t, err = build(ctx, repos, opts); err != nil {
		return nil, false, err
	}
	failures := charts.Failures()
	for ref, err := range failures {
		slog.Warn("A chart could not be fetched: the instances naming it compiled without the checks against it", "error", err, "chart", ref.Repository+"/"+ref.Chart+":"+ref.Version)
	}
	for _, ti := range t.Instances {
		if ti.Outcome.Result == nil {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(ti.Outcome.Result.Charts)) {
			if failures[ti.Outcome.Result.Charts[key]] != nil {
				slog.Warn("Compiled without the checks against this chart: it could not be fetched", "instance", ti.Namespace()+"/"+ti.Name, "chart", key)
			}
		}
	}
	return t, len(failures) > 0, nil
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// OpenCatalogs opens the catalogs of the treeedit.CatalogsFile r holds, none when
// it has none.
func (c *Compiler) OpenCatalogs(ctx context.Context, r treeedit.Reader) (*catalog.Catalogs, error) {
	repos, err := treeedit.ReadCatalogRepositories(r)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return catalog.New(), nil
	}
	return c.Catalogs.Open(ctx, repos)
}

// CheckCatalogs refuses doc, a treeedit.CatalogsFile as decoded, as the
// compile of a tree that holds it does (treeedit.CheckCatalogs): read
// strictly, each service of its menu one of the catalogs of its
// repositories, fetched, its default version one of the service's. A
// refusal is a *treeedit.CompileError of the tree (no project, no
// instance), its problems compiler.FieldErrors'; a catalog that cannot be
// fetched, another error.
func (c *Compiler) CheckCatalogs(ctx context.Context, doc map[string]any) error {
	doc = treeedit.RoundTrip(doc)
	repos, err := treeedit.CatalogRepositories(doc)
	if err != nil {
		return &treeedit.CompileError{Err: fmt.Errorf("%s: %w", treeedit.CatalogsFile, err)}
	}
	cats := catalog.New()
	if len(repos) > 0 {
		if cats, err = c.Catalogs.Open(ctx, repos); err != nil {
			return err
		}
	}
	cat, err := compiler.LoadCatalog(catalogRepositories(cats))
	if err != nil {
		return &treeedit.CompileError{Err: err}
	}
	if err := treeedit.CheckCatalogs(doc, cat); err != nil {
		return &treeedit.CompileError{Err: err}
	}
	return nil
}

// catalogRepositories are the catalogs read, each a repository of the
// compile of role compiler.RoleCatalog.
func catalogRepositories(cats *catalog.Catalogs) []compiler.Repository {
	var repos []compiler.Repository
	for _, cat := range cats.List() {
		repos = append(repos, compiler.Repository{Name: cat.Name, FS: cat.FS, Role: compiler.RoleCatalog})
	}
	return repos
}

// Bounds of what the compiler is given of a tree, okdp-compiler's
// (treeedit.ReadSources): its files are kilobytes. A tree over them does
// not compile, the file named.
const (
	// MaxTreeFileSize is the size a file of the tree may have, in bytes.
	MaxTreeFileSize = treeedit.MaxTreeFileSize
	// MaxTreeSize is the size the files of a tree may have together, in
	// bytes.
	MaxTreeSize = treeedit.MaxTreeSize
)

// memoKey identifies the inputs of a compile: the files of the tree and
// the revision of each catalog; "" when a catalog has none (a local
// directory, which may change under the same key).
func memoKey(tree mapfs.FS, cats *catalog.Catalogs) string {
	h := sha256.New()
	for _, cat := range cats.List() {
		if cat.Revision == "" {
			return ""
		}
		_, _ = fmt.Fprintf(h, "catalog %q %s\n", cat.Name, cat.Revision)
	}
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		_, _ = fmt.Fprintf(h, "file %q %d\n", p, len(tree[p].Data))
		h.Write(tree[p].Data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// recall is the tree compiled of key, nil when none is kept; c.mu held.
func (c *Compiler) recall(key string) *compiler.Tree {
	for _, m := range c.memo {
		if m.key == key {
			return m.tree
		}
	}
	return nil
}

// remember keeps the tree compiled of key; c.mu held.
func (c *Compiler) remember(key string, tree *compiler.Tree) {
	c.memo = append(c.memo, memoEntry{key: key, tree: tree})
	if len(c.memo) > memoSize {
		c.memo = c.memo[len(c.memo)-memoSize:]
	}
}
