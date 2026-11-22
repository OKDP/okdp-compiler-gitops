package gitops

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
)

// Deployments reads and writes the tree of a platform's deployments, its
// parts each in the repository its Topology says (Repositories), or all of
// them at the root of Store. Its reads and its edits are okdp-compiler's
// (package treeedit), each edit a transaction of the tree, checked by a
// compile of the whole tree before and after it (Compiler), then
// committed; the output is WriteCompiled's, always all of it from the tree
// (the CI of the output running okdp-gitops compile).
type Deployments struct {
	Store Store
	// Compiler checks the edits, and compiles the tree for its readers;
	// nil: the edits are not checked.
	Compiler *Compiler
	// Topology is where the parts of the tree live; nil: all of them at the
	// root of Store, a view of the whole tree in memory (a MemoryStore: the
	// tests, a tree read whole), never a repository of a deployment, whose
	// output is in a repository of its own (Repositories).
	Topology *Topology
	// Forgejo creates the repository of a new project of a split topology;
	// nil: such a project cannot be created.
	Forgejo *ForgejoClient
	// OutputPrefix is the directory of the output in its repository, the
	// path the engines read the compiled tree at ("" at its root): the
	// topology's output path by default.
	OutputPrefix string
}

// NewDeployments reads and writes the tree of store, a topology's
// Repositories or one store in memory holding every part.
func NewDeployments(store Store) *Deployments {
	d := &Deployments{Store: store}
	if repos, ok := store.(*Repositories); ok {
		d.Topology = repos.Topology
		d.OutputPrefix = repos.Topology.Output.Path
	}
	return d
}

// split reports whether each project has a repository of its own.
func (d *Deployments) split() bool { return d.Topology != nil && d.Topology.Split() }

// view runs read on the tree as it is.
func view[T any](ctx context.Context, d *Deployments, read func(treeedit.Reader) (T, error)) (T, error) {
	var out T
	err := d.Store.View(ctx, func(r treeedit.Reader) error {
		var err error
		out, err = read(r)
		return err
	})
	return out, err
}

// output is the output of the tree r holds, compiled/, as an fs.FS.
func output(r treeedit.Reader) fs.FS { return treeedit.OutputFS(r) }

// Apply commits edit (treeedit.Apply): its change of the tree at the
// latest revisions, checked by the compile of the tree before and after
// it, written to the repositories of its paths, in one commit of each
// named after the edit, by actor ("okdp: <action> <target> by <user>").
// A repository that moved since the edit read it makes it run again on the
// new revisions (Repositories.Update). The change
// committed, and the revision that holds it.
func (d *Deployments) Apply(ctx context.Context, actor Actor, edit treeedit.Edit) (*treeedit.Change, string, error) {
	var compile func(treeedit.Reader) (*tree.Tree, error)
	if d.Compiler != nil {
		compile = func(r treeedit.Reader) (*tree.Tree, error) { return d.Compiler.Compile(ctx, r) }
	}
	var change *treeedit.Change
	rev, err := d.Store.Update(ctx, NewCommit(edit.Action, edit.Target, actor), func(tx Tx) error {
		c, warnings, err := treeedit.Apply(tx, edit, compile)
		for _, w := range warnings {
			slog.Warn(w.Message, "instance", w.Project+"/"+w.Instance)
		}
		change = c
		return err
	})
	return change, rev, err
}

// apply is Apply of an edit, or the edit's error.
func (d *Deployments) apply(ctx context.Context, actor Actor, edit treeedit.Edit, err error) (string, error) {
	if err != nil {
		return "", err
	}
	_, rev, err := d.Apply(ctx, actor, edit)
	return rev, err
}

