package gitops

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/okdp/okdp-gitops/treeedit"
)

// MemoryStore is an in-memory Store, for tests and for running the server
// without a remote. Every Update that changes something bumps the revision;
// one write at a time, the views at once, each of the files as they were
// when it started.
type MemoryStore struct {
	// wmu: one write at a time; mu guards the fields, held briefly.
	wmu      sync.Mutex
	mu       sync.Mutex
	files    map[string][]byte
	revision int
	// Messages records the full commit messages, oldest first, as a GitStore
	// committing as DefaultCommitter writes them.
	Messages []string
	// Commits records the commits themselves, oldest first.
	Commits []Commit
}

// NewMemoryStore returns a store holding the given files, by path; a path
// out of the store (absolute, or going up out of it) is refused.
func NewMemoryStore(files map[string]string) (*MemoryStore, error) {
	s := &MemoryStore{files: map[string][]byte{}}
	for p, content := range files {
		cleaned, err := cleanPath(p)
		if err != nil {
			return nil, err
		}
		s.files[cleaned] = []byte(content)
	}
	return s, nil
}

// MustMemoryStore is NewMemoryStore for the files a program, a test, writes
// itself: it panics on a path NewMemoryStore refuses. Files read from a
// repository go to NewMemoryStore.
func MustMemoryStore(files map[string]string) *MemoryStore {
	s, err := NewMemoryStore(files)
	if err != nil {
		panic(err)
	}
	return s
}

// Files returns a copy of the current content, for assertions.
func (s *MemoryStore) Files() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.files))
	for p, data := range s.files {
		out[p] = string(data)
	}
	return out
}

// View runs fn on the files as they are, no lock held: an Update replaces
// the map of the files, never changes it.
func (s *MemoryStore) View(_ context.Context, fn func(r treeedit.Reader) error) error {
	s.mu.Lock()
	files := s.files
	s.mu.Unlock()
	return fn(&memoryTx{files: files})
}

// Update runs fn on a copy of the files, which replaces them when it
// changed something: the views under way keep reading the files they
// started with.
func (s *MemoryStore) Update(_ context.Context, commit Commit, fn func(tx Tx) error) (string, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	s.mu.Lock()
	files := s.files
	s.mu.Unlock()
	working := make(map[string][]byte, len(files))
	maps.Copy(working, files)
	tx := &memoryTx{files: working}
	if err := fn(tx); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !tx.changed(files) {
		return s.rev(), nil
	}
	s.files = working
	s.revision++
	s.Messages = append(s.Messages, commit.Message(DefaultCommitter))
	s.Commits = append(s.Commits, commit)
	return s.rev(), nil
}

func (s *MemoryStore) rev() string { return fmt.Sprintf("mem-%d", s.revision) }

type memoryTx struct {
	files map[string][]byte
}

func (t *memoryTx) changed(before map[string][]byte) bool {
	if len(before) != len(t.files) {
		return true
	}
	for p, data := range t.files {
		if old, ok := before[p]; !ok || string(old) != string(data) {
			return true
		}
	}
	return false
}

func (t *memoryTx) ReadFile(p string) ([]byte, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	data, ok := t.files[cleaned]
	if !ok {
		return nil, fmt.Errorf("%s: %w", cleaned, treeedit.ErrNotFound)
	}
	return append([]byte(nil), data...), nil
}

func (t *memoryTx) ReadDir(p string) ([]string, error) {
	cleaned, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	prefix := cleaned + "/"
	if cleaned == "." {
		prefix = ""
	}
	seen := map[string]bool{}
	for f := range t.files {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(f, prefix), "/")
		seen[name] = true
	}
	if len(seen) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (t *memoryTx) Exists(p string) bool {
	cleaned, err := cleanPath(p)
	if err != nil {
		return false
	}
	if _, ok := t.files[cleaned]; ok {
		return true
	}
	names, _ := t.ReadDir(cleaned)
	return len(names) > 0
}

func (t *memoryTx) WriteFile(p string, data []byte) error {
	cleaned, err := cleanPath(p)
	if err != nil {
		return err
	}
	t.files[cleaned] = append([]byte(nil), data...)
	return nil
}

func (t *memoryTx) Remove(p string) error {
	cleaned, err := cleanPath(p)
	if err != nil {
		return err
	}
	delete(t.files, cleaned)
	prefix := cleaned + "/"
	for f := range t.files {
		if strings.HasPrefix(f, prefix) {
			delete(t.files, f)
		}
	}
	return nil
}
