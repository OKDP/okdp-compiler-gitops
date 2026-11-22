package gitops

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/okdp/okdp-gitops/treeedit"
)

// OutputDirs are the top entries of the output: what a compile writes,
// compiled/ and SourcesFile, and the Argo CD entry point it reads
// (ArgoCDRootPath).
var OutputDirs = []string{CompiledDir, path.Dir(ArgoCDRootPath), SourcesFile}

// LayoutPaths are the paths of a repository (by Location.Repository) that
// the layout of t routes to it, as Repositories does, sorted, none under
// another: the platform's files (tree.PlatformFile, treeedit.CatalogsFile,
// treeedit.ProjectsFile), the directory of the projects, the output's directories
// (OutputDirs); "." for the whole repository. A compile reads nothing
// else of it. projects is the treeedit.ProjectsFile of a split topology, its
// projects' locations; nil when it is not known yet: a repository that is
// neither the platform's nor the output's is then a project's, its path
// unknown, the whole repository.
func (t *Topology) LayoutPaths(repository string, projects *treeedit.ProjectsList) []string {
	var paths []string
	add := func(dir string, names ...string) {
		if len(names) == 0 {
			names = []string{""}
		}
		for _, n := range names {
			paths = append(paths, path.Join(".", dir, n))
		}
	}
	platform, output := t.Platform.Repository() == repository, t.Output.Repository() == repository
	if platform {
		add(t.Platform.Path, tree.PlatformFile, treeedit.CatalogsFile, treeedit.ProjectsFile)
	}
	if output {
		add(t.Output.Path, OutputDirs...)
	}
	switch {
	case !t.Split():
		if t.Projects.Repository() == repository {
			add(t.ProjectsListDir())
		}
	case projects == nil:
		if !platform && !output {
			add("")
		}
	default:
		for i := range projects.Projects {
			// An entry Project refuses is no path: the reads of its
			// project fail with the error (routed.resolve).
			entry := &projects.Projects[i]
			if loc, err := t.Project(entry.Name, entry); err == nil && loc.Repository() == repository {
				add(loc.Path)
			}
		}
	}
	if slices.Contains(paths, ".") {
		return []string{"."}
	}
	sort.Strings(paths) // a directory before what is under it
	var out []string
	for _, p := range paths {
		if !slices.ContainsFunc(out, func(dir string) bool { return under(p, dir) }) {
			out = append(out, p)
		}
	}
	return out
}

// Syncer is a store that can be brought to the latest remote revision on
// demand (GitStore): a write across repositories reads every one fresh.
type Syncer interface {
	Sync(ctx context.Context) error
}

// StoreOpener opens the store of the repository of a location, rooted at the
// repository's root.
type StoreOpener func(Location) (Store, error)

// Repositories is the Store of a topology: the paths of the tree are routed
// to the repository of their part, each opened once: projects/<p>/... to
// the project's, compiled/... and argocd/... to the output's, the rest
// (platform.yaml, treeedit.CatalogsFile, treeedit.ProjectsFile) to the platform's.
//
// A write runs once against the latest revisions, its changes held aside,
// then each repository it changes is committed in turn, with the same
// message: the projects', the platform's, then the output's, so the
// sources are in Git before what is compiled from them; the platform and
// the projects of one repository are one commit. A repository that moved
// since the write read it makes the whole write run again (Update). The
// output is never a repository of the sources (Topology.Validate): a
// project that projects.yaml places in it is refused.
type Repositories struct {
	Topology *Topology
	// MaxRetries bounds the replays of an edit whose repositories moved
	// since it read them; 0 is 5, as GitOptions.MaxRetries.
	MaxRetries int
	open       StoreOpener

	mu     sync.Mutex // one write at a time
	smu    sync.Mutex
	stores map[string]Store
}

// NewRepositories routes the layout of t to the stores open gives.
func NewRepositories(t *Topology, open StoreOpener) *Repositories {
	return &Repositories{Topology: t, open: open, stores: map[string]Store{}}
}

// Store returns the store of a location's repository.
func (r *Repositories) Store(loc Location) (Store, error) {
	r.smu.Lock()
	defer r.smu.Unlock()
	if s, ok := r.stores[loc.Repository()]; ok {
		return s, nil
	}
	s, err := r.open(loc)
	if err != nil {
		return nil, fmt.Errorf("repository %s (%s): %w", loc.URL, loc.Branch, err)
	}
	r.stores[loc.Repository()] = s
	return s, nil
}