// Tree compiles the tree as it is (Compiler.Compile), for its readers: the
// outcome of each instance, the claims bound to each provider, where each
// claim of an instance is bound. Without a compiler, an empty tree.
func (d *Deployments) Tree(ctx context.Context) (*tree.Tree, error) {
	if d.Compiler == nil {
		return &tree.Tree{}, nil
	}
	return view(ctx, d, func(r treeedit.Reader) (*tree.Tree, error) { return d.Compiler.Compile(ctx, r) })
}

// --- Instances ---

// ListInstances returns the instances of a project (treeedit.ListInstances).
func (d *Deployments) ListInstances(ctx context.Context, project string) ([]treeedit.Instance, error) {
	return view(ctx, d, func(r treeedit.Reader) ([]treeedit.Instance, error) { return treeedit.ListInstances(r, project) })
}

// GetInstance returns one instance (treeedit.ReadInstance).
func (d *Deployments) GetInstance(ctx context.Context, project, name string) (*treeedit.Instance, error) {
	return view(ctx, d, func(r treeedit.Reader) (*treeedit.Instance, error) { return treeedit.ReadInstance(r, project, name) })
}

// CreateInstance commits treeedit.CreateInstance.
func (d *Deployments) CreateInstance(ctx context.Context, actor Actor, inst treeedit.Instance) (string, error) {
	edit, err := treeedit.CreateInstance(inst)
	return d.apply(ctx, actor, edit, err)
}

// UpdateInstance commits treeedit.UpdateInstance, and returns the
// instance written. change may run more than once when a repository
// moved and the edit runs again.
func (d *Deployments) UpdateInstance(ctx context.Context, actor Actor, project, name string, change func(inst *treeedit.Instance) error) (*treeedit.Instance, string, error) {
	edit, err := treeedit.UpdateInstance(project, name, change)
	if err != nil {
		return nil, "", err
	}
	c, rev, err := d.Apply(ctx, actor, edit)
	if err != nil {
		return nil, rev, err
	}
	return c.Instance, rev, nil
}

// DeleteInstance commits treeedit.DeleteInstance.
func (d *Deployments) DeleteInstance(ctx context.Context, actor Actor, project, name string) (string, error) {
	edit, err := treeedit.DeleteInstance(project, name)
	return d.apply(ctx, actor, edit, err)
}

// ReadCompiled returns the compiled files of an instance in the output
// (treeedit.ReadCompiled), an error wrapping treeedit.ErrNotFound before
// the compile (a CI on the commit of its sources) has written them.
func (d *Deployments) ReadCompiled(ctx context.Context, project, name string) (*treeedit.CompiledFiles, error) {
	return view(ctx, d, func(r treeedit.Reader) (*treeedit.CompiledFiles, error) {
		return treeedit.ReadCompiled(output(r), project, name)
	})
}

// ListPublished returns what the compiled instances of a project publish
// in the output (treeedit.ListPublished).
func (d *Deployments) ListPublished(ctx context.Context, project string) ([]treeedit.Published, error) {
	return view(ctx, d, func(r treeedit.Reader) ([]treeedit.Published, error) {
		return treeedit.ListPublished(r, output(r), project)
	})
}

// --- Connections and named claims ---

// ListConnections returns the connections a project declares, the
// platform's for "" (treeedit.ListConnections).
func (d *Deployments) ListConnections(ctx context.Context, project string) ([]compiler.Connection, error) {
	return view(ctx, d, func(r treeedit.Reader) ([]compiler.Connection, error) { return treeedit.ListConnections(r, project) })
}

// GetConnection returns one connection of a project, the platform's for
// "" (treeedit.ReadConnection).
func (d *Deployments) GetConnection(ctx context.Context, project, name string) (*compiler.Connection, error) {
	return view(ctx, d, func(r treeedit.Reader) (*compiler.Connection, error) {
		return treeedit.ReadConnection(r, project, name)
	})
}

// PutConnection commits treeedit.PutConnection.
func (d *Deployments) PutConnection(ctx context.Context, actor Actor, project string, c compiler.Connection, create bool) (string, error) {
	edit, err := treeedit.PutConnection(project, c, create)
	return d.apply(ctx, actor, edit, err)
}

