// Package tree reads the tree of a deployment, in one or more
// repositories, compiles its instances together and builds the deployment
// graph of those that compile (docs/specs/01_layout.md).
package tree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler-security/policy"
	"github.com/okdp/okdp-compiler/chart"
)

// Repository is one part of the tree. Name identifies it in messages. Role
// is required: Load and LoadCatalog refuse the zero value.
type Repository struct {
	Name string
	FS   fs.FS
	Role Role
}

// Role says what a repository contributes to the tree. A part outside the
// repository's role is refused rather than read (docs/04_threat_model.md).
// The maintainers of a catalog can therefore add neither a project,
// privileged or not, nor the platform's values.
type Role string

const (
	// RoleTree is a repository of the deployment, the platform's or a
	// project's. It may hold any part of the tree.
	RoleTree Role = "tree"
	// RoleCatalog is a catalog of packages. It may hold catalog/ and
	// contracts/ only: PlatformFile or projects/ in it is refused.
	RoleCatalog Role = "catalog"
)

// The file and directory names of the tree.
const (
	PlatformFile = "platform.yaml"       // the platform's values, context level 0
	ContractsDir = compiler.ContractsDir // <contract>.schema.json, the catalog's
	CatalogDir   = compiler.CatalogDir   // <service>/<version>/, a package, the catalog's
	ProjectsDir  = "projects"            // <project>/, a namespace
	ProjectFile  = "project.yaml"        // a project's values, context level 1
	// ProjectDescription is the key of ProjectFile that describes the
	// project, a string. It is not a value of the context.
	ProjectDescription = "description"
	InstancesDir       = "instances"        // <instance>/, the release <project>-<instance>
	InstanceFile       = "instance.yaml"    // {service, version, claimers}
	ConnectionsFile    = "connections.yaml" // the instance's own connections, context level 2
	ConfigFile         = "config.yaml"      // the instance's values
)

// ReleaseName returns the release of an instance, <project>-<instance>. It
// makes the releases of a tree unique across its projects, because the
// HelmReleases or Applications of a deployer share a namespace. Two names
// that hold a "-" can still meet (a-b/c and a/b-c): the targets refuse
// those.
func ReleaseName(project, instance string) string { return project + "-" + instance }

// parts is the repositories merged: it records the owner of each part of
// the tree. The catalog (compiler.LoadCatalog) owns the packages and the
// contracts.
type parts struct {
	platform *Repository            // the owner of PlatformFile, nil when none
	projects map[string]*Repository // the owner of projects/<project>/, by project
	catalog  *compiler.Catalog      // the packages and the contracts of every repository
	packages map[[2]string]*compiler.Definition
	rules    *compiler.RuleSet // the rule set the packages are read for; nil, the mandatory one
	// check is the host's policy on the global of the layers the platform
	// does not review (BuildOptions.CheckGlobals); nil checks nothing.
	check   func(context.Context, policy.GlobalsInput) error
	globals map[[2]string]M // the keys of global each package declares (declaredGlobals)
}

// Load returns every instance of the tree that repos make together, sorted
// by project, then by instance name. Each part of the tree is in one
// repository, so the order of repos changes nothing:
//
//	platform.yaml         PlatformFile
//	catalog/<service>/    every version of the service
//	projects/<project>/   the project's file and its instances
//
// A contract may be in several repositories when its bytes are the same in
// each. The context of an instance is PlatformFile at level 0, ProjectFile
// at level 1 and ConnectionsFile at level 2. An absent file gives no layer.
//
// The error lists every problem at once. A package that fails is reported
// once. A tree with any problem gives no instance: LoadProjects loads what
// it can.
//
// The packages are read for rules: okdp.yaml of a package may hold their
// sections (compiler.LoadDefinition). Nil rules stand for the mandatory
// set.
func Load(ctx context.Context, repos []Repository, rules *compiler.RuleSet) ([]compiler.Instance, error) {
	l, err := LoadProjects(ctx, repos, rules)
	if err != nil {
		return nil, err
	}
	if err := newError(refusedErrs(l.Refused)); err != nil {
		return nil, err
	}
	return l.Instances, nil
}

// Loaded is a tree as LoadProjects read it.
type Loaded struct {
	Instances []compiler.Instance // the instances of the projects that load
	Refused   []Refused           // the projects that do not load, in name order
}

// packageError marks a package that fails to load. This is a problem of the
// catalog, not of the instance that names the package.
type packageError struct{ err error }

