package main

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/okdp/okdp-gitops/chartcache"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/gitops"
)

// opened is a repository of the topology, loaded in memory.
type opened struct {
	loc    gitops.Location
	store  *gitops.MemoryStore
	before map[string]string
	// dir: a local directory, written in place; else git, pushed to.
	dir string
	git *gitops.GitStore
	// base is the revision of the git repository the compile read.
	base string
}

// beforeWrite runs between the compile and the writes, for the tests.
var beforeWrite = func() {}

// compilation is one okdp-gitops compile.
type compilation struct {
	topology *gitops.Topology
	prefix   string
	catalogs map[string]string
	// creds are the credentials of the repositories and of the catalogs.
	creds *gitops.Credentials
	// charts, when set, checks the values against the upstream charts.
	charts *chartcache.Cache
	check  bool
}

// run reads every repository of the topology, compiles the tree and
// writes, or checks, the changes of each. A git repository that moved since
// it was read fails the write with an error wrapping gitops.ErrMoved.
func (c compilation) run(ctx context.Context) (int, error) {
	// The clones live on disk, in a directory of the run's own: the packs
	// stay compressed, a file of the repositories is never held inflated
	// in memory but for the layout's, within its bounds.
	clones, err := os.MkdirTemp("", "okdp-gitops-clones-")
	if err != nil {
		return 1, err
	}
	defer func() { _ = os.RemoveAll(clones) }()
	var repos []*opened
	// projects: the ProjectsFile of a split topology, read with the
	// platform's repository, which the compile opens first (CatalogsFile).
	var projects *treeedit.ProjectsList
	open := func(loc gitops.Location) (gitops.Store, error) {
		o := &opened{loc: loc, before: map[string]string{}}
		links := map[string]bool{}
		// read reads the paths of the layout in the repository; in a split
		// topology, the platform's ProjectsFile first, which places the
		// projects (one an entry puts in the platform's repository is
		// read, then refused by the routing, Repositories): reread then
		// brings the paths of the layout it now knows (a Git repository
		// reads them again from its clone, at the revision it has read).
		read := func(load func([]string) error, reread func() error) error {
			repo := loc.Repository()
			if c.topology.Split() && projects == nil && repo == c.topology.Platform.Repository() {
				if err := load(c.topology.LayoutPaths(repo, nil)); err != nil {
					return err
				}
				projects = projectsOf(o.before, path.Join(".", c.topology.Platform.Path, treeedit.ProjectsFile))
				if err := reread(); err != nil {
					return err
				}
			}
			return load(c.topology.LayoutPaths(repo, projects))
		}
		if gitops.IsLocal(loc.URL) {
			o.dir = loc.URL
			dir := gitops.NewDirReader(o.dir)
			err := read(func(paths []string) error { return readPaths(dir, paths, o.before, links) }, func() error { return nil })
			if err != nil {
				return nil, err
			}
		} else {
			auth, err := c.creds.For(loc.URL)
			if err != nil {
				return nil, err
			}
			if o.git, err = gitops.NewGitStore(gitops.GitOptions{
				URL: loc.URL, Branch: loc.Branch, Auth: auth, Depth: 1,
				CloneDir: filepath.Join(clones, strconv.Itoa(len(repos))),
				Layout:   func() []string { return c.topology.LayoutPaths(loc.Repository(), projects) },
			}); err != nil {
				return nil, err
			}
			snap, err := o.git.Snapshot(ctx)
			if err != nil {
				return nil, err
			}
			err = read(func(paths []string) error { return readPaths(snap, paths, o.before, links) }, func() error {
				var err error
				snap, err = o.git.Reread()
				return err
			})
			if err != nil {
				return nil, err
			}
			o.base = snap.Revision()
		}
		var err error
		if o.store, err = gitops.NewMemoryStore(o.before); err != nil {
			return nil, fmt.Errorf("repository %s: %w", loc.URL, err)
		}
		repos = append(repos, o)
		return &linked{MemoryStore: o.store, name: loc.URL, links: links}, nil
	}

	repositories := gitops.NewRepositories(c.topology, open)
	if c.upToDate(ctx, repositories, func() []*opened { return repos }) {
		fmt.Printf("okdp-gitops: up to date: the sources did not move since the compile %s records\n", gitops.SourcesFile)
		return 0, nil
	}
	d := gitops.NewDeployments(repositories)
	d.OutputPrefix = c.prefix
	d.Compiler = gitops.NewCompiler(&catalog.Loader{
		Dirs:     c.catalogs,
		FileURLs: gitops.IsLocalRepository(c.topology.Platform.URL),
		Auth:     c.creds.Catalogs(c.topology.Platform.URL),
	})
	if c.charts != nil {
		d.Compiler.Charts = c.charts
	}
	var cats []readCatalog
	d.Compiler.OnCatalogs = func(list []treeedit.CatalogRepository, read []catalog.Catalog) {
		cats = cats[:0]
		for i := range min(len(list), len(read)) {
			cats = append(cats, readCatalog{repo: list[i], cat: read[i]})
		}
	}

	var kept []tree.Kept
	_, err = d.Store.Update(ctx, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error {
		var err error
		kept, err = d.WriteCompiled(ctx, tx)
		return err
	})
	if err != nil {
		return 1, err
	}
	if !c.check {
		if err := c.writeRecord(ctx, repos, cats, len(kept)); err != nil {
			return 1, err
		}
	}

	name := func(o *opened, p string) string {
		if o.dir != "" {
			return filepath.Join(o.dir, filepath.FromSlash(p))
		}
		return o.loc.URL + ": " + p
	}
	if !c.check {
		beforeWrite()
	}
	stale := 0
	for _, o := range repos {
		changed, removed := diff(o.before, o.store.Files())
		if c.check {
			for _, p := range changed {
				fmt.Printf("stale %s\n", name(o, p))
			}
			for _, p := range removed {
				fmt.Printf("orphan %s\n", name(o, p))
			}
			stale += len(changed) + len(removed)
			continue
		}
		if err := o.write(ctx, changed, removed, func(p string) string { return name(o, p) }); err != nil {
			return 1, err
		}
	}
	if len(kept) > 0 {
		reportKept(kept, func(project string) (revision, bool) { return c.keptSince(repos, projects, project) })
	}
	if c.check {
		if stale > 0 {
			fmt.Fprintln(os.Stderr, "okdp-gitops: the compiled files are not up to date: run okdp-gitops compile and commit the result")
			return 1, nil
		}
		if len(kept) > 0 {
			return 1, nil
		}
		fmt.Println("okdp-gitops: up to date")
	}
	if len(kept) > 0 {
		return 1, nil
	}
	return 0, nil
}