// DeleteConnection commits treeedit.DeleteConnection.
func (d *Deployments) DeleteConnection(ctx context.Context, actor Actor, project, name string) (string, error) {
	edit, err := treeedit.DeleteConnection(project, name)
	return d.apply(ctx, actor, edit, err)
}

// ListClaims returns the named claims of a project (treeedit.ListClaims).
func (d *Deployments) ListClaims(ctx context.Context, project string) ([]compiler.ClaimSpec, error) {
	return view(ctx, d, func(r treeedit.Reader) ([]compiler.ClaimSpec, error) { return treeedit.ListClaims(r, project) })
}

// PutClaim commits treeedit.PutClaim.
func (d *Deployments) PutClaim(ctx context.Context, actor Actor, project string, c compiler.ClaimSpec, create bool) (string, error) {
	edit, err := treeedit.PutClaim(project, c, create)
	return d.apply(ctx, actor, edit, err)
}

// DeleteClaim commits treeedit.DeleteClaim.
func (d *Deployments) DeleteClaim(ctx context.Context, actor Actor, project, name string) (string, error) {
	edit, err := treeedit.DeleteClaim(project, name)
	return d.apply(ctx, actor, edit, err)
}

// --- Projects ---

// ListProjects returns the projects of the tree (treeedit.ListProjects;
// in a split topology, those of treeedit.ProjectsFile, each listed even
// while its repository does not answer).
func (d *Deployments) ListProjects(ctx context.Context) ([]treeedit.Project, error) {
	projects, err := view(ctx, d, func(r treeedit.Reader) ([]treeedit.Project, error) { return treeedit.ListProjects(r, d.split()) })
	for _, p := range projects {
		if p.ReadErr != nil {
			slog.Warn("Listing a project whose project.yaml cannot be read", "error", p.ReadErr, "project", p.Name)
		}
	}
	return projects, err
}

// GetProject returns one project (treeedit.ReadProject).
func (d *Deployments) GetProject(ctx context.Context, name string) (*treeedit.Project, error) {
	return view(ctx, d, func(r treeedit.Reader) (*treeedit.Project, error) { return treeedit.ReadProject(r, name) })
}

// CheckProjectName refuses a project name the compile refuses, with the
// platform's values of the tree as it is (treeedit.CheckProjectName, a
// *treeedit.CompileError). CreateProject checks it before it creates
// anything; a program that creates more of a project, its Namespace,
// checks it first too.
func (d *Deployments) CheckProjectName(ctx context.Context, name string) error {
	var set *compiler.RuleSet
	if d.Compiler != nil {
		set = d.Compiler.Rules
	}
	_, err := view(ctx, d, func(r treeedit.Reader) (struct{}, error) { return struct{}{}, treeedit.CheckProjectName(set, r, name) })
	return err
}

// ErrNoForgejo refuses a project of a split topology without the forgejo
// settings that create its repository.
var ErrNoForgejo = errors.New("each project has a repository of its own (the topology's projects url names {project}), and the topology has no forgejo section to create it: add forgejo (url, owner, tokenFile) to GITOPS_CONFIG, or create the repository and add the project to " + treeedit.ProjectsFile + " by hand")