func (e *packageError) Error() string { return e.err.Error() }
func (e *packageError) Unwrap() error { return e.err }

// LoadProjects is Load, project by project, the packages read for rules
// as Load reads them. A problem of the platform (the repositories and
// their roles, PlatformFile, CatalogsFile, a package or its contracts) is
// an error: an *LoadError with every problem, the platform's first, then
// those of the projects. A problem of a project's own refuses that
// project alone: Loaded.Refused lists it, and the instances of the other
// projects load. What a file sets beyond what its level may set (levels)
// is a problem of the file's owner.
func LoadProjects(ctx context.Context, repos []Repository, rules *compiler.RuleSet) (*Loaded, error) {
	return loadProjects(ctx, repos, rules, nil)
}

// loadProjects is LoadProjects, the host's policy on the global of the
// layers the platform does not review given by check (nil checks
// nothing).
func loadProjects(ctx context.Context, repos []Repository, rules *compiler.RuleSet, check func(context.Context, policy.GlobalsInput) error) (*Loaded, error) {
	t, err := index(repos)
	if err != nil {
		return nil, newError([]error{err})
	}
	t.rules, t.check = rules, check
	var platformErrs []error // the problems of the platform's parts and of the packages
	for _, r := range repos {
		if r.Role == RoleTree {
			platformErrs = append(platformErrs, t.checkCatalogs(r)...)
		}
	}
	var platform levels
	if t.platform != nil {
		v, err := readValues(*t.platform, PlatformFile)
		platformErrs = append(platformErrs, err)
		if v != nil {
			platform, err = platform.add(layer{level: 0, file: where(*t.platform, PlatformFile), values: v})
			platformErrs = append(platformErrs, err)
		}
	}
	l := &Loaded{}
	for _, project := range slices.Sorted(maps.Keys(t.projects)) {
		repo := *t.projects[project]
		dir := path.Join(ProjectsDir, project)
		file := path.Join(dir, ProjectFile)
		var own []error // the project's own problems
		ls := platform
		ls.namespace = project
		values, err := readProject(repo, file)
		own = append(own, err)
		readable := err == nil // its file reads and sets what level 1 may
		var groups map[string]compiler.AccessRole
		if readable {
			if values != nil {
				ls, err = ls.add(layer{level: 1, file: where(repo, file), values: values})
				own = append(own, err)
				readable = err == nil
			}
			groups, err = projectAccess(where(repo, file), values, project)
			own = append(own, err)
			readable = readable && err == nil
		}
		in := withGroups(compiler.Inputs{Rules: t.rules, Contracts: t.catalog.Contracts(), Values: slices.Clip(ls.layers), Namespace: project}, groups)
		if readable && values != nil {
			err := globalsCheck(ctx, t.check, in, where(repo, file), values)
			own = append(own, err)
			readable = err == nil
		}
		names, err := dirs(repo, path.Join(dir, InstancesDir))
		own = append(own, err)
		// A project without instances has no compile to check its
		// declarations. Check them here.
		if err == nil && len(names) == 0 && values != nil && readable {
			for _, err := range unjoin(compiler.CheckDeclarations(ctx, in)) {
				own = append(own, fmt.Errorf("%s: %w", where(repo, file), err))
			}
		}
		var instances []compiler.Instance
		for _, name := range names {
			inst, err := t.instance(ctx, repo, project, name, ls, groups)
			if pe, ok := errors.AsType[*packageError](err); ok {
				// This is the catalog's problem, not the project's.
				platformErrs = append(platformErrs, pe)
				continue
			}
			if err != nil || inst.Definition == nil {
				own = append(own, err)
				continue
			}
			instances = append(instances, inst)
		}
		if r := refusal(project, own); r != nil {
			r.Inputs = compiler.Inputs{Contracts: t.catalog.Contracts(), Values: slices.Clip(slices.Clone(platform.layers)), Namespace: project}
			if readable {
				r.Inputs.Values, r.Inputs.Settings, r.Own = slices.Clip(ls.layers), GroupsSettings(groups), true
			}
			l.Refused = append(l.Refused, *r)
			continue
		}
		l.Instances = append(l.Instances, instances...)
	}
	if newError(platformErrs) != nil {
		return nil, newError(slices.Concat(platformErrs, refusedErrs(l.Refused)))
	}
	return l, nil
}