// Stores returns the stores opened so far, by repository.
func (r *Repositories) Stores() map[string]Store {
	r.smu.Lock()
	defer r.smu.Unlock()
	out := make(map[string]Store, len(r.stores))
	maps.Copy(out, r.stores)
	return out
}

// View runs fn on the parts of the tree, each read in its repository, no
// lock held: of a Snapshotter, the snapshot read first, the same revision
// of the repository for the whole view.
func (r *Repositories) View(ctx context.Context, fn func(treeedit.Reader) error) error {
	return fn(&routed{repos: r, ctx: ctx, pinned: map[string]Snapshot{}})
}

// RevisionUpdater is a store that commits a change on a given revision only
// (GitStore.UpdateAt): the revision a change was computed from.
type RevisionUpdater interface {
	UpdateAt(ctx context.Context, base string, commit Commit, fn func(tx Tx) error) (string, error)
}

// Update runs fn, once on the revisions it reads, and commits its changes:
// to each repository that fn read, on the revision it read
// (RevisionUpdater, the snapshot its reads pinned), so that a change
// computed from a file never overwrites a newer version of it. A
// repository that moved since fn read it makes fn run again from the new
// revisions (up to MaxRetries times, then ErrConflict wrapping ErrMoved);
// fn must give the same change for the same tree. A repository fn only
// wrote, never read, takes its changes as they are on the latest revision
// (Store.Update).
func (r *Repositories) Update(ctx context.Context, commit Commit, fn func(Tx) error) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	retries := r.MaxRetries
	if retries <= 0 {
		retries = defaultRetries
	}
	for attempt := 0; ; attempt++ {
		rev, retry, err := r.updateOnce(ctx, commit, fn)
		if !retry {
			return rev, err
		}
		if attempt >= retries {
			return "", fmt.Errorf("%w: %w", ErrConflict, err)
		}
		slog.Info("The deployments repositories moved during the edit, computing it again on the new revisions", "error", err, "attempt", attempt+1)
	}
}

// defaultRetries is GitOptions.MaxRetries' default.
const defaultRetries = 5

// updateOnce is one run of Update's fn and its commits; retry when a
// repository moved before anything was committed.
func (r *Repositories) updateOnce(ctx context.Context, commit Commit, fn func(Tx) error) (rev string, retry bool, err error) {
	tx := &routed{repos: r, ctx: ctx, overlay: newOverlay(), synced: map[string]bool{}, pinned: map[string]Snapshot{}}
	if err := fn(tx); err != nil {
		return "", false, err
	}
	groups := tx.overlay.groups(r.Topology)
	bases := tx.revisions()
	// The first repository's commit checks its revision itself; the others
	// are checked before it, while nothing is committed yet, so that an edit
	// across repositories is not computed again once half of it is in.
	for _, g := range groups[min(1, len(groups)):] {
		moved, err := r.moved(ctx, g.loc, bases)
		if err != nil {
			return "", false, err
		}
		if moved {
			return "", true, fmt.Errorf("%s: %w", g.loc.URL, ErrMoved)
		}
	}
	for i, g := range groups {
		st, err := r.Store(g.loc)
		if err != nil {
			return rev, false, err
		}
		write := func(t Tx) error {
			for _, o := range g.ops {
				var err error
				if o.remove {
					err = t.Remove(o.inner)
				} else {
					err = t.WriteFile(o.inner, o.data)
				}
				if err != nil {
					return err
				}
			}
			return nil
		}
		var committed string
		if base, read := bases[g.loc.Repository()]; read {
			if at, ok := st.(RevisionUpdater); ok {
				committed, err = at.UpdateAt(ctx, base, commit, write)
			} else {
				committed, err = st.Update(ctx, commit, write)
			}
		} else {
			committed, err = st.Update(ctx, commit, write)
		}
		if err != nil {
			if i > 0 {
				return rev, false, fmt.Errorf("%s is committed, but not %s (run okdp-gitops compile): %w", groups[0].loc.URL, g.loc.URL, err)
			}
			return "", errors.Is(err, ErrMoved), err
		}
		if rev == "" {
			rev = committed
		}
	}
	return rev, false, nil
}

