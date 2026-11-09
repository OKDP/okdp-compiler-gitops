package deploy_test

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
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
	instance := path.Join(compiler.ProjectsDir, "demo", compiler.InstancesDir, "web")
	tree := fstest.MapFS{
		compiler.PlatformFile:                                         file("global: {okdp: {}}\n"),
		path.Join(compiler.ContractsDir, "http.schema.json"):          file(`{"properties": {"url": {"type": "string"}}}`),
		path.Join(compiler.CatalogDir, "web", "1.0.0", "Chart.yaml"):  file("apiVersion: v2\nname: web\nversion: 1.0.0\n"),
		path.Join(compiler.ProjectsDir, "demo", compiler.ProjectFile): file("connections: {api: {contract: http, url: https://api.example.org}}\n"),
		path.Join(instance, compiler.InstanceFile):                    file("service: web\nversion: 1.0.0\n"),
		path.Join(instance, compiler.ConnectionsFile):                 file("connections: {own: {contract: http, url: https://own.example.org}}\n"),
		path.Join(instance, compiler.ConfigFile):                      file("{}\n"),
	}
	instances, err := loadRepository([]compiler.Repository{{Name: "db", FS: tree, Role: compiler.RoleTree}})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || len(instances[0].Inputs.Context) != 3 || len(instances[0].Inputs.Values) != 1 {
		t.Fatalf("got %d instances, want one with its 3 context layers and its values", len(instances))
	}
	in := instances[0].Inputs
	if want := compiler.ReleaseName("demo", "web"); in.Release != want || want != "demo-web" || in.Namespace != "demo" {
		t.Errorf("release %s/%s, want demo/%s", in.Namespace, in.Release, want)
	}
	res, err := compiler.Compile(t.Context(), instances[0].Definition, in)
	if err != nil {
		t.Fatal(err)
	}
	g, err := compiler.NewGraph([]*compiler.Result{res})
	if err != nil {
		t.Fatal(err)
	}
	files, err := deploy.Umbrella{}.Files([]*compiler.Result{res}, g)
	if err != nil {
		t.Fatal(err)
	}
	dir := deploy.InstanceDir("demo", in.Release)
	if _, ok := files[path.Join(dir, "Chart.yaml")]; !ok || dir != path.Join(deploy.ProjectDir("demo"), "instances", "demo-web") || deploy.ProjectDir("demo") != "projects/demo" {
		t.Errorf("files %q, want the umbrella chart in %s", slices.Sorted(maps.Keys(files)), dir)
	}
}

// charts: a ChartSource that records what it is asked, and answers fsys,
// or err.
type charts struct {
	refs []compiler.ChartRef
	fsys fs.FS
	err  error
}

func (c *charts) Ensure(_ context.Context, refs []compiler.ChartRef) (fs.FS, map[compiler.ChartRef]error) {
	c.refs = refs
	if c.err == nil {
		return c.fsys, nil
	}
	failures := map[compiler.ChartRef]error{}
	for _, ref := range refs {
		failures[ref] = c.err
	}
	return c.fsys, failures
}

// loadRepository: compiler.LoadRepository, each instance with Helm's merge
// of the final values.
func loadRepository(repos []compiler.Repository) ([]compiler.Instance, error) {
	instances, err := compiler.LoadRepository(context.Background(), repos)
	for i := range instances {
		instances[i].Inputs.Rules, instances[i].Inputs.Finalize = rules.Standard(), helmvalues.New()
	}
	return instances, err
}

