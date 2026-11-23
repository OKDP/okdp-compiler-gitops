package examples_test

import (
	"github.com/okdp/okdp-gitops/tree"

	"bytes"
	"errors"
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"

	"github.com/okdp/okdp-gitops/deploy"
	"github.com/okdp/okdp-gitops/internal/chartcheck"
	"github.com/okdp/okdp-gitops/internal/helmvalues"
)

var update = flag.Bool("update", false, "rewrite the examples' outputs from the current compiler")

// examples: each example a repository tree (docs/01_usage.md, repository
// layout): its sources under input/db/, what the helm target writes for
// its instances in output/, and, for --check --charts, a chart cache in
// .charts/.
const examples = "examples"

// load: every instance of an example's tree, its input/db/ one
// repository, as deploy.Build loads it (the standard rule set, Helm's
// merge, the platform's policy on the globals of the projects and the
// instances): a project that does not load fails.
func load(t *testing.T, example string) []compiler.Instance {
	t.Helper()
	db := filepath.Join(examples, example, "input", "db")
	built, err := deploy.Build(t.Context(), []tree.Repository{{Name: db, FS: os.DirFS(db), Role: tree.RoleTree}}, deploy.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if le := tree.LoadErrorOf(built); le != nil {
		t.Fatal(le)
	}
	var instances []compiler.Instance
	for _, ti := range built.Instances {
		instances = append(instances, ti.Instance)
	}
	return instances
}

// exists: whether path exists.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// TestExample compiles the instances of each example's tree together
// (load, compiler.CompileAll), checks their graph (tree.NewGraph) and compares what the
// helm target writes with its output/, byte for byte; a file of output/
// that it does not write fails. 00_passthrough: Chart.yaml and a plain
// values.yaml, no instance values. 01_minimal: one chart, no contract.
// 02_advanced: dcr mode, the OAuth client's chart added; the database it
// references. 03_full: the project's project.yaml at level 1 and the
// instance's connections.yaml at level 2, between platform.yaml and
// config.yaml; the instances it references; its chart cache, .charts/,
// checks the compiles (--check --charts), which give the same files and
// no other warning.
func TestExample(t *testing.T) {
	dbs, _ := filepath.Glob(filepath.Join(examples, "*", "input", "db"))
	if len(dbs) == 0 {
		t.Fatal("no example")
	}
	for _, db := range dbs {
		example := filepath.Base(filepath.Dir(filepath.Dir(db)))
		t.Run(example, func(t *testing.T) {
			instances := load(t, example)
			files, warnings := compileTree(t, instances)
			checkDir(t, filepath.Join(example, "output"), files)
			charts := filepath.Join(examples, example, ".charts")
			if !exists(t, charts) {
				return
			}
			finalize := helmvalues.New(chartcheck.New(chartcheck.Static(os.DirFS(charts))))
			for i := range instances {
				instances[i].Inputs.Finalize = finalize
			}
			checked, checkedWarnings := compileTree(t, instances)
			if !maps.EqualFunc(checked, files, bytes.Equal) || !slices.Equal(checkedWarnings, warnings) {
				t.Errorf("checked with %s: other files, or warnings %q", charts, checkedWarnings)
			}
		})
	}
}

// compileTree: what the helm target writes for instances compiled
// together, their graph checked, and their warnings, in their order.
func compileTree(t *testing.T, instances []compiler.Instance) (map[string][]byte, []string) {
	t.Helper()
	var results []*compiler.Result
	var warnings []string
	for i, o := range compiler.CompileAll(t.Context(), instances) {
		if o.Err != nil {
			t.Fatalf("%s: %v", instances[i].Inputs.Release, o.Err)
		}
		results = append(results, o.Result)
		warnings = append(warnings, o.Result.Warnings...)
	}
	g, err := tree.NewGraph(results)
	if err != nil {
		t.Fatal(err)
	}
	files, err := deploy.Helm{}.Files(results, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	return files, warnings
}

// checkDir: examples/<dir> holds these files, path -> content, and only
// them, byte for byte, its subdirectories included; -update rewrites it.
func checkDir(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	root := filepath.Join(examples, dir)
	if *update {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		for name, b := range files {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	var names []string
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			rel, _ := filepath.Rel(root, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatalf("%v (run go test . -update)", err)
	}
	for name := range files {
		if !slices.Contains(names, name) {
			t.Errorf("examples/%s/%s: missing, run go test . -update and review the diff", dir, name)
		}
	}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if want, ok := files[name]; !ok || !bytes.Equal(b, want) {
			t.Errorf("examples/%s/%s differs from the compile, run go test . -update and review the diff", dir, name)
		}
	}
}

// TestExampleErrors: on the instance demo-webapp of 02_advanced, every
// testdata/<case>.yaml, an instance file in place of its config.yaml, and
// every testdata/context/<level>/<case>.yaml, a file of that level
// without config.yaml, must fail the tree (deploy.Build: it does not
// load, or the instance is refused) with the message of its first line
// (# error: ..., \n for a line break). A context file of level 0 is
// merged over platform.yaml, one of level 1 is demo's project.yaml, one
// of level 2 webapp's connections.yaml, its other keys its config.yaml:
// the parameters that reference the connections it gives. Its key
// platform, when it has one, is merged over platform.yaml: a setting of
// the platform's the case needs (its secret store).
func TestExampleErrors(t *testing.T) {
	testdata := "testdata"
	values, _ := filepath.Glob(filepath.Join(testdata, "*.yaml"))
	context, _ := filepath.Glob(filepath.Join(testdata, "context", "*", "*.yaml"))
	if len(values) == 0 || len(context) == 0 {
		t.Fatal("no testdata")
	}
	db := filepath.Join(examples, "02_advanced", "input", "db")
	webapp := "projects/demo/instances/webapp"
	for _, f := range slices.Concat(values, context) {
		name, _ := filepath.Rel(testdata, f)
		t.Run(strings.TrimSuffix(name, ".yaml"), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(string(b), "\n")
			want, ok := strings.CutPrefix(first, "# error: ")
			if !ok {
				t.Fatalf("first line must be \"# error: <message>\", got %q", first)
			}
			want = strings.ReplaceAll(want, `\n`, "\n")
			src := readTree(t, db)
			layer, err := chart.ParseValues(b)
			if err != nil {
				t.Fatal(err)
			}
			level, err := strconv.ParseUint(filepath.Base(filepath.Dir(f)), 10, 0)
			if err != nil {
				src[webapp+"/config.yaml"] = &fstest.MapFile{Data: b}
			} else {
				delete(src, webapp+"/config.yaml")
				if p, ok := layer["platform"].(map[string]any); ok {
					mergeFile(t, src, tree.PlatformFile, p)
					delete(layer, "platform")
				}
				switch level {
				case 0:
					mergeFile(t, src, tree.PlatformFile, layer)
				case 1:
					writeFile(t, src, "projects/demo/project.yaml", layer)
				case 2:
					connections := map[string]any{"connections": layer["connections"]}
					delete(layer, "connections")
					writeFile(t, src, webapp+"/connections.yaml", connections)
					writeFile(t, src, webapp+"/config.yaml", layer)
				}
			}
			built, err := deploy.Build(t.Context(), []tree.Repository{{Name: "db", FS: src, Role: tree.RoleTree}}, deploy.BuildOptions{})
			var got []string
			if err != nil {
				got = append(got, err.Error())
			} else {
				if le := tree.LoadErrorOf(built); le != nil {
					got = append(got, le.Error())
				}
				if ti := built.Instance("demo", "webapp"); ti != nil && ti.Err() != nil {
					got = append(got, ti.Err().Error())
				}
			}
			if all := strings.Join(got, "\n"); !strings.Contains(all, want) {
				t.Errorf("got %s, want an error containing %q", all, want)
			}
		})
	}
}

// readTree: the files under dir, by path.
func readTree(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// writeFile: values as the file name of src.
func writeFile(t *testing.T, src fstest.MapFS, name string, values map[string]any) {
	t.Helper()
	b, err := yaml.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	src[name] = &fstest.MapFile{Data: b}
}

// mergeFile: values merged over the file name of src, as the compile
// merges values layers (compiler.MergeValues).
func mergeFile(t *testing.T, src fstest.MapFS, name string, values map[string]any) {
	t.Helper()
	base, err := chart.ParseValues(src[name].Data)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, name, compiler.MergeValues(base, values))
}