// moved tells whether the repository of loc, read by the edit at its
// revision in bases, is at another one now: false for one the edit did not
// read, or whose store keeps no revision.
func (r *Repositories) moved(ctx context.Context, loc Location, bases map[string]string) (bool, error) {
	base, read := bases[loc.Repository()]
	if !read {
		return false, nil
	}
	st, err := r.Store(loc)
	if err != nil {
		return false, err
	}
	syncer, ok := st.(Syncer)
	sn, snap := st.(Snapshotter)
	if !ok || !snap {
		return false, nil
	}
	if err := syncer.Sync(ctx); err != nil {
		return false, err
	}
	now, err := sn.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	return now.Revision() != base, nil
}

// target is where a layout path lives.
type target struct {
	loc   Location
	inner string // the path in the repository, "" for its root
	// projects: the path is projects/ itself, the list of the projects.
	projects bool
	// project: the project of a path under projects/<p>, root its directory.
	project string
	root    bool
}

// routed is the Tx (or treeedit.Reader) of Repositories: each path read from, or
// written to, the repository of its part.
type routed struct {
	repos *Repositories
	ctx   context.Context
	// overlay: the changes of a write across repositories, held until
	// they are committed.
	overlay *overlay
	synced  map[string]bool
	// pinned are the snapshots read, by repository: a view reads one
	// revision of each.
	mu     sync.Mutex
	pinned map[string]Snapshot
}

func (t *routed) resolve(p string) (target, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return target{}, err
	}
	topo := t.repos.Topology
	first, rest, _ := strings.Cut(cleaned, "/")
	join := func(dir, rest string) string {
		j := path.Join(dir, rest)
		if j == "." {
			return ""
		}
		return j
	}
	switch {
	case first == tree.ProjectsDir:
		if rest == "" {
			return target{projects: true}, nil
		}
		name, inProject, _ := strings.Cut(rest, "/")
		var entry *treeedit.ProjectEntry
		if topo.Split() {
			list, err := t.projectsList()
			if err != nil {
				return target{}, err
			}
			entry = list.Entry(name)
		}
		loc, err := topo.Project(name, entry)
		if err != nil {
			return target{}, err
		}
		if err := t.checkLocation(name, loc); err != nil {
			return target{}, err
		}
		return target{loc: loc, inner: join(loc.Path, inProject), project: name, root: inProject == ""}, nil
	case slices.Contains(OutputDirs, first):
		return target{loc: topo.Output, inner: join(topo.Output.Path, cleaned)}, nil
	case cleaned == ".":
		return target{}, fmt.Errorf("the root of the tree is in several repositories: read its parts")
	default:
		return target{loc: topo.Platform, inner: join(topo.Platform.Path, cleaned)}, nil
	}
}

// checkLocation refuses the location loc of the project name when its
// repository is another part's of the topology: the output's, which holds
// no source; and in a split topology, where each project has a repository
// of its own, the platform's, one of a catalog (catalogs.yaml) or one of a
// project listed before it (treeedit.ProjectsFile), whose writers would
// otherwise write the platform's files, a catalog or another project. A
// name the template maps to such a repository, or an entry that places the
// project there, fails the reads and writes of that project.
func (t *routed) checkLocation(name string, loc Location) error {
	topo := t.repos.Topology
	if sameRepository(loc.URL, topo.Output.URL) {
		return fmt.Errorf("project %s: its repository %s is the output's, which holds no source (%s)", name, loc.URL, treeedit.ProjectsFile)
	}
	if !topo.Split() {
		return nil
	}
	if sameRepository(loc.URL, topo.Platform.URL) {
		return fmt.Errorf("project %s: its repository %s is the platform's, which holds no project in a split topology (%s)", name, loc.URL, treeedit.ProjectsFile)
	}
	// A catalogs.yaml that cannot be read places nothing: the compile
	// refuses it.
	catalogs, _ := treeedit.ReadCatalogRepositories(t)
	for _, c := range catalogs {
		if sameRepository(loc.URL, c.URL) {
			return fmt.Errorf("project %s: its repository %s is the catalog %s's (%s)", name, loc.URL, c.Name, treeedit.CatalogsFile)
		}
	}
	// A projects.yaml that cannot be read lists nothing: the reads of the
	// list report it.
	if list, err := t.projectsList(); err == nil {
		for i := range list.Projects {
			other := &list.Projects[i]
			if other.Name == name {
				break // the projects before it keep their repository
			}
			if oloc, err := topo.Project(other.Name, other); err == nil && sameRepository(loc.URL, oloc.URL) {
				return fmt.Errorf("project %s: its repository %s is project %s's (%s)", name, loc.URL, other.Name, treeedit.ProjectsFile)
			}
		}
	}
	return nil
}