// CreateProject commits treeedit.CreateProject, its name checked first
// (CheckProjectName). In a split topology, the project's repository is
// created first (Forgejo), the repository of the topology's projects url
// (Topology.ProjectRepository) in an organization of its own, then
// project.yaml pushed to it, then the project added to
// treeedit.ProjectsFile. The name is refused first when that repository
// is the platform's, a catalog's, another project's or the output's
// (Repositories.CheckProjectLocation), and a repository that exists
// already is taken only when it is empty
// (ForgejoClient.CreateRepository, ErrRepositoryNotEmpty): the repository
// of a project deleted since is kept with its content, and a repository
// someone else filled is not the project's. The repository gets no
// workflow and no secret: the output repository's workflow compiles it
// (OutputWorkflow), its token reading it.
func (d *Deployments) CreateProject(ctx context.Context, actor Actor, p treeedit.Project) (string, error) {
	edit, err := treeedit.CreateProject(p, d.split())
	if err != nil {
		return "", err
	}
	if d.split() && d.Forgejo == nil {
		return "", ErrNoForgejo
	}
	if err := d.CheckProjectName(ctx, p.Name); err != nil {
		return "", err
	}
	if d.split() {
		if _, err := view(ctx, d, func(r treeedit.Reader) (struct{}, error) { return struct{}{}, treeedit.CheckNewProject(r, p.Name) }); err != nil {
			return "", err
		}
		if repos, ok := d.Store.(*Repositories); ok {
			if err := repos.CheckProjectLocation(ctx, p.Name); err != nil {
				return "", err
			}
		}
		name, err := d.Topology.ProjectRepository(p.Name)
		if err != nil {
			return "", err
		}
		if err := d.Forgejo.CreateRepository(ctx, name, d.Topology.Projects.Branch, "OKDP project "+p.Name); err != nil {
			return "", fmt.Errorf("creating the repository of project %s: %w", p.Name, err)
		}
	}
	_, rev, err := d.Apply(ctx, actor, edit)
	return rev, err
}

// UpdateProject commits treeedit.UpdateProject.
func (d *Deployments) UpdateProject(ctx context.Context, actor Actor, p treeedit.Project) (string, error) {
	edit, err := treeedit.UpdateProject(p)
	return d.apply(ctx, actor, edit, err)
}

// DeleteProject commits treeedit.DeleteProject. In a split topology, the
// project leaves treeedit.ProjectsFile; its repository is left as it is.
func (d *Deployments) DeleteProject(ctx context.Context, actor Actor, project string) (string, error) {
	edit, err := treeedit.DeleteProject(project, d.split())
	rev, err := d.apply(ctx, actor, edit, err)
	if err == nil && d.split() {
		slog.Info("The project left "+treeedit.ProjectsFile+"; its repository is kept", "project", project)
	}
	return rev, err
}

// --- Platform ---

// ReadPlatformValues returns platform.yaml (treeedit.ReadPlatformValues).
func (d *Deployments) ReadPlatformValues(ctx context.Context) ([]byte, error) {
	return view(ctx, d, treeedit.ReadPlatformValues)
}

// ReadCatalog returns treeedit.CatalogsFile (treeedit.ReadCatalog).
func (d *Deployments) ReadCatalog(ctx context.Context) (map[string]any, error) {
	return view(ctx, d, treeedit.ReadCatalog)
}

// UpdateCatalog commits treeedit.UpdateCatalog: with a compiler, the file
// change makes refused as the compile refuses it (Compiler.CheckCatalogs,
// its menu's services those of its catalogs).
func (d *Deployments) UpdateCatalog(ctx context.Context, actor Actor, target string, change func(catalog map[string]any) error) (string, error) {
	edit := change
	if d.Compiler != nil {
		edit = func(doc map[string]any) error {
			if err := change(doc); err != nil {
				return err
			}
			return d.Compiler.CheckCatalogs(ctx, doc)
		}
	}
	return d.apply(ctx, actor, treeedit.UpdateCatalog(target, edit), nil)
}

// Catalogs opens the catalogs treeedit.CatalogsFile names, read first (the
// catalogs are fetched outside the read); none without a compiler or the
// file.
func (d *Deployments) Catalogs(ctx context.Context) (*catalog.Catalogs, error) {
	if d.Compiler == nil {
		return catalog.New(), nil
	}
	repos, err := view(ctx, d, treeedit.ReadCatalogRepositories)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return catalog.New(), nil
	}
	return d.Compiler.Catalogs.Open(ctx, repos)
}
