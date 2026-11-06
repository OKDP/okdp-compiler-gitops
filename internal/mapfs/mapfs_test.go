package mapfs

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

func TestFS(t *testing.T) {
	tree := FS{
		"platform.yaml":          {Data: []byte("a: 1\n"), Mode: 0o444},
		"projects/demo":          {Mode: fs.ModeDir | 0o555},
		"projects/demo/one.yaml": {Data: []byte("x\n"), Mode: 0o444},
		"projects/other/two":     {Data: []byte("y\n"), Mode: 0o444},
		"empty":                  {Mode: fs.ModeDir | 0o555},
	}
	if err := fstest.TestFS(tree, "platform.yaml", "projects/demo/one.yaml", "projects/other/two", "empty"); err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(tree, "projects/demo/one.yaml")
	if err != nil || string(got) != "x\n" {
		t.Errorf("read %q, %v", got, err)
	}
	if _, err := tree.Open("projects/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an absent file: %v", err)
	}
	if _, err := tree.Open("../out"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an invalid path: %v", err)
	}
	if entries, err := fs.ReadDir(FS{}, "."); err != nil || len(entries) != 0 {
		t.Errorf("an empty tree: %v, %v", entries, err)
	}
}

// A directory listed by its own entries keeps their modes: a symbolic
// link reads as one.
func TestDirectoryEntries(t *testing.T) {
	dir, err := FS{"f": {Data: []byte("x"), Mode: 0o444}, "l": {Mode: fs.ModeSymlink | 0o777}, "d": {Mode: fs.ModeDir | 0o555}}.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.(fs.ReadDirFile).ReadDir(-1)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"d", "f", "l"}) {
		t.Errorf("names %v", names)
	}
	if entries[2].Type()&fs.ModeSymlink == 0 || !entries[0].IsDir() || entries[1].IsDir() {
		t.Errorf("types: %v %v %v", entries[0].Type(), entries[1].Type(), entries[2].Type())
	}
}