// CheckProjectLocation refuses a project name whose repository, by the
// topology's template or its entry of treeedit.ProjectsFile, is another
// part's (the output's, and in a split topology the platform's, a
// catalog's or another project's): checkLocation. It creates nothing.
func (r *Repositories) CheckProjectLocation(ctx context.Context, name string) error {
	return r.View(ctx, func(rd treeedit.Reader) error {
		t := rd.(*routed)
		var entry *treeedit.ProjectEntry
		if r.Topology.Split() {
			list, err := t.projectsList()
			if err != nil {
				return err
			}
			entry = list.Entry(name)
		}
		loc, err := r.Topology.Project(name, entry)
		if err != nil {
			return err
		}
		return t.checkLocation(name, loc)
	})
}

// projectsList reads treeedit.ProjectsFile (none listed when absent).
func (t *routed) projectsList() (*treeedit.ProjectsList, error) {
	raw, err := t.ReadFile(treeedit.ProjectsFile)
	if errors.Is(err, treeedit.ErrNotFound) {
		return &treeedit.ProjectsList{}, nil
	}
	if err != nil {
		return nil, err
	}
	return treeedit.DecodeProjects(raw)
}

// projectNames lists the projects: treeedit.ProjectsFile in a split topology, else
// the directories of the projects' repository.
func (t *routed) projectNames() ([]string, error) {
	topo := t.repos.Topology
	if topo.Split() {
		list, err := t.projectsList()
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(list.Projects))
		for _, p := range list.Projects {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		return names, nil
	}
	dir := topo.ProjectsListDir()
	return t.readDir(target{loc: topo.Projects, inner: dir})
}

// read runs fn on a view of the reader of a target's repository (synced
// first during a write): of a Snapshotter, the snapshot this view read
// first.
func (t *routed) read(tg target, fn func(treeedit.Reader) error) error {
	st, err := t.repos.Store(tg.loc)
	if err != nil {
		return err
	}
	repo := tg.loc.Repository()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.synced != nil && !t.synced[repo] {
		if s, ok := st.(Syncer); ok {
			if err := s.Sync(t.ctx); err != nil {
				return err
			}
		}
		t.synced[repo] = true
	}
	sn, ok := st.(Snapshotter)
	if !ok {
		return st.View(t.ctx, fn)
	}
	snap, ok := t.pinned[repo]
	if !ok {
		if snap, err = sn.Snapshot(t.ctx); err != nil {
			return err
		}
		t.pinned[repo] = snap
	}
	return fn(snap)
}

// revisions are the revisions the transaction read, by repository: of the
// stores whose reads are snapshots.
func (t *routed) revisions() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.pinned))
	for repo, snap := range t.pinned {
		out[repo] = snap.Revision()
	}
	return out
}

func rooted(inner string) string {
	if inner == "" {
		return "."
	}
	return inner
}

func (t *routed) ReadFile(p string) ([]byte, error) {
	tg, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	if tg.projects {
		return nil, fmt.Errorf("%s: %w", p, treeedit.ErrNotFound)
	}
	if t.overlay != nil {
		if data, state := t.overlay.file(tg); state != unknown {
			if state == absent {
				return nil, fmt.Errorf("%s: %w", p, treeedit.ErrNotFound)
			}
			return data, nil
		}
	}
	var data []byte
	err = t.read(tg, func(r treeedit.Reader) error {
		var err error
		data, err = r.ReadFile(rooted(tg.inner))
		return err
	})
	return data, err
}

func (t *routed) ReadDir(p string) ([]string, error) {
	tg, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	if tg.projects {
		return t.projectNames()
	}
	return t.readDir(tg)
}

