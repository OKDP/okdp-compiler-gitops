package main

import (
	"context"
	"errors"
	"path"
	"slices"
	"sort"

	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/gitops"
)

// linked is a repository read into memory with its symbolic links, which
// are not: a path that is, or goes through, one fails, as in a GitStore
// (gitops.SymlinkError, naming the repository and the link), and its
// directory lists it. A link the tree does not reach is no problem: a
// repository may hold other things than the tree.
type linked struct {
	*gitops.MemoryStore
	name  string
	links map[string]bool
}

func (s *linked) View(ctx context.Context, fn func(treeedit.Reader) error) error {
	return s.MemoryStore.View(ctx, func(r treeedit.Reader) error {
		return fn(&linkedTx{Reader: r, s: s})
	})
}

func (s *linked) Update(ctx context.Context, commit gitops.Commit, fn func(gitops.Tx) error) (string, error) {
	return s.MemoryStore.Update(ctx, commit, func(tx gitops.Tx) error {
		return fn(&linkedTx{Reader: tx, tx: tx, s: s})
	})
}

// linkedTx is a transaction of a linked store; tx is nil in a view.
type linkedTx struct {
	treeedit.Reader
	tx gitops.Tx
	s  *linked
}

// through refuses a path that is, or goes through, a link.
func (t *linkedTx) through(p string) error {
	for q := path.Clean(p); q != "." && q != "/"; q = path.Dir(q) {
		if t.s.links[q] {
			return gitops.SymlinkError(t.s.name, q)
		}
	}
	return nil
}

func (t *linkedTx) ReadFile(p string) ([]byte, error) {
	if err := t.through(p); err != nil {
		return nil, err
	}
	return t.Reader.ReadFile(p)
}

func (t *linkedTx) ReadDir(p string) ([]string, error) {
	if err := t.through(p); err != nil {
		return nil, err
	}
	names, err := t.Reader.ReadDir(p)
	if err != nil {
		return nil, err
	}
	dir := path.Clean(p)
	for link := range t.s.links {
		if path.Dir(link) == dir && !slices.Contains(names, path.Base(link)) {
			names = append(names, path.Base(link))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (t *linkedTx) Exists(p string) bool {
	return t.through(p) == nil && t.Reader.Exists(p)
}

func (t *linkedTx) WriteFile(p string, data []byte) error {
	if err := t.through(p); err != nil {
		return err
	}
	if t.tx == nil {
		return errors.New("read-only view")
	}
	return t.tx.WriteFile(p, data)
}

func (t *linkedTx) Remove(p string) error {
	if err := t.through(p); err != nil {
		return err
	}
	if t.tx == nil {
		return errors.New("read-only view")
	}
	return t.tx.Remove(p)
}