// refusedErrs returns the errors of the refused projects, in their order.
func refusedErrs(refused []Refused) []error {
	var errs []error
	for _, r := range refused {
		errs = append(errs, r.Errs...)
	}
	return errs
}

// refusal builds the Refused of project, nil when errs holds no problem.
func refusal(project string, errs []error) *Refused {
	e, ok := newError(errs).(*LoadError)
	if !ok {
		return nil
	}
	return &Refused{Namespace: project, Problems: e.Problems, Errs: e.errs}
}

// instance loads the instance name of project from repo. ls holds the
// layers below its own, groups the roles of its project's groups.
// Without claimers in its InstanceFile, Inputs.Claimers is nil and the
// package's claimers apply. The instance comes without its Definition
// when its package already failed for an instance before it.
func (t *parts) instance(ctx context.Context, repo Repository, project, name string, ls levels, groups map[string]compiler.AccessRole) (compiler.Instance, error) {
	dir := path.Join(ProjectsDir, project, InstancesDir, name)
	if err := instanceFiles(repo, dir); err != nil {
		return compiler.Instance{}, err
	}
	file := path.Join(dir, InstanceFile)
	var pkg InstanceSpec
	b, err := fs.ReadFile(repo.FS, file)
	if err == nil {
		pkg, err = ParseInstance(b)
	}
	if err == nil && (pkg.Service == "" || pkg.Version == "") {
		err = errors.New("service and version are required")
	}
	if err == nil {
		err = compiler.CheckClaimers("claimers", pkg.Claimers)
	}
	if err != nil {
		return compiler.Instance{}, fmt.Errorf("%s: %w", where(repo, file), unwrapPath(err))
	}
	def, err := t.definition(pkg.Service, pkg.Version)
	if err != nil {
		err = fmt.Errorf("%s: %w", where(repo, file), err)
		if _, ok := errors.AsType[*packageError](err); ok {
			// A package that fails to load is the catalog's problem, so the
			// tree's, not the project's.
			err = &packageError{err}
		}
		return compiler.Instance{}, err
	}
	var errs []error
	connectionsFile := path.Join(dir, ConnectionsFile)
	connections, err := readValues(repo, connectionsFile)
	if err != nil {
		return compiler.Instance{}, err
	}
	if connections != nil {
		for _, k := range slices.Sorted(maps.Keys(connections)) {
			if k != "connections" {
				return compiler.Instance{}, fmt.Errorf("%s: %s: the instance's own connections only (connections): the rest of the context is the platform's or the project's", where(repo, connectionsFile), k)
			}
		}
		ls, err = ls.add(layer{level: 2, file: where(repo, connectionsFile), values: connections})
		errs = append(errs, err)
	}
	configFile := path.Join(dir, ConfigFile)
	values, err := readValues(repo, configFile)
	if err != nil {
		return compiler.Instance{}, err
	}
	in := withGroups(compiler.Inputs{Contracts: t.catalog.Contracts(), Values: slices.Clip(ls.layers), Release: ReleaseName(project, name), Namespace: project, Claimers: pkg.Claimers}, groups)
	if values != nil {
		errs = append(errs, checkConfig(where(repo, configFile), values, t.declared(pkg.Service, pkg.Version, def)))
		in.Values = append(in.Values, values)
	}
	if err := errors.Join(errs...); err != nil {
		return compiler.Instance{}, err
	}
	if values != nil {
		checked := in
		checked.Rules = t.rules
		if err := globalsCheck(ctx, t.check, checked, where(repo, configFile), values); err != nil {
			return compiler.Instance{}, err
		}
	}
	return compiler.Instance{Definition: def, Inputs: in, Name: name}, nil
}

// instanceFiles checks that the instance's directory holds InstanceFile,
// ConfigFile and ConnectionsFile only, each a file. No compile would read
// any other entry (a values.yaml, a hidden file), so it is refused.
func instanceFiles(repo Repository, dir string) error {
	entries, err := fs.ReadDir(repo.FS, dir)
	if err != nil {
		return fmt.Errorf("%s: %w", where(repo, dir), unwrapPath(err))
	}
	var errs []error
	for _, e := range entries {
		switch e.Name() {
		case InstanceFile, ConfigFile, ConnectionsFile:
			if !e.IsDir() {
				continue
			}
		}
		errs = append(errs, fmt.Errorf("%s: not a file of an instance, which holds %s, %s and %s only: no compile reads it", where(repo, path.Join(dir, e.Name())), InstanceFile, ConfigFile, ConnectionsFile))
	}
	return errors.Join(errs...)
}

