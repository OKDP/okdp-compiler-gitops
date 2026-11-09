// Package rulestest gives this module's tests a rule set of their own: the
// standard one, its rules.yaml edited. Only tests import it.
package rulestest

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/internal/mapfs"
)

// Edited: the standard rule set, edit applied to its rules.yaml.
func Edited(t *testing.T, edit func(string) string) *compiler.RuleSet {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "rules", "v1")
	files := mapfs.FS{}
	err := fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		files[p] = &mapfs.File{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	files["rules.yaml"] = &mapfs.File{Data: []byte(edit(string(files["rules.yaml"].Data)))}
	r, err := compiler.LoadRules(files)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