// TestCompileTreeCharts: the charts the compile names fetched once each,
// then the values checked against them; unchecked, and why, when one
// cannot be fetched.
func TestCompileTreeCharts(t *testing.T) {
	instances, err := loadRepository([]compiler.Repository{{Name: "db", FS: os.DirFS("../examples/04_claims/input/db"), Role: compiler.RoleTree}})
	if err != nil {
		t.Fatal(err)
	}
	away := &charts{err: errors.New("the registry is away")}
	tree := compiler.CompileTree(t.Context(), instances, away)
	if len(tree.ChartFailures) == 0 || tree.Problems() != nil || tree.Graph == nil {
		t.Errorf("unchecked: ChartFailures %v, problems %q", tree.ChartFailures, tree.Problems())
	}
	for ref, err := range tree.ChartFailures {
		if !errors.Is(err, away.err) {
			t.Errorf("chart %v: %v", ref, err)
		}
	}
	if len(away.refs) == 0 || len(slices.Compact(slices.Clone(away.refs))) != len(away.refs) {
		t.Errorf("refs: %v", away.refs)
	}
	empty := &charts{fsys: fstest.MapFS{}}
	tree = compiler.CompileTree(t.Context(), instances, empty)
	if tree.ChartFailures != nil || tree.Problems() == nil {
		t.Errorf("checked against a cache without the charts: ChartFailures %v, no problem", tree.ChartFailures)
	}
	if !reflect.DeepEqual(empty.refs, away.refs) {
		t.Errorf("refs: %v, then %v", away.refs, empty.refs)
	}
	for _, inst := range instances {
		if inst.Inputs.Charts != nil {
			t.Fatal("the instances given were changed")
		}
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
	tree := fstest.MapFS{
		compiler.PlatformFile: file(platform),
		path.Join(compiler.CatalogDir, "web", "1.0.0", "Chart.yaml"):                           file("apiVersion: v2\nname: web\nversion: 1.0.0\n"),
		path.Join(compiler.ProjectsDir, "demo", compiler.ProjectFile):                          file(project),
		path.Join(compiler.ProjectsDir, "demo", compiler.InstancesDir, "web", "instance.yaml"): file("service: web\nversion: 1.0.0\n"),
		path.Join(compiler.ProjectsDir, "ops", compiler.InstancesDir, "web", "instance.yaml"):  file("service: web\nversion: 1.0.0\n"),
	}
	tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: "db", FS: tree, Role: compiler.RoleTree}}, compiler.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if problems := tr.Problems(); problems != nil {
		t.Fatal(problems)
	}
	files, _, err := deploy.TreeFiles(tr, []deploy.Target{deploy.ArgoCD{RepoURL: "https://git.example.org/o.git"}, deploy.Flux{}}, nil)
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
	values, err := compiler.ParseValues([]byte(platform))
	if err != nil {
		t.Fatal(err)
	}
	if admins, err := compiler.PlatformAdmins(context.Background(), rules.Standard(), values); err != nil || !slices.Equal(admins, []string{"platform_admin"}) {
		t.Errorf("PlatformAdmins: %v, %v", admins, err)
	}
	if compiler.DefaultProjectGroup("demo") != "okdp-project-demo" || !compiler.IsGroupName("platform_admin") || compiler.IsGroupName("/platform_admin") {
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
	tree := fstest.MapFS{
		compiler.PlatformFile:                    file("global: {okdp: {policy: {privileged: [ops]}}}\n"),
		"catalog/admin/1.0.0/Chart.yaml":         file("apiVersion: v2\nname: admin\nversion: 1.0.0\ndependencies:\n  - {name: hello, alias: app, repository: https://charts.example.org, version: 1.0.0}\n"),
		"catalog/admin/1.0.0/okdp.yaml":          file("objects:\n  - {apiVersion: rbac.authorization.k8s.io/v1, kind: ClusterRoleBinding, metadata: {name: ops-admin}, roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}, subjects: [{kind: ServiceAccount, name: default, namespace: ops}]}\n"),
		"catalog/admin/1.0.0/config.schema.json": file(`{"type": "object", "properties": {"n": {"type": "integer"}}}`),
		"projects/ops/project.yaml":              file("access: {ops-team: admin}\n"),
		"projects/ops/instances/x/instance.yaml": file("service: admin\nversion: 1.0.0\n"),
	}
	targets := []deploy.Target{deploy.Flux{}, deploy.ArgoCD{RepoURL: "https://git.example.org/out.git"}}
	build := func() *compiler.Tree {
		tr, err := deploy.Build(t.Context(), []compiler.Repository{{Name: "db", FS: tree, Role: compiler.RoleTree}}, compiler.BuildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	first, _, err := deploy.TreeFiles(build(), targets, nil)
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
	tree[compiler.PlatformFile] = file("global: {okdp: {policy: {privileged: []}}}\n")
	tree["projects/ops/project.yaml"] = file("access: {}\n")
	tree["projects/ops/instances/broken/instance.yaml"] = file("service: admin\nversion: 1.0.0\n")
	tree["projects/ops/instances/broken/config.yaml"] = file("n: notanumber\n")
	second, kept, err := deploy.TreeFiles(build(), targets, previous)
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
