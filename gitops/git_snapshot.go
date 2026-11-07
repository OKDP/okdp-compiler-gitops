package gitops

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/okdp/okdp-gitops/treeedit"
)

// gitSnapshot is the tree of a GitStore at one commit, read out of the
// commit's objects into memory: immutable, any number of readers read it
// at once, while the store fetches and writes. A snapshot is never
// changed: a newer commit is a new snapshot, which shares the content of
// the files that did not change (by blob).
//
// What it reads is bounded (snapshotLimits): the size of a blob is read
// from its object header before the blob is, and a file over MaxTreeFileSize,
// or that would take the blobs over MaxTreeSize, is listed but never read,
// reading it fails (ErrTooLarge); and only the paths of the layout are read
// when the store names them (GitOptions.Layout).
type gitSnapshot struct {
	url    string
	prefix string
	// rev is the commit, "" for a branch that does not exist (an empty
	// remote): an empty tree.
	rev string
	// at is when the remote branch was last seen at rev.
	at time.Time
	// err refuses every read: the path prefix is, or goes through, a
	// symbolic link.
	err error
	// files are the regular files, by path relative to the prefix, their
	// blob; blobs the content of each blob.
	files map[string]plumbing.Hash
	blobs map[plumbing.Hash][]byte
	// unread are the regular files not read, by path relative to the
	// prefix, why: over the bounds.
	unread map[string]error
	// layout identifies the paths the snapshot was read for
	// (snapshotLimits.key).
	layout string
	// dirs are the sorted names of each directory, "." the root: files,
	// directories and symbolic links.
	dirs  map[string][]string
	links map[string]bool
}

// snapshotLimits bound what a snapshot reads.
type snapshotLimits struct {
	// layout are the paths of the repository (from its root, slash
	// separated, "." for all of it) to read, nil for every path under the
	// store's prefix: a file beside them is not listed, nor read.
	layout []string
	// file is the size a blob may have, tree the size the blobs may have
	// together, in bytes.
	file, tree int64
}

// key identifies the paths of the layout: "*" for all of the repository.
func (l snapshotLimits) key() string {
	if l.layout == nil {
		return "*"
	}
	return "layout:" + strings.Join(l.layout, "\x00")
}

// reach tells how the layout reaches the path p of the repository: in,
// when p is a layout path or under one, and ancestor when p is a
// directory above one, which is walked for it.
func (l snapshotLimits) reach(p string) (in, ancestor bool) {
	if l.layout == nil {
		return true, false
	}
	for _, lp := range l.layout {
		switch {
		case lp == "." || lp == p || strings.HasPrefix(p, lp+"/"):
			return true, false
		case strings.HasPrefix(lp, p+"/") || p == ".":
			ancestor = true
		}
	}
	return false, ancestor
}