func (t *routed) readDir(tg target) ([]string, error) {
	var names []string
	hidden := t.overlay != nil && t.overlay.removed(tg)
	if !hidden {
		err := t.read(tg, func(r treeedit.Reader) error {
			var err error
			names, err = r.ReadDir(rooted(tg.inner))
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	if t.overlay == nil {
		return names, nil
	}
	return t.overlay.names(tg, names), nil
}

func (t *routed) Exists(p string) bool {
	tg, err := t.resolve(p)
	if err != nil {
		return false
	}
	if tg.projects {
		names, err := t.projectNames()
		return err == nil && len(names) > 0
	}
	if t.repos.Topology.Split() && tg.root {
		// A project of a split topology exists when it is listed.
		list, err := t.projectsList()
		return err == nil && list.Entry(tg.project) != nil
	}
	if t.overlay != nil {
		if exists, known := t.overlay.exists(tg); known {
			return exists
		}
	}
	exists := false
	_ = t.read(tg, func(r treeedit.Reader) error {
		exists = r.Exists(rooted(tg.inner))
		return nil
	})
	return exists
}

func (t *routed) WriteFile(p string, data []byte) error {
	tg, err := t.resolve(p)
	if err != nil {
		return err
	}
	if tg.projects || tg.inner == "" {
		return fmt.Errorf("%s is a directory of the tree, not a file", p)
	}
	if t.overlay == nil {
		return errors.New("read-only view")
	}
	t.overlay.write(tg, data)
	return nil
}

func (t *routed) Remove(p string) error {
	tg, err := t.resolve(p)
	if err != nil {
		return err
	}
	if tg.projects || tg.inner == "" {
		return fmt.Errorf("refusing to remove %s: the root of a repository", p)
	}
	if t.overlay == nil {
		return errors.New("read-only view")
	}
	t.overlay.remove(tg)
	return nil
}

// overlay holds the changes of a write across repositories.
type overlay struct {
	ops     []overlayOp
	written map[string][]byte // by key, what is written and not removed since
	gone    []string          // keys removed, with all below them
}

type overlayOp struct {
	loc    Location
	key    string
	inner  string
	data   []byte
	remove bool
}

type fileState int

const (
	unknown fileState = iota
	present
	absent
)

func newOverlay() *overlay { return &overlay{written: map[string][]byte{}} }

func overlayKey(tg target) string { return tg.loc.Repository() + "\x00" + tg.inner }

func under(key, dir string) bool { return key == dir || strings.HasPrefix(key, dir+"/") }

func (o *overlay) write(tg target, data []byte) {
	key := overlayKey(tg)
	o.written[key] = append([]byte(nil), data...)
	o.ops = append(o.ops, overlayOp{loc: tg.loc, key: key, inner: tg.inner, data: o.written[key]})
}

func (o *overlay) remove(tg target) {
	key := overlayKey(tg)
	for k := range o.written {
		if under(k, key) {
			delete(o.written, k)
		}
	}
	o.gone = append(o.gone, key)
	o.ops = append(o.ops, overlayOp{loc: tg.loc, key: key, inner: tg.inner, remove: true})
}

// removed reports whether the path was removed (and its base content with it).
func (o *overlay) removed(tg target) bool {
	key := overlayKey(tg)
	for _, g := range o.gone {
		if under(key, g) {
			return true
		}
	}
	return false
}

func (o *overlay) file(tg target) ([]byte, fileState) {
	if data, ok := o.written[overlayKey(tg)]; ok {
		return append([]byte(nil), data...), present
	}
	if o.removed(tg) {
		return nil, absent
	}
	return nil, unknown
}

func (o *overlay) exists(tg target) (bool, bool) {
	key := overlayKey(tg)
	for k := range o.written {
		if under(k, key) {
			return true, true
		}
	}
	if o.removed(tg) {
		return false, true
	}
	return false, false
}

// names merges the base listing of a directory with the changes.
func (o *overlay) names(tg target, base []string) []string {
	dir := overlayKey(tg)
	seen := map[string]bool{}
	for _, name := range base {
		child := tg
		child.inner = path.Join(tg.inner, name)
		if !o.removed(child) {
			seen[name] = true
		}
	}
	prefix := dir + "/"
	if tg.inner == "" {
		prefix = dir
	}
	for k := range o.written {
		if rest, ok := strings.CutPrefix(k, prefix); ok && rest != "" {
			name, _, _ := strings.Cut(rest, "/")
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// overlayGroup: the changes of one repository.
type overlayGroup struct {
	loc  Location
	ops  []overlayOp
	rank int
}

// groups returns the changes by repository, in commit order: the projects'
// repositories, the platform's, then the output's.
func (o *overlay) groups(t *Topology) []overlayGroup {
	byRepo := map[string]*overlayGroup{}
	var order []string
	for _, op := range o.ops {
		repo := op.loc.Repository()
		g, ok := byRepo[repo]
		if !ok {
			g = &overlayGroup{loc: op.loc, rank: 0}
			switch repo {
			case t.Output.Repository():
				g.rank = 2
			case t.Platform.Repository():
				g.rank = 1
			}
			byRepo[repo] = g
			order = append(order, repo)
		}
		g.ops = append(g.ops, op)
	}
	out := make([]overlayGroup, 0, len(order))
	for _, repo := range order {
		out = append(out, *byRepo[repo])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	return out
}
