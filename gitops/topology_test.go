package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/okdp/okdp-gitops/treeedit"
)

func TestParseTopology(t *testing.T) {
	topo, err := ParseTopology([]byte(`
platform: {url: https://git/okdp-platform.git, path: /}
projects: {url: "https://git/okdp-project-{project}.git"}
output: {url: https://git/okdp-output.git, branch: release, path: out/}
forgejo: {url: http://forgejo:3000, owner: okdp, tokenFile: /etc/okdp/forgejo/token}
`))
	if err != nil {
		t.Fatal(err)
	}
	if !topo.Split() || topo.Platform.Branch != "main" || topo.Output.Branch != "release" || topo.Output.Path != "out" || topo.Platform.Path != "" {
		t.Errorf("topology = %+v", topo)
	}
	path := "teams/demo"
	demo, err := topo.Project("demo", &treeedit.ProjectEntry{Name: "demo", Branch: "dev", Path: &path})
	if err != nil || demo.URL != "https://git/okdp-project-demo.git" || demo.Branch != "dev" || demo.Path != "teams/demo" {
		t.Errorf("project = %+v %v", demo, err)
	}

	for name, raw := range map[string]string{
		"no output url":             "platform: {url: a}\nprojects: {url: a, path: 'projects/{project}'}\noutput: {}\n",
		"an unknown key":            "platform: {url: a}\nprojects: {url: a, path: 'projects/{project}'}\noutput: {url: o}\nengine: flux\n",
		"one repository, no dir":    "platform: {url: a}\nprojects: {url: a, path: projects}\noutput: {url: o}\n",
		"{project} in the platform": "platform: {url: 'a-{project}'}\nprojects: {url: a, path: 'projects/{project}'}\noutput: {url: o}\n",
		"a path out of the repo":    "platform: {url: a, path: ../x}\nprojects: {url: a, path: 'projects/{project}'}\noutput: {url: o}\n",
		"an incomplete forgejo":     "platform: {url: a}\nprojects: {url: 'b-{project}'}\noutput: {url: o}\nforgejo: {url: http://f}\n",
	} {
		if _, err := ParseTopology([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The output is in a repository of its own, never in one of the sources,
// whatever names it (mono, the tree and the output in one repository, is
// no topology).
func TestTheOutputIsNeverARepositoryOfTheSources(t *testing.T) {
	for name, raw := range map[string]string{
		"the three in one repository": "platform: {url: https://git/deployments.git, path: gitops}\nprojects: {url: https://git/deployments.git, path: 'gitops/projects/{project}'}\noutput: {url: https://git/deployments.git, path: gitops}\n",
		"another branch":              "platform: {url: https://git/in.git}\nprojects: {url: https://git/in.git, path: 'projects/{project}'}\noutput: {url: https://git/in.git, branch: compiled}\n",
		"the platform's, spelt apart": "platform: {url: 'https://Git/okdp/in.git/'}\nprojects: {url: 'https://git/okdp/p-{project}.git'}\noutput: {url: 'ssh://forgejo@git:2222/okdp/in'}\n",
		"the projects', by scp":       "platform: {url: https://git/okdp/platform.git}\nprojects: {url: https://bot@git/okdp/in.git, path: 'projects/{project}'}\noutput: {url: 'git@git:okdp/in.git'}\n",
		"one local directory":         "platform: {url: ./tree}\nprojects: {url: ./tree, path: 'projects/{project}'}\noutput: {url: tree/}\n",
	} {
		if _, err := ParseTopology([]byte(raw)); err == nil || !strings.Contains(err.Error(), "the output is in a repository of its own, which the CI alone writes, never in one of the sources") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseTopology([]byte("platform: {url: ./tree}\nprojects: {url: ./tree, path: 'projects/{project}'}\noutput: {url: ./tree-output}\n")); err != nil {
		t.Errorf("io of two local directories: %v", err)
	}

	// A project projects.yaml places in the output's repository is refused.
	topo := &Topology{
		Platform: Location{URL: "platform", Branch: "main"},
		Projects: Location{URL: "okdp-project-{project}", Branch: "main"},
		Output:   Location{URL: "output", Branch: "main"},
	}
	rec := newRecorder(map[string]map[string]string{
		"platform": {"projects.yaml": "projects:\n  - name: lab\n    url: output\n    path: lab\n"},
		"output":   {"lab/project.yaml": "description: Lab\n"},
	})
	d := NewDeployments(NewRepositories(topo, rec.open))
	if _, err := d.GetProject(context.Background(), "lab"); err == nil || !strings.Contains(err.Error(), "project lab: its repository output is the output's, which holds no source (projects.yaml)") {
		t.Errorf("a project in the output: %v", err)
	}
}

// The paths of each repository a compile reads: those of the layout,
// nothing beside them; the whole repository of a project whose path is
// not known yet, or is its root.
func TestTheLayoutPathsOfARepository(t *testing.T) {
	check := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	local, err := ParseTopology([]byte("platform: {url: /srv/tree}\nprojects: {url: /srv/tree, path: 'projects/{project}'}\noutput: {url: /srv/output}\n"))
	if err != nil {
		t.Fatal(err)
	}
	check("two local directories, the tree", local.LayoutPaths(local.Platform.Repository(), nil), "catalogs.yaml", "platform.yaml", "projects", "projects.yaml")
	check("two local directories, the output", local.LayoutPaths(local.Output.Repository(), nil), "argocd", "compiled", "sources.yaml")

	io, err := ParseTopology([]byte("platform: {url: https://git/in.git}\nprojects: {url: https://git/in.git, path: 'teams/{project}'}\noutput: {url: https://git/out.git, path: deploy}\n"))
	if err != nil {
		t.Fatal(err)
	}
	check("io, the sources", io.LayoutPaths(io.Platform.Repository(), nil), "catalogs.yaml", "platform.yaml", "projects.yaml", "teams")
	check("io, the output", io.LayoutPaths(io.Output.Repository(), nil), "deploy/argocd", "deploy/compiled", "deploy/sources.yaml")
	check("io, another repository", io.LayoutPaths("https://git/other.git#main", nil))

	apart, err := ParseTopology([]byte("platform: {url: https://git/platform.git}\nprojects: {url: https://git/projects.git, path: 'projects/{project}'}\noutput: {url: https://git/out.git}\n"))
	if err != nil {
		t.Fatal(err)
	}
	check("io, the platform apart", apart.LayoutPaths(apart.Platform.Repository(), nil), "catalogs.yaml", "platform.yaml", "projects.yaml")
	check("io, the projects apart", apart.LayoutPaths(apart.Projects.Repository(), nil), "projects")
	check("io, the output apart", apart.LayoutPaths(apart.Output.Repository(), nil), "argocd", "compiled", "sources.yaml")

	split, err := ParseTopology([]byte("platform: {url: https://git/platform.git}\nprojects: {url: 'https://git/project-{project}.git'}\noutput: {url: https://git/out.git}\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, platformURL := "teams/data", "https://git/platform.git"
	list := &treeedit.ProjectsList{Projects: []treeedit.ProjectEntry{{Name: "demo"}, {Name: "data", URL: platformURL, Path: &data}}}
	check("split, the platform", split.LayoutPaths(split.Platform.Repository(), list), "catalogs.yaml", "platform.yaml", "projects.yaml", "teams/data")
	check("split, the platform before its projects", split.LayoutPaths(split.Platform.Repository(), nil), "catalogs.yaml", "platform.yaml", "projects.yaml")
	check("split, a project at its root", split.LayoutPaths("https://git/project-demo.git#main", list), ".")
	check("split, a project not known yet", split.LayoutPaths("https://git/project-demo.git#main", nil), ".")
	check("split, the output", split.LayoutPaths(split.Output.Repository(), nil), "argocd", "compiled", "sources.yaml")
	check("split, a repository of no project", split.LayoutPaths("https://git/project-gone.git#main", list))
}

func TestLoadTopologyResolvesLocalDirectoriesFromTheFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "topology.yaml")
	if err := os.WriteFile(file, []byte("platform: {url: ./in}\nprojects: {url: ./in, path: 'projects/{project}'}\noutput: {url: /abs/out}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	topo, err := LoadTopology(file)
	if err != nil {
		t.Fatal(err)
	}
	if topo.Platform.URL != filepath.Join(dir, "in") || topo.Output.URL != "/abs/out" {
		t.Errorf("topology = %+v", topo)
	}
	for url, local := range map[string]bool{"./in": true, "/abs": true, "file:///x": false, "gitops": true, "https://git/x.git": false, "git@host:org/x.git": false, "ssh://git@host/x.git": false} {
		if IsLocal(url) != local {
			t.Errorf("IsLocal(%q) = %v", url, !local)
		}
	}
}

// recorder opens a MemoryStore per repository and records the commits of
// all, in order.
type recorder struct {
	mu      sync.Mutex
	stores  map[string]*MemoryStore
	commits []string
}

func newRecorder(files map[string]map[string]string) *recorder {
	r := &recorder{stores: map[string]*MemoryStore{}}
	for url, f := range files {
		r.stores[url] = MustMemoryStore(f)
	}
	return r
}

type recordedStore struct {
	*MemoryStore
	url string
	r   *recorder
}

func (s recordedStore) Update(ctx context.Context, commit Commit, fn func(Tx) error) (string, error) {
	before := len(s.Messages)
	rev, err := s.MemoryStore.Update(ctx, commit, fn)
	if err == nil && len(s.Messages) > before {
		s.r.mu.Lock()
		s.r.commits = append(s.r.commits, s.url+": "+commit.Subject)
		s.r.mu.Unlock()
	}
	return rev, err
}

func (r *recorder) open(loc Location) (Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.stores[loc.URL]
	if !ok {
		s = MustMemoryStore(nil)
		r.stores[loc.URL] = s
	}
	return recordedStore{MemoryStore: s, url: loc.URL, r: r}, nil
}

// The tree and the output in two repositories: a console change is one
// commit of the tree; the compile that follows it, one of the output.
func TestAChangeCommitsTheTreeTheCompileTheOutput(t *testing.T) {
	ctx := context.Background()
	in := readFixture(t, treeFixture)
	root := in[ArgoCDRootPath]
	delete(in, ArgoCDRootPath)
	rec := newRecorder(map[string]map[string]string{"in": in, "out": {"deploy/" + ArgoCDRootPath: root}})
	topo := &Topology{
		Platform: Location{URL: "in", Branch: "main"},
		Projects: Location{URL: "in", Branch: "main", Path: "projects/{project}"},
		Output:   Location{URL: "out", Branch: "main", Path: "deploy"},
	}
	d := NewDeployments(NewRepositories(topo, rec.open))
	if d.Topology != topo || d.OutputPrefix != "deploy" {
		t.Fatalf("deployments of the topology: %+v", d)
	}
	d.Compiler = testCompiler()

	web := treeedit.Instance{Project: "demo", Name: "web", Service: "app", Version: "1.0.0", Values: map[string]any{"database": "analytics"}}
	if _, err := d.CreateInstance(ctx, alice, web); err != nil {
		t.Fatal(err)
	}
	if want := "in: okdp: deploy demo/web by alice"; strings.Join(rec.commits, "\n") != want {
		t.Fatalf("commits:\n%s", strings.Join(rec.commits, "\n"))
	}
	if got := rec.stores["in"].Files()["projects/demo/instances/web/config.yaml"]; got != "database: analytics\n" {
		t.Errorf("config.yaml = %q", got)
	}
	if _, err := d.ReadCompiled(ctx, "demo", "web"); !errors.Is(err, treeedit.ErrNotFound) {
		t.Fatalf("compiled before the compile: %v", err)
	}

	rec.commits = nil
	compileAll(t, d)
	if want := "out: okdp: compile"; strings.Join(rec.commits, "\n") != want {
		t.Fatalf("commits:\n%s", strings.Join(rec.commits, "\n"))
	}
	out := rec.stores["out"].Files()
	for _, p := range []string{"compiled/projects/demo/instances/demo-web/instance.values.yaml", "compiled/projects/demo/instances/demo-web/helmrelease.yaml", "compiled/projects/demo/instances/demo-web/application.yaml", "compiled/projects/demo/kustomization.yaml"} {
		if _, ok := out["deploy/"+p]; !ok {
			t.Errorf("deploy/%s missing from the output: %v", p, out)
		}
		if _, ok := rec.stores["in"].Files()[p]; ok {
			t.Errorf("%s written to the tree", p)
		}
	}
	if app := out["deploy/compiled/projects/demo/instances/demo-web/application.yaml"]; !strings.Contains(app, "    path: \"deploy/compiled/projects/demo/instances/demo-web\"\n") {
		t.Errorf("application.yaml:\n%s", app)
	}

	// Reads go to the repository of each part.
	compiled, err := d.ReadCompiled(ctx, "demo", "web")
	if err != nil || compiled.Values["app"] == nil {
		t.Fatalf("compiled: %+v %v", compiled, err)
	}

	// A delete: the instance's directory, then, compiled, its output.
	rec.commits = nil
	if _, err := d.DeleteInstance(ctx, alice, "demo", "web"); err != nil {
		t.Fatal(err)
	}
	compileAll(t, d)
	for p := range rec.stores["out"].Files() {
		if strings.Contains(p, "/demo-web/") {
			t.Errorf("%s survived", p)
		}
	}
	want := []string{"in: okdp: delete demo/web by alice", "out: okdp: compile"}
	if strings.Join(rec.commits, "\n") != strings.Join(want, "\n") {
		t.Errorf("commits:\n%s", strings.Join(rec.commits, "\n"))
	}
}

// io, the platform apart: a write of a project goes to the projects'
// repository only, the team's; the platform's is not written.
func TestAProjectOfIOThePlatformApartIsWrittenToTheProjectsRepository(t *testing.T) {
	ctx := context.Background()
	platform, projects := map[string]string{}, map[string]string{}
	for p, c := range readFixture(t, treeFixture) {
		switch {
		case strings.HasPrefix(p, "projects/"):
			projects[p] = c
		case p != ArgoCDRootPath:
			platform[p] = c
		}
	}
	rec := newRecorder(map[string]map[string]string{"platform": platform, "projects": projects})
	topo := &Topology{
		Platform: Location{URL: "platform", Branch: "main"},
		Projects: Location{URL: "projects", Branch: "main", Path: "projects/{project}"},
		Output:   Location{URL: "out", Branch: "main"},
	}
	d := NewDeployments(NewRepositories(topo, rec.open))
	d.Compiler = testCompiler()
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "lab"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateInstance(ctx, alice, treeedit.Instance{Project: "lab", Name: "web", Service: "app", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"projects: okdp: create project lab by alice", "projects: okdp: deploy lab/web by alice"}
	if strings.Join(rec.commits, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commits:\n%s", strings.Join(rec.commits, "\n"))
	}
	if _, ok := rec.stores["projects"].Files()["projects/lab/instances/web/instance.yaml"]; !ok {
		t.Errorf("the instance is not in the projects' repository: %v", rec.stores["projects"].Files())
	}
}

func splitTopology(forgejo *ForgejoConfig) *Topology {
	return &Topology{
		Platform: Location{URL: "platform", Branch: "main"},
		Projects: Location{URL: "okdp-project-{project}", Branch: "main"},
		Output:   Location{URL: "output", Branch: "main"},
		Forgejo:  forgejo,
	}
}

// A repository per project: creating one creates its repository (Forgejo),
// pushes project.yaml to it, then lists it in projects.yaml.
func TestAProjectOfASplitTopologyHasARepositoryOfItsOwn(t *testing.T) {
	ctx := context.Background()
	var created []map[string]any
	var auth string
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/orgs/okdp/repos" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		created = append(created, body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer forgejo.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: tokenFile}

	rec := newRecorder(nil)
	d := NewDeployments(NewRepositories(splitTopology(&config), rec.open))
	d.Forgejo = NewForgejoClient(config, localhostHTTP(t))
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "demo", Description: "Demo"}); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0]["name"] != "okdp-project-demo" || created[0]["default_branch"] != "main" || created[0]["description"] != "OKDP project demo" || auth != "token s3cr3t" {
		t.Fatalf("forgejo: %v %q", created, auth)
	}
	want := []string{"okdp-project-demo: okdp: create project demo by alice", "platform: okdp: create project demo by alice"}
	if strings.Join(rec.commits, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commits:\n%s", strings.Join(rec.commits, "\n"))
	}
	if got := rec.stores["okdp-project-demo"].Files()["project.yaml"]; got != "description: Demo\n" {
		t.Errorf("project.yaml = %q", got)
	}
	if got, ok := rec.stores["okdp-project-demo"].Files()[WorkflowPath]; ok {
		t.Errorf("a workflow in a repository of the sources: %s", got)
	}
	if got := rec.stores["platform"].Files()["projects.yaml"]; got != "projects:\n  - name: demo\n" {
		t.Errorf("projects.yaml = %q", got)
	}
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "demo"}); !errors.Is(err, treeedit.ErrExists) {
		t.Fatalf("second create: %v", err)
	}

	projects, err := d.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Description != "Demo" {
		t.Fatalf("projects: %v %v", projects, err)
	}

	// An instance of the project: in its repository.
	if _, err := d.CreateInstance(ctx, alice, treeedit.Instance{Project: "demo", Name: "web", Service: "app", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.stores["okdp-project-demo"].Files()["instances/web/instance.yaml"]; !ok {
		t.Errorf("the instance is not in the project's repository: %v", rec.stores["okdp-project-demo"].Files())
	}

	// Deleted: the project leaves projects.yaml, its repository stays.
	if _, err := d.DeleteProject(ctx, alice, "demo"); err != nil {
		t.Fatal(err)
	}
	if got := rec.stores["platform"].Files()["projects.yaml"]; got != "projects: []\n" {
		t.Errorf("projects.yaml = %q", got)
	}
	if out, opened := rec.stores["output"]; opened && len(out.Files()) != 0 {
		t.Errorf("the project's writes wrote the output: %v", out.Files())
	}
	if _, ok := rec.stores["okdp-project-demo"].Files()["project.yaml"]; !ok {
		t.Error("the project's repository was emptied")
	}
	if projects, _ := d.ListProjects(ctx); len(projects) != 0 {
		t.Errorf("projects: %v", projects)
	}

	// Of HTTPS repositories, which a job could reach: no workflow either,
	// the sources hold none (the output's compiles them).
	https := &Topology{
		Platform: Location{URL: "https://git.example.org/okdp/okdp-platform.git", Branch: "main"},
		Projects: Location{URL: "https://git.example.org/okdp/okdp-project-{project}.git", Branch: "main"},
		Output:   Location{URL: "https://git.example.org/okdp/okdp-output.git", Branch: "main"},
		Forgejo:  &config,
	}
	rec = newRecorder(nil)
	d = NewDeployments(NewRepositories(https, rec.open))
	d.Forgejo = NewForgejoClient(config, localhostHTTP(t))
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "lab"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.stores["https://git.example.org/okdp/okdp-project-lab.git"].Files(); len(got) != 1 || got["project.yaml"] == "" {
		t.Errorf("the repository of the project holds project.yaml only: %v", got)
	}
}

// A name the compile refuses creates no repository of a split topology:
// the check goes before Forgejo.
func TestASplitTopologyCreatesNoRepositoryForAReservedName(t *testing.T) {
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forgejo called: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer forgejo.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: tokenFile}
	rec := newRecorder(map[string]map[string]string{"platform": {"platform.yaml": "global:\n  okdp:\n    policy:\n      reserved: [openbao]\n"}})
	d := NewDeployments(NewRepositories(splitTopology(&config), rec.open))
	d.Forgejo = NewForgejoClient(config, localhostHTTP(t))
	for _, name := range []string{"kube-foo", "openbao"} {
		var compileErr *treeedit.CompileError
		if _, err := d.CreateProject(context.Background(), alice, treeedit.Project{Name: name}); !errors.As(err, &compileErr) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(rec.commits) != 0 {
		t.Errorf("commits: %v", rec.commits)
	}
}

// localhostHTTP allows a credential over http:// to the test servers.
func localhostHTTP(t *testing.T) *Credentials {
	t.Helper()
	return newCredentials(t, CredentialsConfig{InsecureHTTPHosts: []string{"127.0.0.1"}})
}

// The Forgejo token goes over http:// only to the hosts the credentials
// allow: refused before it is read, nothing created.
func TestForgejoRefusesItsTokenOverHTTP(t *testing.T) {
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forgejo called: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer forgejo.Close()
	config := ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: filepath.Join(t.TempDir(), "token")}
	for _, creds := range []*Credentials{nil, newCredentials(t, CredentialsConfig{InsecureHTTPHosts: []string{"forgejo.local"}})} {
		err := NewForgejoClient(config, creds).CreateRepository(context.Background(), "okdp-project-demo", "main", "")
		if err == nil || !strings.Contains(err.Error(), "forgejo: refusing to send the credentials of "+forgejo.URL+" over http://") {
			t.Errorf("err = %v", err)
		}
	}
}

func TestASplitTopologyWithoutForgejoCannotCreateAProject(t *testing.T) {
	d := NewDeployments(NewRepositories(splitTopology(nil), newRecorder(nil).open))
	if _, err := d.CreateProject(context.Background(), alice, treeedit.Project{Name: "demo"}); !errors.Is(err, ErrNoForgejo) {
		t.Fatalf("err = %v", err)
	}
}

// The repository Forgejo creates for a new project is the one the
// projects url names, whatever its template.
func TestTheRepositoryOfANewProjectIsTheTemplates(t *testing.T) {
	for url, want := range map[string]string{
		"https://git.example.org/okdp/okdp-project-{project}.git": "okdp-project-lab",
		"https://git.example.org/okdp/team-{project}":             "team-lab",
		"git@git.example.org:okdp/{project}-sources.git":          "lab-sources",
		"git@git.example.org:{project}.git":                       "lab",
		"./okdp-project-{project}":                                "okdp-project-lab",
	} {
		topo := &Topology{Projects: Location{URL: url}}
		if got, err := topo.ProjectRepository("lab"); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", url, got, err, want)
		}
	}
	if got, err := (&Topology{Projects: Location{URL: "https://git.example.org/"}}).ProjectRepository("lab"); err == nil {
		t.Errorf("no repository: %q", got)
	}
}

// An entry of projects.yaml, the platform repository's content, stays in
// the topology: in a topology of Git repositories, no local directory nor
// file:// repository, the hosts of the templates only, the credentials
// going to those; a branch Git accepts, a relative path without "..". Its
// project's reads then fail with the error, nothing read elsewhere.
func TestAnEntryOfProjectsYAMLStaysInTheTopology(t *testing.T) {
	topo := &Topology{
		Platform: Location{URL: "https://git.example.org/okdp/okdp-platform.git", Branch: "main"},
		Projects: Location{URL: "https://git.example.org/okdp/okdp-project-{project}.git", Branch: "main"},
		Output:   Location{URL: "ssh://git@deploy.example.org/okdp/okdp-output.git", Branch: "main"},
	}
	str := func(s string) *string { return &s }
	for _, ok := range []treeedit.ProjectEntry{
		{URL: "https://git.example.org/data/lab.git"},
		{URL: "https://GIT.example.org:8443/data/lab.git"},
		{URL: "git@git.example.org:data/lab.git"},
		{URL: "ssh://git@deploy.example.org/data/lab.git"},
		{Branch: "okdp/lab"},
		{Path: str("deployments/")},
		{Path: str("")},
		{Path: str("teams/./lab")},
	} {
		entry := ok
		entry.Name = "lab"
		if _, err := topo.Project("lab", &entry); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for want, entry := range map[string]treeedit.ProjectEntry{
		`project lab: projects.yaml: url "/srv/lab": a local directory or a file:// repository, in a topology whose projects are Git repositories`: {URL: "/srv/lab"},
		`url "./lab": a local directory`:               {URL: "./lab"},
		`url "file:///srv/lab.git": a local directory`: {URL: "file:///srv/lab.git"},
		`url "https://evil.example/lab.git": its host evil.example is not one of the topology's (git.example.org, deploy.example.org)`: {URL: "https://evil.example/lab.git"},
		`url "git@git.example.org.evil.example:lab.git": its host`:                                                                     {URL: "git@git.example.org.evil.example:lab.git"},
		`url "http://tenant.lab.svc.cluster.local/lab.git": its host`:                                                                  {URL: "http://tenant.lab.svc.cluster.local/lab.git"},
		`branch "a..b" is not a branch name`:                                                                                           {Branch: "a..b"},
		`branch "-f" is not a branch name`:                                                                                             {Branch: "-f"},
		`path "../outside" goes up (..)`:                                                                                               {Path: str("../outside")},
		`path "a/../../outside" goes up (..)`:                                                                                          {Path: str("a/../../outside")},
		`path "a/../b" goes up (..)`:                                                                                                   {Path: str("a/../b")},
		`path "/etc" is absolute`:                                                                                                      {Path: str("/etc")},
	} {
		entry.Name = "lab"
		if _, err := topo.Project("lab", &entry); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v, want %q", entry, err, want)
		}
	}

	// A topology of local directories, the operator's: an entry may name
	// another, never a path that goes up.
	local := splitTopology(nil)
	if loc, err := local.Project("lab", &treeedit.ProjectEntry{Name: "lab", URL: "/srv/lab"}); err != nil || loc.URL != "/srv/lab" {
		t.Errorf("a local entry of a local topology: %+v %v", loc, err)
	}
	if _, err := local.Project("lab", &treeedit.ProjectEntry{Name: "lab", URL: "https://git.example.org/lab.git"}); err == nil {
		t.Error("a host of no template: accepted")
	}
	if _, err := local.Project("lab", &treeedit.ProjectEntry{Name: "lab", Path: str("../outside")}); err == nil {
		t.Error("a path that goes up: accepted")
	}

	// Its project's reads fail, and no layout path names it.
	rec := newRecorder(map[string]map[string]string{"platform": {"projects.yaml": "projects:\n  - {name: lab, path: ../outside}\n  - {name: demo}\n"}})
	repos := NewRepositories(local, rec.open)
	err := repos.View(context.Background(), func(r treeedit.Reader) error {
		_, err := r.ReadFile("projects/lab/project.yaml")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), `project lab: projects.yaml: path "../outside" goes up (..)`) {
		t.Errorf("a read of the project: %v", err)
	}
	list := &treeedit.ProjectsList{Projects: []treeedit.ProjectEntry{{Name: "lab", URL: "okdp-project-demo", Path: str("../outside")}}}
	if got := local.LayoutPaths(Location{URL: "okdp-project-demo", Branch: "main"}.Repository(), list); len(got) != 0 {
		t.Errorf("layout paths: %v", got)
	}
}

// A store of files read from a repository refuses a path out of it, an
// error, not a panic.
func TestNewMemoryStoreRefusesAPathOutOfTheStore(t *testing.T) {
	for _, p := range []string{"../outside/project.yaml", "/etc/passwd"} {
		if _, err := NewMemoryStore(map[string]string{p: "x"}); err == nil {
			t.Errorf("%s: accepted", p)
		}
	}
}

// In a split topology a project's repository is the project's: a name the
// template maps to the platform's repository, to a catalog's or to another
// project's (an entry of projects.yaml may name any repository of the
// hosts) is refused before anything is created, and the reads of a
// project placed there fail.
func TestAProjectCannotTakeTheRepositoryOfAnotherPart(t *testing.T) {
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forgejo called: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer forgejo.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: tokenFile}
	topo := &Topology{
		Platform: Location{URL: "okdp-core", Branch: "main"},
		Projects: Location{URL: "okdp-{project}", Branch: "main"},
		Output:   Location{URL: "okdp-out", Branch: "main"},
		Forgejo:  &config,
	}
	rec := newRecorder(map[string]map[string]string{
		"okdp-core": {
			"catalogs.yaml": "repositories:\n  - {name: charts, url: okdp-charts, ref: main}\n",
			"projects.yaml": "projects:\n  - name: first\n    url: okdp-shared\n  - name: second\n    url: okdp-shared\n",
		},
		"okdp-shared": {"project.yaml": "description: shared\n"},
	})
	d := NewDeployments(NewRepositories(topo, rec.open))
	d.Forgejo = NewForgejoClient(config, localhostHTTP(t))

	for name, want := range map[string]string{
		"core":   "project core: its repository okdp-core is the platform's",
		"charts": "project charts: its repository okdp-charts is the catalog charts's (catalogs.yaml)",
		"shared": "project shared: its repository okdp-shared is project first's (projects.yaml)",
		"out":    "project out: its repository okdp-out is the output's",
	} {
		if _, err := d.CreateProject(context.Background(), alice, treeedit.Project{Name: name}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	if len(rec.commits) != 0 {
		t.Errorf("commits: %v", rec.commits)
	}

	// Listed after the first, the second takes nothing of it; the first,
	// which was there before, keeps its repository.
	if _, err := d.GetProject(context.Background(), "second"); err == nil || !strings.Contains(err.Error(), "project second: its repository okdp-shared is project first's") {
		t.Errorf("second: %v", err)
	}
	if p, err := d.GetProject(context.Background(), "first"); err != nil || p.Description != "shared" {
		t.Errorf("first: %v, %v", p, err)
	}
}

// A repository Forgejo has already is taken as the new project's when it
// is empty (a creation that failed halfway) and refused when it holds
// commits: the repository of a project deleted since, or someone else's.
func TestAnExistingRepositoryIsTakenWhenEmptyOnly(t *testing.T) {
	ctx := context.Background()
	empty := true
	var posts, gets int
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/okdp/repos":
			posts++
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/okdp/okdp-project-demo":
			gets++
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "okdp-project-demo", "empty": empty})
		default:
			http.NotFound(w, r)
		}
	}))
	defer forgejo.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: tokenFile}

	rec := newRecorder(nil)
	d := NewDeployments(NewRepositories(splitTopology(&config), rec.open))
	d.Forgejo = NewForgejoClient(config, localhostHTTP(t))
	empty = false
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "demo"}); !errors.Is(err, ErrRepositoryNotEmpty) || !strings.Contains(err.Error(), "okdp/okdp-project-demo") {
		t.Fatalf("a repository with commits: %v", err)
	}
	if len(rec.commits) != 0 {
		t.Errorf("commits: %v", rec.commits)
	}
	empty = true
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "demo"}); err != nil {
		t.Fatalf("an empty repository: %v", err)
	}
	if posts != 2 || gets != 2 || len(rec.commits) != 2 {
		t.Errorf("%d creations, %d reads, commits %v", posts, gets, rec.commits)
	}
}