// buildSnapshot reads the tree under the store's prefix at commit rev, the
// blobs of prev, when set, reused: only what changed is read.
func buildSnapshot(repo *git.Repository, url, prefix, rev string, prev *gitSnapshot, lim snapshotLimits) (*gitSnapshot, error) {
	snap := &gitSnapshot{
		url: url, prefix: prefix, rev: rev, layout: lim.key(),
		files: map[string]plumbing.Hash{}, blobs: map[plumbing.Hash][]byte{}, unread: map[string]error{},
		dirs: map[string][]string{}, links: map[string]bool{},
	}
	if rev == "" {
		return snap, nil
	}
	commit, err := repo.CommitObject(plumbing.NewHash(rev))
	if err != nil {
		return nil, fmt.Errorf("failed to read commit %s: %w", rev, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to read the tree of commit %s: %w", rev, err)
	}
	if prefix != "." {
		parts := strings.Split(prefix, "/")
		for i, name := range parts {
			entry := findEntry(tree, name)
			switch {
			case entry == nil || (entry.Mode != filemode.Dir && entry.Mode != filemode.Symlink):
				// No tree under the prefix: an empty one.
				return snap, nil
			case entry.Mode == filemode.Symlink:
				snap.err = SymlinkError(url, path.Join(parts[:i+1]...))
				return snap, nil
			}
			if tree, err = repo.TreeObject(entry.Hash); err != nil {
				return nil, err
			}
		}
	}
	b := &snapshotBuilder{repo: repo, snap: snap, prev: prev, lim: lim}
	if err := b.walk(tree, "."); err != nil {
		return nil, err
	}
	return snap, nil
}

func findEntry(tree *object.Tree, name string) *object.TreeEntry {
	for i := range tree.Entries {
		if tree.Entries[i].Name == name {
			return &tree.Entries[i]
		}
	}
	return nil
}

type snapshotBuilder struct {
	repo *git.Repository
	snap *gitSnapshot
	prev *gitSnapshot
	lim  snapshotLimits
	// size is the bytes of the blobs read so far.
	size int64
}

// walk reads the directory dir of the snapshot, the Git tree t: a
// submodule, which the tree does not hold, is left out, and so is what the
// layout does not reach.
func (b *snapshotBuilder) walk(t *object.Tree, dir string) error {
	names := make([]string, 0, len(t.Entries))
	for _, e := range t.Entries {
		if e.Name == ".git" {
			continue
		}
		p := path.Join(dir, e.Name)
		in, ancestor := b.lim.reach(path.Join(b.snap.prefix, p))
		switch {
		case e.Mode == filemode.Dir:
			if !in && !ancestor {
				continue
			}
			sub, err := b.repo.TreeObject(e.Hash)
			if err != nil {
				return err
			}
			if err := b.walk(sub, p); err != nil {
				return err
			}
		case e.Mode == filemode.Symlink:
			if !in && !ancestor {
				continue
			}
			b.snap.links[p] = true
		case e.Mode.IsFile():
			if !in {
				continue
			}
			if err := b.file(p, e.Hash); err != nil {
				return fmt.Errorf("failed to read %s: %w", path.Join(b.snap.prefix, p), err)
			}
		default:
			continue
		}
		names = append(names, e.Name)
	}
	sort.Strings(names)
	b.snap.dirs[dir] = names
	return nil
}

// isVendoredArchive: whether p is a chart archive vendored in an instance's
// umbrella of the output: .../charts/<name>-<version>.tgz.
func isVendoredArchive(p string) bool {
	dir, file := path.Split(p)
	return strings.HasSuffix(strings.TrimSuffix(dir, "/"), "/charts") && strings.HasSuffix(file, ".tgz")
}

// file records the regular file p, of blob h, reading the blob when its
// size, known from its header, is within the bounds: else the file is
// listed and not read.
func (b *snapshotBuilder) file(p string, h plumbing.Hash) error {
	full := path.Join(b.snap.prefix, p)
	var data []byte
	var size int64
	if d, ok := b.snap.blobs[h]; ok {
		data = d
	} else if b.prev != nil {
		data = b.prev.blobs[h]
	}
	if data != nil {
		size = int64(len(data))
	} else {
		var err error
		if size, err = b.repo.Storer.EncodedObjectSize(h); err != nil {
			return err
		}
	}
	_, counted := b.snap.blobs[h]
	// A vendored chart archive of the output (compiled/.../charts/*.tgz):
	// read within the bound of a file, not counted against the tree's,
	// whose budget is for the files people and the compiler write: the
	// charts of many instances would fill it (and each blob is one).
	vendored := isVendoredArchive(p)
	switch {
	case size > b.lim.file:
		b.snap.unread[p] = TooLargeError(full, size)
		return nil
	case !counted && !vendored && b.size+size > b.lim.tree:
		b.snap.unread[p] = treeTooLargeError(b.lim.tree)
		return nil
	case counted:
		b.snap.files[p] = h
		return nil
	}
	if data == nil {
		var err error
		if data, err = b.read(h, size); err != nil {
			return err
		}
	}
	b.snap.blobs[h] = data
	b.snap.files[p] = h
	if !vendored {
		b.size += size
	}
	return nil
}

// read reads the blob h, of size bytes, no more.
func (b *snapshotBuilder) read(h plumbing.Hash, size int64) ([]byte, error) {
	blob, err := b.repo.BlobObject(h)
	if err != nil {
		return nil, err
	}
	r, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("blob %s is %d bytes, its header says %d", h, len(data), size)
	}
	return data, nil
}

// seen is the snapshot, the remote seen at its commit again at at.
func (s *gitSnapshot) seen(at time.Time) *gitSnapshot {
	again := *s
	again.at = at
	return &again
}

// Revision is the commit of the snapshot, "" for a branch that does not
// exist.
func (s *gitSnapshot) Revision() string { return s.rev }

// resolve validates p as fsTx does: a path relative to the prefix, no
// symbolic link on it.
func (s *gitSnapshot) resolve(p string) (string, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return cleaned, nil
	}
	parts := strings.Split(cleaned, "/")
	for i := range parts {
		if prefix := path.Join(parts[:i+1]...); s.links[prefix] {
			return "", SymlinkError(s.url, path.Join(s.prefix, prefix))
		}
	}
	return cleaned, nil
}

func (s *gitSnapshot) ReadFile(p string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	cleaned, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	h, ok := s.files[cleaned]
	if !ok {
		if err, unread := s.unread[cleaned]; unread {
			return nil, err
		}
		if _, dir := s.dirs[cleaned]; dir {
			return nil, fmt.Errorf("%s: is a directory", cleaned)
		}
		return nil, fmt.Errorf("%s: %w", cleaned, treeedit.ErrNotFound)
	}
	return append([]byte(nil), s.blobs[h]...), nil
}

func (s *gitSnapshot) ReadDir(p string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	cleaned, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	names, ok := s.dirs[cleaned]
	if !ok {
		_, file := s.files[cleaned]
		_, unread := s.unread[cleaned]
		if file || unread {
			return nil, fmt.Errorf("%s: not a directory", cleaned)
		}
		return nil, nil
	}
	if len(names) == 0 {
		return nil, nil
	}
	return append([]string(nil), names...), nil
}

func (s *gitSnapshot) Exists(p string) bool {
	if s.err != nil {
		return false
	}
	cleaned, err := s.resolve(p)
	if err != nil {
		return false
	}
	if cleaned == "." {
		return true
	}
	_, file := s.files[cleaned]
	_, unread := s.unread[cleaned]
	_, dir := s.dirs[cleaned]
	return file || unread || dir
}