// InstanceSpec is an instance's InstanceFile, as written. Claimers lists
// the namespaces whose instances may claim from it from another namespace
// (namespace names, or "*"). Nil means absent: the package's claimers
// apply. Empty means none (claimers: []).
type InstanceSpec struct {
	Service  string   `yaml:"service"`
	Version  string   `yaml:"version"`
	Claimers []string `yaml:"claimers"`
}

// ParseInstance reads the first document of an InstanceFile strictly: it
// refuses an unknown key, a duplicate key and a value of the wrong form. It
// does not require service and version, nor check the claimers: Load does.
// Its messages name the keys and the lines, never a Go type.
func ParseInstance(b []byte) (InstanceSpec, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return InstanceSpec{}, nil
		}
		return InstanceSpec{}, err
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return InstanceSpec{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return InstanceSpec{}, fmt.Errorf("yaml: unmarshal errors:\n  line %d: a mapping of service, version and claimers expected", root.Line)
	}
	var problems []string
	first := map[string]int{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if at, dup := first[k.Value]; dup {
			problems = append(problems, fmt.Sprintf("line %d: mapping key %q already defined at line %d", k.Line, k.Value, at))
			continue
		}
		first[k.Value] = k.Line
		switch k.Value {
		case "service", "version":
			if v.Kind != yaml.ScalarNode {
				problems = append(problems, fmt.Sprintf("line %d: %s: a string expected", v.Line, k.Value))
			}
		case "claimers":
			if !claimersNode(v) {
				problems = append(problems, fmt.Sprintf("line %d: claimers: a list of namespace names expected", v.Line))
			}
		default:
			problems = append(problems, fmt.Sprintf("line %d: field %s not found: service, version and claimers only", k.Line, k.Value))
		}
	}
	if problems != nil {
		return InstanceSpec{}, fmt.Errorf("yaml: unmarshal errors:\n  %s", strings.Join(problems, "\n  "))
	}
	var spec InstanceSpec
	if err := root.Decode(&spec); err != nil {
		return InstanceSpec{}, err
	}
	return spec, nil
}

// claimersNode reports whether n is null or a list of scalars.
func claimersNode(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Tag == "!!null"
	case yaml.SequenceNode:
		return !slices.ContainsFunc(n.Content, func(c *yaml.Node) bool { return c.Kind != yaml.ScalarNode })
	}
	return false
}

// definition loads the package catalog/<service>/<version>/, once. Its
// Chart.yaml must have that name and version. A version the catalog lacks
// is the instance's error. A package that fails to load is a *packageError
// the first time only: later calls return nil and no error.
func (t *parts) definition(service, version string) (*compiler.Definition, error) {
	files, err := t.catalog.Package(service, version)
	if err != nil {
		return nil, err
	}
	key := [2]string{service, version}
	if def, loaded := t.packages[key]; loaded {
		return def, nil
	}
	t.packages[key] = nil
	def, err := compiler.LoadDefinition(files, t.rules)
	if err == nil && (def.Chart.Name != service || def.Chart.Version != version) {
		err = fmt.Errorf("its Chart.yaml is %s %s", def.Chart.Name, def.Chart.Version)
	}
	if err != nil {
		svc, _ := t.catalog.Service(service)
		return nil, &packageError{fmt.Errorf("the package %s: %w", path.Join(svc.Repository, CatalogDir, service, version), err)}
	}
	t.packages[key] = def
	return def, nil
}

// declared returns the keys of global that the package of service and
// version, def, declares, read once (declaredGlobals).
func (t *parts) declared(service, version string, def *compiler.Definition) M {
	key := [2]string{service, version}
	if g, ok := t.globals[key]; ok {
		return g
	}
	g := declaredGlobals(def)
	t.globals[key] = g
	return g
}