// The Forgejo token goes where the topology says: a redirect to another
// scheme or host is refused, the token not sent there; one on the same
// origin is followed.
func TestForgejoDoesNotFollowARedirectToAnotherOrigin(t *testing.T) {
	ctx := context.Background()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("another origin reached: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer other.Close()
	var moved bool
	var forgejo *httptest.Server
	forgejo = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orgs/okdp/repos":
			http.Redirect(w, r, other.URL+"/api/v1/orgs/okdp/repos", http.StatusTemporaryRedirect)
		case "/api/v1/orgs/stay/repos":
			http.Redirect(w, r, forgejo.URL+"/moved", http.StatusTemporaryRedirect)
		case "/moved":
			moved = r.Header.Get("Authorization") == "token s3cr3t"
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer forgejo.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := newCredentials(t, CredentialsConfig{InsecureHTTPHosts: []string{"127.0.0.1"}})
	err := NewForgejoClient(ForgejoConfig{URL: forgejo.URL, Owner: "okdp", TokenFile: tokenFile}, creds).CreateRepository(ctx, "p", "main", "")
	if err == nil || !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Errorf("a redirect to another origin: %v", err)
	}
	if err := NewForgejoClient(ForgejoConfig{URL: forgejo.URL, Owner: "stay", TokenFile: tokenFile}, creds).CreateRepository(ctx, "p", "main", ""); err != nil || !moved {
		t.Errorf("a redirect on the same origin: %v (token kept: %v)", err, moved)
	}
}