// reportKept prints, on stderr, the projects whose output is kept as it
// was, and why: every problem, one per line; then, a line each, the
// revision of its sources the kept output is the compile of (since, the
// output's record) and the count of its files written all the same, of
// today's settings (tree.Kept.Rederived).
func reportKept(kept []tree.Kept, since func(project string) (revision, bool)) {
	var names, problems []string
	for _, k := range kept {
		names = append(names, k.Namespace)
		problems = append(problems, k.Problems...)
	}
	fmt.Fprintf(os.Stderr, "okdp-gitops: %d problem(s), the output of %s kept as it was:\n%s\n", len(problems), strings.Join(names, ", "), strings.Join(problems, "\n"))
	for _, k := range kept {
		when := "kept, no revision recorded in " + gitops.SourcesFile
		if r, ok := since(k.Namespace); ok {
			when = fmt.Sprintf("kept since %s of %s#%s (%s)", r.Revision, r.URL, r.Branch, gitops.SourcesFile)
		}
		fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %s, %d file(s) re-derived\n", k.Namespace, when, len(k.Rederived))
	}
}

// write writes the changes of a repository: in place for a local directory,
// as one commit pushed for a git url, on the revision the compile read.
func (o *opened) write(ctx context.Context, changed, removed []string, name func(string) string) error {
	if len(changed)+len(removed) == 0 {
		return nil
	}
	after := o.store.Files()
	if o.git != nil {
		_, err := o.git.UpdateAt(ctx, o.base, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error {
			for _, p := range changed {
				if err := tx.WriteFile(p, []byte(after[p])); err != nil {
					return err
				}
			}
			for _, p := range removed {
				if err := tx.Remove(p); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, p := range changed {
		if o.dir != "" {
			file := filepath.Join(o.dir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(after[p]), 0o644); err != nil {
				return err
			}
		}
		fmt.Printf("compiled %s\n", name(p))
	}
	for _, p := range removed {
		if o.dir != "" {
			file := filepath.Join(o.dir, filepath.FromSlash(p))
			if err := os.Remove(file); err != nil {
				return err
			}
			removeEmptyParents(o.dir, filepath.Dir(file))
		}
		fmt.Printf("removed %s\n", name(p))
	}
	return nil
}

// diff returns the files written or changed, and the files removed, sorted.
func diff(before, after map[string]string) (changed, removed []string) {
	for p, content := range after {
		if old, ok := before[p]; !ok || old != content {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return changed, removed
}

// removeEmptyParents removes dir and its parents up to root while empty.
func removeEmptyParents(root, dir string) {
	root = filepath.Clean(root)
	for dir != root && strings.HasPrefix(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