// index records the owner of each part of the tree. It reads the
// repositories in name order and reports every conflict at once, those of
// the parts in two repositories last, sorted by part. The catalog that the
// repositories of a known role make together (compiler.LoadCatalog) gives
// the packages and the contracts, from their catalog/ and contracts/ alone
// (catalogView); its problems come after the tree's. In a catalog
// repository, PlatformFile or projects/ is refused as soon as it exists (a
// file, an empty directory); the rest of the catalog is still read.
func index(repos []Repository) (*parts, error) {
	repos = slices.SortedFunc(slices.Values(repos), func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
	t := &parts{projects: map[string]*Repository{}, packages: map[[2]string]*compiler.Definition{}, globals: map[[2]string]M{}}
	var errs []error
	var catalogs []compiler.CatalogFiles
	owners := map[string][]string{} // a part, as messages name it: the repositories that have it
	own := func(part string, repo *Repository) { owners[part] = append(owners[part], repo.Name) }
	for i := range repos {
		repo := &repos[i]
		switch repo.Role {
		case RoleTree, RoleCatalog:
			catalogs = append(catalogs, compiler.CatalogFiles{Name: repo.Name, FS: catalogView{repo.FS}})
		case "":
			errs = append(errs, fmt.Errorf("repository %s: no role (Repository.Role): %s, a part of the deployment, or %s, packages and contracts only", repo.Name, RoleTree, RoleCatalog))
			continue
		default:
			errs = append(errs, fmt.Errorf("repository %s: role %q: %s, a part of the deployment, or %s, packages and contracts only", repo.Name, repo.Role, RoleTree, RoleCatalog))
			continue
		}
		if repo.Role == RoleCatalog {
			for _, p := range []string{PlatformFile, ProjectsDir} {
				if _, err := fs.Stat(repo.FS, p); err == nil {
					errs = append(errs, fmt.Errorf("%s: a catalog repository holds %s/ and %s/ only: %s is the deployment's (a repository of role %s)", where(*repo, p), CatalogDir, ContractsDir, p, RoleTree))
				} else if !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, fmt.Errorf("%s: %w", where(*repo, p), unwrapPath(err)))
				}
			}
			continue
		}
		if _, err := fs.Stat(repo.FS, PlatformFile); err == nil {
			own(PlatformFile, repo)
			t.platform = repo
		} else if !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %w", where(*repo, PlatformFile), unwrapPath(err)))
		}
		if _, err := fs.Stat(repo.FS, CatalogsFile); err == nil {
			own(CatalogsFile, repo)
		}
		projects, err := dirs(*repo, ProjectsDir)
		errs = append(errs, err)
		for _, p := range projects {
			own(fmt.Sprintf("project %q", p), repo)
			t.projects[p] = repo
		}
	}
	for _, part := range slices.Sorted(maps.Keys(owners)) {
		if names := owners[part]; len(names) > 1 {
			errs = append(errs, fmt.Errorf("%s is in the repositories %s and %s", part, strings.Join(names[:len(names)-1], ", "), names[len(names)-1]))
		}
	}
	catalog, err := compiler.LoadCatalog(catalogs)
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	t.catalog = catalog
	return t, nil
}

// catalogView is the catalog of a repository, its catalog/ and contracts/:
// the rest of the repository is the tree's, which the catalog does not
// read.
type catalogView struct{ fsys fs.FS }

func (v catalogView) Open(name string) (fs.File, error) {
	for _, dir := range []string{".", CatalogDir, ContractsDir} {
		if name == dir || dir != "." && strings.HasPrefix(name, dir+"/") {
			return v.fsys.Open(name)
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// dirs returns the directories of dir, none when dir does not exist.
func dirs(repo Repository, dir string) ([]string, error) {
	entries, err := fs.ReadDir(repo.FS, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where(repo, dir), unwrapPath(err))
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// readValues reads a values file. It returns no values when the file
// does not exist.
func readValues(repo Repository, file string) (M, error) {
	b, err := fs.ReadFile(repo.FS, file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where(repo, file), unwrapPath(err))
	}
	v, err := chart.ParseValues(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where(repo, file), err)
	}
	return v, nil
}

// readProject reads a project's file, the layer of level 1. It removes
// ProjectDescription, which must be a string or null: no instance
// receives it.
func readProject(repo Repository, file string) (M, error) {
	v, err := readValues(repo, file)
	if d, set := v[ProjectDescription]; set {
		if _, isString := d.(string); !isString && d != nil {
			return nil, fmt.Errorf("%s: %s: a string, the project's description", where(repo, file), ProjectDescription)
		}
		delete(v, ProjectDescription)
	}
	return v, err
}

func where(repo Repository, p string) string { return path.Join(repo.Name, p) }

// unwrapPath strips the path of an fs.PathError from err. Messages name the
// path themselves, with its repository.
func unwrapPath(err error) error {
	if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
