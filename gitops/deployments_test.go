package gitops

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"github.com/okdp/okdp-gitops/deploy"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
)

// The test tree (testdata/tree) and its catalog (testdata/catalog): the
// project data provides the databases of the project demo's claims, a
// default one (demo-app), a named one (demo-reports, analytics), and
// demo-legacy references a connection of its project (ext-db).
const (
	treeFixture    = "../testdata/tree"
	catalogFixture = "../testdata/catalog"
)

// readFixture reads the files of a directory, by slash path.
func readFixture(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// testCompiler compiles with the test catalog.
func testCompiler() *Compiler {
	return NewCompiler(&catalog.Loader{Dirs: map[string]string{"test-charts": catalogFixture}})
}

// testDeployments are the deployments of the test tree, in one memory store,
// their writes checked.
func testDeployments(t *testing.T) (*Deployments, *MemoryStore) {
	t.Helper()
	store := MustMemoryStore(readFixture(t, treeFixture))
	d := NewDeployments(store)
	d.Compiler = testCompiler()
	return d, store
}

// compileAll is what the CI of the output runs on the sources: okdp-gitops
// compile, the whole output from the tree, in one commit.
func compileAll(t *testing.T, d *Deployments) {
	t.Helper()
	if kept := compile(t, d); kept != nil {
		t.Fatalf("kept: %+v", kept)
	}
}

// compile is okdp-gitops compile: the output of the projects that compile
// written, in one commit; the projects kept.
func compile(t *testing.T, d *Deployments) []tree.Kept {
	t.Helper()
	ctx := context.Background()
	var kept []tree.Kept
	if _, err := d.Store.Update(ctx, Commit{Subject: "okdp: compile"}, func(tx Tx) error {
		var err error
		kept, err = d.WriteCompiled(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return kept
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// An edit is a commit named after it, by its actor, checked by the
// compile of the tree before and after it: one refused, or that changes
// nothing, commits nothing. Its rules are okdp-compiler's (treeedit).
func TestAnEditIsACommitCheckedByTheCompile(t *testing.T) {
	ctx := context.Background()
	d, store := testDeployments(t)

	web := treeedit.Instance{Project: "demo", Name: "web", Service: "app", Version: "1.0.0", Values: map[string]any{"database": "analytics"}}
	if _, err := d.CreateInstance(ctx, alice, web); err != nil {
		t.Fatal(err)
	}
	if got := store.Files()["projects/demo/instances/web/config.yaml"]; got != "database: analytics\n" {
		t.Errorf("config.yaml = %q", got)
	}
	if got := store.Messages[len(store.Messages)-1]; !strings.HasPrefix(got, "okdp: deploy demo/web by alice") {
		t.Errorf("message = %q", got)
	}
	if _, err := d.CreateInstance(ctx, alice, web); !errors.Is(err, treeedit.ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	if got, err := d.GetInstance(ctx, "demo", "web"); err != nil || got.Release() != "demo-web" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if list, err := d.ListInstances(ctx, "demo"); err != nil || len(list) != 4 {
		t.Fatalf("list: %+v %v", list, err)
	}

	commits, before := len(store.Messages), store.Files()
	got, _, err := d.UpdateInstance(ctx, alice, "demo", "web", func(*treeedit.Instance) error { return nil })
	if err != nil || got.Name != "web" || len(store.Messages) != commits {
		t.Fatalf("a no-op update: %+v %v, %d commits", got, err, len(store.Messages)-commits)
	}
	_, err = d.CreateInstance(ctx, alice, treeedit.Instance{Project: "demo", Name: "other", Service: "app", Version: "1.0.0", Values: map[string]any{"database": "nowhere"}})
	var ce *treeedit.CompileError
	if !errors.As(err, &ce) || ce.Instance != "other" {
		t.Fatalf("an unknown connection: %v", err)
	}
	var inUse *treeedit.ErrInUse
	if _, err := d.DeleteInstance(ctx, alice, "data", "postgresql"); !errors.As(err, &inUse) {
		t.Fatalf("the provider of claims: %v", err)
	}
	if _, err := d.DeleteInstance(ctx, alice, "demo", ".."); !errors.Is(err, treeedit.ErrNotFound) {
		t.Fatalf("a dot segment: %v", err)
	}
	if len(store.Messages) != commits || !maps.Equal(before, store.Files()) {
		t.Error("a refused edit committed")
	}

	got, _, err = d.UpdateInstance(ctx, alice, "demo", "web", func(i *treeedit.Instance) error {
		i.Values = map[string]any{"database": map[string]any{"class": "large"}}
		return nil
	})
	if err != nil || got.Project != "demo" || store.Files()["projects/demo/instances/web/config.yaml"] != "database:\n  class: large\n" {
		t.Fatalf("update: %+v %v", got, err)
	}
	shared := compiler.Connection{Name: "shared", Contract: "database-server", Values: map[string]any{"host": "shared.example.org", "dbName": "shared"}}
	if _, err := d.PutConnection(ctx, alice, "", shared, true); err != nil {
		t.Fatal(err)
	}
	if got := store.Messages[len(store.Messages)-1]; !strings.HasPrefix(got, "okdp: create connection platform/shared by alice") {
		t.Errorf("message = %q", got)
	}
	if c, err := d.GetConnection(ctx, "", "shared"); err != nil || c.Values["dbName"] != "shared" {
		t.Fatalf("the platform's: %+v %v", c, err)
	}
	if _, err := d.DeleteInstance(ctx, alice, "demo", "web"); err != nil {
		t.Fatal(err)
	}
	if got := store.Messages[len(store.Messages)-1]; !strings.HasPrefix(got, "okdp: delete demo/web by alice") {
		t.Errorf("message = %q", got)
	}
}

// The compile of the tree gives its readers each instance's outcome, the
// claims bound to each provider, where each claim of an instance is bound.
func TestTheTreeBindsTheClaims(t *testing.T) {
	d, _ := testDeployments(t)
	built, err := d.Tree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if problems := built.Problems(); len(problems) > 0 || built.Graph == nil {
		t.Fatalf("problems: %v", problems)
	}
	pg := built.Instance("data", "postgresql")
	if pg == nil || pg.Release() != "data-postgresql" || !slices.Equal(pg.Provisions(), []string{"database-server"}) {
		t.Fatalf("data/postgresql: %+v", pg)
	}
	var ids []string
	for _, c := range pg.Provided {
		ids = append(ids, c.ID+" by "+c.Claimer.Namespace()+"/"+c.Claimer.Name)
		if pg.Granted(c.ID) == nil {
			t.Errorf("%s: nothing granted", c.ID)
		}
	}
	if got := strings.Join(ids, ", "); got != "demo.analytics by demo/reports, demo.app.database by demo/app" {
		t.Errorf("provided: %s", got)
	}
	reports := built.Instance("demo", "reports")
	if len(reports.Outcome.Claims) != 1 || reports.Outcome.Claims[0].ID != "demo.analytics" || reports.Outcome.Claims[0].Class != "large" ||
		built.Release(reports.Outcome.Claims[0].Namespace, reports.Outcome.Claims[0].Release) != pg {
		t.Errorf("reports' claims: %+v", reports.Outcome.Claims)
	}
	if legacy := built.Instance("demo", "legacy"); legacy.Err() != nil || len(legacy.Outcome.Claims) != 0 {
		t.Errorf("legacy: %+v", legacy.Outcome)
	}
}

// The output is the targets' files under compiled/: what the server reads
// back of an instance, what the instances publish.
func TestTheOutputIsReadBack(t *testing.T) {
	ctx := context.Background()
	d, store := testDeployments(t)
	if _, err := d.ReadCompiled(ctx, "demo", "app"); !errors.Is(err, treeedit.ErrNotFound) {
		t.Fatalf("compiled before the compile: %v", err)
	}
	compileAll(t, d)
	files := store.Files()
	for _, p := range []string{
		"compiled/projects/kustomization.yaml",
		"compiled/projects/demo/kustomization.yaml",
		"compiled/projects/demo/instances/demo-app/helmrelease.yaml",
		"compiled/projects/demo/instances/demo-app/application.yaml",
		"compiled/projects/data/instances/data-postgresql/okdp.info.yaml",
	} {
		if _, ok := files[p]; !ok {
			t.Errorf("%s missing", p)
		}
	}
	if hr := files["compiled/projects/demo/instances/demo-app/helmrelease.yaml"]; !strings.Contains(hr, "      chart: \"./compiled/projects/demo/instances/demo-app\"\n") ||
		!strings.Contains(hr, "  dependsOn:\n    - name: \"data-postgresql\"\n") {
		t.Errorf("helmrelease.yaml:\n%s", hr)
	}
	compiled, err := d.ReadCompiled(ctx, "demo", "app")
	if err != nil || !strings.Contains(string(compiled.Chart), "name: app") || compiled.Values["app"] == nil {
		t.Fatalf("compiled: %+v %v", compiled, err)
	}
	published, err := d.ListPublished(ctx, "data")
	if err != nil || len(published) != 1 {
		t.Fatalf("published: %+v %v", published, err)
	}
	if pg := published[0]; pg.Name != "postgresql" || pg.Release != "data-postgresql" || pg.Service != "postgresql" || pg.Version != "1.0.0" || len(pg.Publishes.Provides) != 2 {
		t.Errorf("data-postgresql: %+v", pg)
	}

	// An instance removed: its compiled directory goes with the next
	// compile.
	if _, err := d.DeleteInstance(ctx, alice, "demo", "legacy"); err != nil {
		t.Fatal(err)
	}
	compileAll(t, d)
	for p := range store.Files() {
		if strings.Contains(p, "demo-legacy") {
			t.Errorf("%s survived", p)
		}
	}
}

// A problem of the whole tree writes nothing: a file that does not load,
// a release of the output a project kept keeps that a project written
// has too.
func TestAProblemOfTheTreeWritesNothing(t *testing.T) {
	ctx := context.Background()
	app := "service: app\nversion: 1.0.0\n"
	for _, c := range []struct {
		name          string
		setup, change map[string]string
		err           string
	}{
		{name: "a file that does not load", change: map[string]string{"platform.yaml": "global: [\n"}},
		{name: "a release of the output kept", setup: map[string]string{
			"projects/demo/instances/x-y/instance.yaml": app,
		}, change: map[string]string{
			"projects/demo/instances/x-y":                "",
			"projects/demo/instances/legacy/config.yaml": "database: nowhere\n",
			"projects/demo-x/project.yaml":               "{}\n",
			"projects/demo-x/instances/y/instance.yaml":  app,
		}, err: `release "demo-x-y" is in namespaces "demo" and "demo-x": `},
	} {
		d, store := testDeployments(t)
		writeFiles(t, store, c.setup)
		compileAll(t, d)
		writeFiles(t, store, c.change)
		before := store.Files()
		_, err := d.Store.Update(ctx, Commit{Subject: "okdp: compile"}, func(tx Tx) error {
			_, err := d.WriteCompiled(ctx, tx)
			return err
		})
		var layout *tree.LoadError
		if c.err == "" && !errors.As(err, &layout) || c.err != "" && (err == nil || !strings.HasPrefix(err.Error(), c.err)) {
			t.Errorf("%s: %v", c.name, err)
		}
		if !sameFiles(before, store.Files()) {
			t.Errorf("%s: written", c.name)
		}
	}
}

// A release name two projects share keeps those two projects, not the
// tree: demo keeps the output it had, demo-x, new, has none, other is
// written.
func TestAReleaseCollisionKeepsItsProjects(t *testing.T) {
	app := "service: app\nversion: 1.0.0\n"
	d, store := testDeployments(t)
	writeFiles(t, store, map[string]string{"projects/demo/instances/x-y/instance.yaml": app})
	compileAll(t, d)
	first := store.Files()

	writeFiles(t, store, map[string]string{
		"projects/demo-x/project.yaml":              "{}\n",
		"projects/demo-x/instances/y/instance.yaml": app,
		"projects/other/project.yaml":               "{}\n",
		"projects/other/instances/z/instance.yaml":  app,
	})
	kept := compile(t, d)
	if len(kept) != 2 || kept[0].Namespace != "demo" || kept[1].Namespace != "demo-x" {
		t.Fatalf("kept: %+v", kept)
	}
	for _, k := range kept {
		if len(k.Instances) != 1 || k.Instances[0].Release() != "demo-x-y" || k.Instances[0].Depends {
			t.Fatalf("%s: %+v", k.Namespace, k.Instances)
		}
		re, ok := errors.AsType[*deploy.ReleaseError](k.Instances[0].Err)
		if !ok || re.Release != "demo-x-y" || !slices.Equal(re.Namespaces, []string{"demo", "demo-x"}) {
			t.Errorf("%s: %v", k.Namespace, k.Instances[0].Err)
		}
		if len(k.Problems) != 1 || !strings.Contains(k.Problems[0], `release "demo-x-y" is in namespaces "demo" and "demo-x"`) {
			t.Errorf("%s: %q", k.Namespace, k.Problems)
		}
	}
	second := store.Files()
	if !sameFiles(filesUnder(first, "compiled/projects/demo"), filesUnder(second, "compiled/projects/demo")) {
		t.Error("compiled/projects/demo changed")
	}
	if len(filesUnder(second, "compiled/projects/demo-x")) != 0 {
		t.Error("demo-x has an output")
	}
	if len(filesUnder(second, "compiled/projects/other")) == 0 {
		t.Error("other not written")
	}
	root := "compiled/projects/kustomization.yaml"
	if !strings.HasSuffix(second[root], "resources:\n  - data\n  - demo\n  - other\n") {
		t.Errorf("%s:\n%s", root, second[root])
	}
}

// writeFiles writes files to store, a path each, or removes those of
// no content.
func writeFiles(t *testing.T, store Store, files map[string]string) {
	t.Helper()
	if _, err := store.Update(context.Background(), Commit{Subject: "by hand"}, func(tx Tx) error {
		for p, content := range files {
			var err error
			if content == "" {
				err = tx.Remove(p)
			} else {
				err = tx.WriteFile(p, []byte(content))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// under returns the files of dir, by path.
func filesUnder(files map[string]string, dir string) map[string]string {
	out := map[string]string{}
	for p, content := range files {
		if strings.HasPrefix(p, dir+"/") {
			out[p] = content
		}
	}
	return out
}

// A project with an instance that does not compile keeps its output as
// it was; the others' is written. A project that depends on a project
// kept is kept too; one that never compiled has no output.
func TestAProjectThatDoesNotCompileKeepsItsOutput(t *testing.T) {
	d, store := testDeployments(t)
	solo := func(host string) map[string]string {
		return map[string]string{
			"projects/solo/project.yaml":                "connections:\n  ext-db:\n    contract: database-server\n    dbName: ext\n    engine: postgresql\n    host: " + host + "\n    secretRef: {name: ext-db-credentials}\n",
			"projects/solo/instances/web/instance.yaml": "service: app\nversion: 1.0.0\n",
			"projects/solo/instances/web/config.yaml":   "database: ext-db\n",
		}
	}
	writeFiles(t, store, solo("db1.example.org"))
	compileAll(t, d)
	first := store.Files()
	values := "compiled/projects/solo/instances/solo-web/instance.values.yaml"
	if !strings.Contains(first[values], "db1.example.org") {
		t.Fatalf("%s:\n%s", values, first[values])
	}

	// demo/legacy does not compile, demo/web is new: demo keeps its output;
	// fresh never compiled: none; solo changes: written.
	writeFiles(t, store, solo("db2.example.org"))
	writeFiles(t, store, map[string]string{
		"projects/demo/instances/legacy/config.yaml": "database: nowhere\n",
		"projects/demo/instances/web/instance.yaml":  "service: app\nversion: 1.0.0\n",
		"projects/demo/instances/web/config.yaml":    "database: ext-db\n",
		"projects/fresh/project.yaml":                "{}\n",
		"projects/fresh/instances/x/instance.yaml":   "service: app\nversion: 1.0.0\n",
		"projects/fresh/instances/x/config.yaml":     "database: nowhere\n",
	})
	kept := compile(t, d)
	if len(kept) != 2 || kept[0].Namespace != "demo" || kept[1].Namespace != "fresh" ||
		len(kept[0].Problems) != 1 || !strings.HasPrefix(kept[0].Problems[0], `demo/legacy: database: "nowhere" is not a connection`) ||
		len(kept[1].Problems) != 1 || !strings.HasPrefix(kept[1].Problems[0], "fresh/x: ") {
		t.Fatalf("kept: %+v", kept)
	}
	second := store.Files()
	for _, dir := range []string{"compiled/projects/demo", "compiled/projects/data"} {
		if !sameFiles(filesUnder(first, dir), filesUnder(second, dir)) {
			t.Errorf("%s changed", dir)
		}
	}
	if len(filesUnder(second, "compiled/projects/fresh")) != 0 {
		t.Error("fresh has an output")
	}
	if !strings.Contains(second[values], "db2.example.org") {
		t.Errorf("solo not written:\n%s", second[values])
	}
	root := "compiled/projects/kustomization.yaml"
	if second[root] != first[root] || !strings.HasSuffix(second[root], "resources:\n  - data\n  - demo\n  - solo\n") {
		t.Errorf("%s:\n%s", root, second[root])
	}

	// data does not compile: demo, whose claims it provides, is kept with
	// it; solo, which depends on neither, is written.
	writeFiles(t, store, solo("db3.example.org"))
	writeFiles(t, store, map[string]string{
		"projects/demo/instances/legacy/config.yaml":   "database: ext-db\n",
		"projects/demo/instances/web":                  "",
		"projects/fresh":                               "",
		"projects/data/instances/broken/instance.yaml": "service: app\nversion: 1.0.0\n",
		"projects/data/instances/broken/config.yaml":   "database: nowhere\n",
	})
	kept = compile(t, d)
	if len(kept) != 2 || kept[0].Namespace != "data" || kept[1].Namespace != "demo" ||
		!strings.HasPrefix(kept[0].Problems[0], "data/broken: ") ||
		!slices.Contains(kept[1].Problems, "demo/app: it depends on data/data-postgresql, of the project data, whose output is kept") {
		t.Fatalf("kept: %+v", kept)
	}
	third := store.Files()
	for _, dir := range []string{"compiled/projects/demo", "compiled/projects/data"} {
		if !sameFiles(filesUnder(second, dir), filesUnder(third, dir)) {
			t.Errorf("%s changed", dir)
		}
	}
	if !strings.Contains(third[values], "db3.example.org") || third[root] != first[root] {
		t.Errorf("solo not written:\n%s\n%s", third[values], third[root])
	}

	// Fixed, everything is written.
	writeFiles(t, store, map[string]string{"projects/data/instances/broken": ""})
	compileAll(t, d)
}

// A project's name the compile refuses, okdp-compiler's reserved ones and
// those of platform.yaml's global.okdp.policy.reserved, is a
// *treeedit.CompileError, the compiler's message, and nothing is written; another
// is created.
func TestAProjectOfAReservedNameIsNotCreated(t *testing.T) {
	ctx := context.Background()
	d, store := testDeployments(t)
	if _, err := store.Update(ctx, Commit{Subject: "reserve"}, func(tx Tx) error {
		raw, err := tx.ReadFile(tree.PlatformFile)
		if err != nil {
			return err
		}
		return tx.WriteFile(tree.PlatformFile, append(raw, "    policy:\n      reserved: [openbao]\n"...))
	}); err != nil {
		t.Fatal(err)
	}
	before, commits := store.Files(), len(store.Messages)
	for name, want := range map[string]string{
		"kube-foo": `namespace "kube-foo": the namespace is reserved: kube-* is Kubernetes' own (kube-system, kube-public, kube-node-lease)`,
		"platform": `namespace "platform": the namespace is reserved: platform/ holds the platform's keys of the secret store, which no project writes`,
		"argocd":   `namespace "argocd": the namespace is reserved: a deployer engine's, or the namespace its target writes in (argocd, flux-system, okdp-releases): a project's releases there would drive the engine`,
		"openbao":  `namespace "openbao": the namespace is reserved by the platform (global.okdp.policy.reserved): no project has it`,
	} {
		_, err := d.CreateProject(ctx, alice, treeedit.Project{Name: name})
		var compileErr *treeedit.CompileError
		if !errors.As(err, &compileErr) || compileErr.Project != name || err.Error() != want {
			t.Errorf("%s: got %v, want %s", name, err, want)
		}
		if err := d.CheckProjectName(ctx, name); err == nil || err.Error() != want {
			t.Errorf("%s: check %v", name, err)
		}
	}
	if len(store.Messages) != commits || !maps.Equal(store.Files(), before) {
		t.Errorf("a refused project was written: %d commits", len(store.Messages)-commits)
	}
	if err := d.CheckProjectName(ctx, "lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateProject(ctx, alice, treeedit.Project{Name: "lab"}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCatalogKeepsWhatItDoesNotTouch(t *testing.T) {
	ctx := context.Background()
	d, store := testDeployments(t)
	if _, err := d.UpdateCatalog(ctx, alice, "app", func(doc map[string]any) error {
		doc["categories"] = []any{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := store.Files()[treeedit.CatalogsFile]
	if !strings.HasPrefix(got, "# The catalog repositories") || !strings.Contains(got, "repositories:\n  - {name: test-charts") || !strings.Contains(got, "categories: []\n") {
		t.Errorf("%s:\n%s", treeedit.CatalogsFile, got)
	}
	cats, err := d.Catalogs(ctx)
	if err != nil || len(cats.List()) != 1 || cats.List()[0].Name != "test-charts" {
		t.Fatalf("catalogs: %+v %v", cats, err)
	}
}

// A menu that names a service of no catalog is refused as the compile
// refuses it: by UpdateCatalog, a *treeedit.CompileError, nothing
// committed; by the compile of a
// tree that holds it, a *tree.LoadError naming the service, nothing
// written.
func TestAMenuOfAServiceInNoCatalogIsRefused(t *testing.T) {
	ctx := context.Background()
	d, store := testDeployments(t)
	before := store.Files()
	addRedis := func(doc map[string]any) error {
		cats, _ := doc["categories"].([]any)
		doc["categories"] = append(cats, map[string]any{"title": "Caches", "services": []any{map[string]any{"name": "redis"}}})
		return nil
	}
	_, err := d.UpdateCatalog(ctx, alice, "redis", addRedis)
	var refused *treeedit.CompileError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), `service "redis" is in no catalog`) {
		t.Fatalf("UpdateCatalog: %v", err)
	}
	if fields := compiler.FieldErrors(refused.Err); len(fields) != 1 || !strings.Contains(fields[0].Message, `categories[2].services[0]: service "redis" is in no catalog`) {
		t.Errorf("the problems of the refusal: %+v", fields)
	}
	if !sameFiles(before, store.Files()) {
		t.Error("UpdateCatalog: written")
	}
	_, err = d.UpdateCatalog(ctx, alice, "postgresql", func(doc map[string]any) error {
		cats := doc["categories"].([]any)
		cats[0].(map[string]any)["services"].([]any)[0].(map[string]any)["default"] = "9.9.9"
		return nil
	})
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "categories[0].services[0].default") {
		t.Fatalf("UpdateCatalog, a default version of no package: %v", err)
	}

	raw := store.Files()[treeedit.CatalogsFile]
	writeFiles(t, store, map[string]string{treeedit.CatalogsFile: raw + "  - title: Caches\n    services:\n      - {name: redis}\n"})
	before = store.Files()
	_, err = d.Store.Update(ctx, Commit{Subject: "okdp: compile"}, func(tx Tx) error {
		_, err := d.WriteCompiled(ctx, tx)
		return err
	})
	var layout *tree.LoadError
	if !errors.As(err, &layout) || !strings.Contains(err.Error(), `catalogs.yaml: categories[2].services[0]: service "redis" is in no catalog`) {
		t.Fatalf("WriteCompiled: %v", err)
	}
	if !sameFiles(before, store.Files()) {
		t.Error("WriteCompiled: written")
	}
	if _, err := d.Tree(ctx); !errors.As(err, &layout) {
		t.Errorf("Tree: %v", err)
	}
}
