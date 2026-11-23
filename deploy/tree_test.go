package deploy_test

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/deploy"
	"github.com/okdp/okdp-gitops/internal/helmvalues"
	"github.com/okdp/okdp-gitops/rules"
)

// TestConcurrentCompile: the concurrent use the documentation promises,
// one Definition and the same values layers shared by goroutines, in the
// dcr mode of the example's platform (the OAuth client chart added to a copy
// of the Definition's charts) and the existing mode, a layer after it: each
// compile gives what it gives alone, and the inputs are not changed.
// Meaningful under go test -race.
// TestTreeNames: a tree written with the names the package exports, read
// by LoadRepository: its instance is the release ReleaseName of its
// project and directory, in the project's namespace, and a target writes
// it in InstanceDir, under ProjectDir.
func TestTreeNames(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	instance := path.Join(tree.ProjectsDir, "demo", tree.InstancesDir, "web")
	src := fstest.MapFS{
		tree.PlatformFile: file("global: {okdp: {}}\n"),
		path.Join(tree.ContractsDir, "http.schema.json"):         file(`{"properties": {"url": {"type": "string"}}}`),
		path.Join(tree.CatalogDir, "web", "1.0.0", "Chart.yaml"): file("apiVersion: v2\nname: web\nversion: 1.0.0\n"),
		path.Join(tree.ProjectsDir, "demo", tree.ProjectFile):    file("connections: {api: {contract: http, url: https://api.example.org}}\n"),
		path.Join(instance, tree.InstanceFile):                   file("service: web\nversion: 1.0.0\n"),
		path.Join(instance, tree.ConnectionsFile):                file("connections: {own: {contract: http, url: https://own.example.org}}\n"),
		path.Join(instance, tree.ConfigFile):                     file("{}\n"),
	}
	instances, err := loadRepository([]tree.Repository{{Name: "db", FS: src, Role: tree.RoleTree}})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || len(instances[0].Inputs.Values) != 4 {
		t.Fatalf("got %d instances, want one with its 4 values layers: the platform's, the project's, its connections and its values", len(instances))
	}
	in := instances[0].Inputs
	if want := tree.ReleaseName("demo", "web"); in.Release != want || want != "demo-web" || in.Namespace != "demo" {
		t.Errorf("release %s/%s, want demo/%s", in.Namespace, in.Release, want)
	}
	res, err := compiler.Compile(t.Context(), instances[0].Definition, in)
	if err != nil {
		t.Fatal(err)
	}
	g, err := tree.NewGraph([]*compiler.Result{res})
	if err != nil {
		t.Fatal(err)
	}
	files, err := deploy.Umbrella{}.Files([]*compiler.Result{res}, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := deploy.InstanceDir("demo", in.Release)
	if _, ok := files[path.Join(dir, "Chart.yaml")]; !ok || dir != path.Join(deploy.ProjectDir("demo"), "instances", "demo-web") || deploy.ProjectDir("demo") != "projects/demo" {
		t.Errorf("files %q, want the umbrella chart in %s", slices.Sorted(maps.Keys(files)), dir)
	}
}

// charts: a ChartSource that records the charts it is asked for, gives
// fsys, and fails each Get with err.
type charts struct {
	mu   sync.Mutex
	refs []compiler.ChartRef
	fsys fs.FS
	err  error
}

func (c *charts) Get(_ context.Context, ref compiler.ChartRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refs = append(c.refs, ref)
	return c.err
}

func (c *charts) FS() fs.FS { return c.fsys }

// loadRepository: tree.Load with the standard rule set, each
// instance with it and Helm's merge of the final values.
func loadRepository(repos []tree.Repository) ([]compiler.Instance, error) {
	instances, err := tree.Load(context.Background(), repos, rules.Standard())
	for i := range instances {
		instances[i].Inputs.Rules, instances[i].Inputs.Finalize = rules.Standard(), helmvalues.New(nil)
	}
	return instances, err
}

// TestBuildCharts: the charts of the tree's Chart.yaml fetched once each,
// before the compile, then the values checked against them; unchecked, and
// why, when one cannot be fetched.
func TestBuildCharts(t *testing.T) {
	repos := []tree.Repository{{Name: "db", FS: os.DirFS("../examples/04_claims/input/db"), Role: tree.RoleTree}}
	away := &charts{err: errors.New("the registry is away")}
	failed := deploy.NewCharts(away)
	built, err := deploy.Build(t.Context(), repos, deploy.BuildOptions{Charts: failed})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Failures()) == 0 || built.Problems() != nil || built.Graph == nil {
		t.Errorf("unchecked: failures %v, problems %q", failed.Failures(), built.Problems())
	}
	for ref, err := range failed.Failures() {
		if !errors.Is(err, away.err) {
			t.Errorf("chart %v: %v", ref, err)
		}
	}
	if len(away.refs) == 0 || len(slices.Compact(slices.Clone(away.refs))) != len(away.refs) {
		t.Errorf("refs: %v, want each once", away.refs)
	}
	empty := &charts{fsys: fstest.MapFS{}}
	checked := deploy.NewCharts(empty)
	built, err = deploy.Build(t.Context(), repos, deploy.BuildOptions{Charts: checked})
	if err != nil {
		t.Fatal(err)
	}
	if checked.Failures() != nil || built.Problems() == nil || !strings.Contains(strings.Join(built.Problems(), "\n"), "is not in the chart cache: helm pull ") {
		t.Errorf("checked against a cache without the charts: failures %v, problems %q", checked.Failures(), built.Problems())
	}
	if !reflect.DeepEqual(empty.refs, away.refs) {
		t.Errorf("refs: %v, then %v", away.refs, empty.refs)
	}
}

