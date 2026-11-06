// Package mapfs is a tree of files in memory, as an fs.FS: what this
// module reads from a store or a commit, then hands to a compile.
package mapfs

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// dirMode: the mode of a directory that has no entry of its own.
const dirMode = fs.ModeDir | 0o555

// File: a file of a tree, or a directory when its mode says so.
type File struct {
	Data []byte
	Mode fs.FileMode
}

// FS: the files of a tree by path, slash-separated, without leading
// slash. The directories above a file exist by it: a directory needs an
// entry of its own only to be empty. Written while it is built, then only
// read: safe for concurrent use.
type FS map[string]*File

// Open: the file or the directory name.
func (m FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f := m[name]
	if f != nil && !f.Mode.IsDir() {
		return &openFile{Reader: bytes.NewReader(f.Data), info: info{name: path.Base(name), mode: f.Mode, size: int64(len(f.Data))}}, nil
	}
	entries := m.children(name)
	if f == nil && len(entries) == 0 && name != "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	mode := dirMode
	if f != nil {
		mode = f.Mode
	}
	return &openDir{info: info{name: path.Base(name), mode: mode}, entries: entries}, nil
}

// children: the entries of the directory name, by name.
func (m FS) children(name string) []fs.DirEntry {
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	found := map[string]info{}
	for p, f := range m {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || p == "." {
			continue
		}
		first, _, nested := strings.Cut(rest, "/")
		if !nested {
			found[first] = info{name: first, mode: f.Mode, size: int64(len(f.Data))}
		} else if _, ok := found[first]; !ok {
			found[first] = info{name: first, mode: dirMode}
		}
	}
	entries := make([]fs.DirEntry, 0, len(found))
	for _, i := range found {
		entries = append(entries, fs.FileInfoToDirEntry(i))
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries
}

// openFile: a file being read.
type openFile struct {
	*bytes.Reader
	info info
}

// Stat: its description.
func (f *openFile) Stat() (fs.FileInfo, error) { return f.info, nil }

// Close: nothing to release.
func (f *openFile) Close() error { return nil }

// openDir: a directory being read.
type openDir struct {
	info    info
	entries []fs.DirEntry
	next    int
}

// Stat: its description.
func (d *openDir) Stat() (fs.FileInfo, error) { return d.info, nil }

// Read: a directory holds no bytes.
func (d *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid}
}

// Close: nothing to release.
func (d *openDir) Close() error { return nil }

// ReadDir: the next n entries, every one left when n <= 0 (fs.ReadDirFile).
func (d *openDir) ReadDir(n int) ([]fs.DirEntry, error) {
	left := d.entries[d.next:]
	if n <= 0 {
		d.next = len(d.entries)
		return slices.Clone(left), nil
	}
	if len(left) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(left))
	d.next += n
	return slices.Clone(left[:n]), nil
}

// info: what fs.FileInfo says of a file or a directory.
type info struct {
	name string
	mode fs.FileMode
	size int64
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return nil }
