package rules

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
)

// TestStandard: the standard set loads and holds a platform's rules.
func TestStandard(t *testing.T) {
	r := Standard()
	if r.Version() == "" || !slices.Contains(compiler.EngineNamespaces(r), "argocd") || len(compiler.Roles(r)) != 3 {
		t.Errorf("the standard set: version %q, roles %v", r.Version(), compiler.Roles(r))
	}
}

// filesOf: the regular files under dir, by relative path.
func filesOf(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
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

// TestCompilerCopy: the copy the compiler's tests compile with is this set:
// a rule changed here is changed there, or they test another.
func TestCompilerCopy(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/okdp/okdp-compiler").Output()
	if err != nil {
		t.Fatal(err)
	}
	copied := filesOf(t, filepath.Join(strings.TrimSpace(string(out)), "testdata", "rules"))
	own := filesOf(t, "v1")
	for name, f := range own {
		c, ok := copied[name]
		if !ok || string(c.Data) != string(f.Data) {
			t.Errorf("%s differs in the compiler's testdata/rules", name)
		}
	}
	for name := range copied {
		if _, ok := own[name]; !ok {
			t.Errorf("%s is only in the compiler's testdata/rules", name)
		}
	}
}