// TestAccess: project.yaml's access, read from the project's file alone,
// written by the targets as the Argo CD roles of its AppProject and the
// RoleBindings of its namespace, the platform's group prefix on the
// latter; the accessors a control plane mirrors them with.
func TestAccess(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	platform := "global: {okdp: {access: {admins: [platform_admin], groupPrefix: 'oidc:'}}}\n"
	project := "description: the demo\naccess:\n  okdp-project-demo: admin\n  analysts: viewer\n"
	src := fstest.MapFS{
		tree.PlatformFile: file(platform),
		path.Join(tree.CatalogDir, "web", "1.0.0", "Chart.yaml"):                       file("apiVersion: v2\nname: web\nversion: 1.0.0\n"),
		path.Join(tree.ProjectsDir, "demo", tree.ProjectFile):                          file(project),
		path.Join(tree.ProjectsDir, "demo", tree.InstancesDir, "web", "instance.yaml"): file("service: web\nversion: 1.0.0\n"),
		path.Join(tree.ProjectsDir, "ops", tree.InstancesDir, "web", "instance.yaml"):  file("service: web\nversion: 1.0.0\n"),
	}
	tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: "db", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if problems := tr.Problems(); problems != nil {
		t.Fatal(problems)
	}
	files, _, err := deploy.TreeFiles(tr, []deploy.Target{deploy.ArgoCD{RepoURL: "https://git.example.org/o.git"}, deploy.Flux{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	demo := string(files[path.Join(deploy.ProjectManifestsDir("demo"), "access.yaml")])
	app := string(files[path.Join(deploy.ProjectManifestsDir("demo"), "appproject.yaml")])
	ops := string(files[path.Join(deploy.ProjectManifestsDir("ops"), "access.yaml")])
	for _, want := range []string{"name: okdp-access-viewer", `name: "oidc:analysts"`, "name: okdp-access-admin", `name: "oidc:okdp-project-demo"`} {
		if !strings.Contains(demo, want) {
			t.Errorf("demo's access.yaml lacks %q:\n%s", want, demo)
		}
	}
	if !strings.Contains(app, "p, proj:project-demo:admin, applications, override, project-demo/*, allow") || !strings.Contains(app, `- "okdp-project-demo"`) || strings.Contains(app, "oidc:") {
		t.Errorf("demo's AppProject:\n%s", app)
	}
	if !strings.Contains(ops, "name: okdp-access-editor") || !strings.Contains(ops, `name: "oidc:okdp-project-ops"`) {
		t.Errorf("ops's access.yaml, the default:\n%s", ops)
	}
	for p, b := range files {
		if strings.Contains(p, "/instances/") && strings.Contains(string(b), "analysts") {
			t.Errorf("%s holds the access", p)
		}
	}
	got, err := deploy.ProjectAccess(nil, []byte(project), "demo")
	if want := map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessAdmin, "analysts": compiler.AccessViewer}; err != nil || !maps.Equal(got, want) {
		t.Errorf("ProjectAccess: %v, %v", got, err)
	}
	if got, err := deploy.ProjectAccess(nil, []byte("description: no access\n"), "ops"); err != nil || !maps.Equal(got, map[string]compiler.AccessRole{"okdp-project-ops": compiler.AccessEditor}) {
		t.Errorf("ProjectAccess, the default: %v, %v", got, err)
	}
	if _, err := deploy.ProjectAccess(nil, []byte("access: {a/b: viewer}\n"), "demo"); err == nil || !strings.Contains(err.Error(), "access.a/b: a group name") {
		t.Errorf("ProjectAccess, a refused group: %v", err)
	}
	if tree.DefaultProjectGroup("demo") != "okdp-project-demo" || !compiler.IsGroupName("platform_admin") || compiler.IsGroupName("/platform_admin") {
		t.Error("DefaultProjectGroup, IsGroupName")
	}
}

// TestPartialRederives: a project kept (an instance of it refused) keeps
// its instances as the previous output holds them, but its platform's
// part is today's (D-5, CMP-07): a privilege revoked, its Namespace
// baseline, its identity confined again, its kept HelmRelease impersonating
// it, its AppProject confined; a role taken back, its RoleBinding gone.
func TestPartialRederives(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	src := fstest.MapFS{
		tree.PlatformFile:                        file("global: {okdp: {policy: {privileged: [ops]}}}\n"),
		"catalog/admin/1.0.0/Chart.yaml":         file("apiVersion: v2\nname: admin\nversion: 1.0.0\ndependencies:\n  - {name: hello, alias: app, repository: https://charts.example.org, version: 1.0.0}\n"),
		"catalog/admin/1.0.0/okdp.yaml":          file("objects:\n  - {apiVersion: rbac.authorization.k8s.io/v1, kind: ClusterRoleBinding, metadata: {name: ops-admin}, roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}, subjects: [{kind: ServiceAccount, name: default, namespace: ops}]}\n"),
		"catalog/admin/1.0.0/config.schema.json": file(`{"type": "object", "properties": {"n": {"type": "integer"}}}`),
		"projects/ops/project.yaml":              file("access: {ops-team: admin}\n"),
		"projects/ops/instances/x/instance.yaml": file("service: admin\nversion: 1.0.0\n"),
	}
	targets := []deploy.Target{deploy.Flux{}, deploy.ArgoCD{RepoURL: "https://git.example.org/out.git"}}
	build := func() *tree.Tree {
		tr, err := deploy.Build(t.Context(), []tree.Repository{{Name: "db", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	first, _, err := deploy.TreeFiles(build(), targets, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifests := deploy.ProjectManifestsDir("ops")
	hr := path.Join(deploy.InstanceDir("ops", "ops-x"), "helmrelease.yaml")
	if ns := string(first[path.Join(manifests, "namespace.yaml")]); !strings.Contains(ns, "enforce: privileged") || strings.Contains(string(first[hr]), "serviceAccountName") {
		t.Fatalf("before: privileged expected:\n%s", ns)
	}
	previous := fstest.MapFS{}
	for p, b := range first {
		previous[p] = &fstest.MapFile{Data: b}
	}
	// The platform revokes the privilege, the project takes its role back;
	// an instance of it is refused, which keeps the project.
	src[tree.PlatformFile] = file("global: {okdp: {policy: {privileged: []}}}\n")
	src["projects/ops/project.yaml"] = file("access: {}\n")
	src["projects/ops/instances/broken/instance.yaml"] = file("service: admin\nversion: 1.0.0\n")
	src["projects/ops/instances/broken/config.yaml"] = file("n: notanumber\n")
	second, kept, err := deploy.TreeFiles(build(), targets, previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Namespace != "ops" {
		t.Fatalf("kept: %+v", kept)
	}
	ns := string(second[path.Join(manifests, "namespace.yaml")])
	app := string(second[path.Join(manifests, "appproject.yaml")])
	switch {
	case !strings.Contains(ns, "enforce: baseline") || strings.Contains(ns, "privileged"):
		t.Errorf("the Namespace, the privilege revoked:\n%s", ns)
	case second[path.Join(manifests, "service-account.yaml")] == nil:
		t.Error("no confined identity")
	case !strings.Contains(string(second[hr]), `serviceAccountName: "project-ops"`):
		t.Errorf("the kept HelmRelease, not confined:\n%s", second[hr])
	case !strings.Contains(app, "clusterResourceWhitelist: []") || strings.Contains(app, `namespace: "*"`) || strings.Contains(app, "roles:"):
		t.Errorf("the AppProject, not confined:\n%s", app)
	case second[path.Join(manifests, "access.yaml")] != nil:
		t.Errorf("a role taken back kept:\n%s", second[path.Join(manifests, "access.yaml")])
	}
	// The instance's own files as they were: its objects, refused now by
	// the identity that applies them.
	objects := path.Join(deploy.InstanceDir("ops", "ops-x"), deploy.UmbrellaValues)
	if string(second[objects]) != string(first[objects]) || !strings.Contains(string(second[objects]), "ClusterRoleBinding") {
		t.Errorf("the kept instance's objects changed:\n%s", second[objects])
	}
}
