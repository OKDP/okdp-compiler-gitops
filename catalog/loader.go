package catalog

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/internal/gitfetch"
	"github.com/okdp/okdp-gitops/internal/mapfs"
)

// Bounds of what a catalog may hold: packages are kilobytes.
const (
	MaxFileSize    = 4 << 20
	MaxCatalogSize = 128 << 20
	// MaxRepoSize is what the in-memory clone of a catalog's repository
	// may hold, its objects inflated, before the next fetch starts a new
	// one: each revision fetched adds its objects, none is ever dropped.
	MaxRepoSize = 4 * MaxCatalogSize
)

// DefaultRefreshInterval is how stale a catalog may be before it is
// fetched again.
const DefaultRefreshInterval = 30 * time.Second

// Loader opens the catalogs of a platform: each repository fetched at its
// ref, that ref only and its last commit (kept in memory, fetched again
// after RefreshInterval), or read from a local directory the operator
// names (Dirs). A url of catalogs.yaml that reads the local file system
// is refused, the file being the platform's repository content, not the
// operator's: a local directory always (Dirs reads one), a file://
// repository unless FileURLs. Safe for concurrent use.
type Loader struct {
	// Dirs reads a catalog from a local directory instead of its url, by
	// catalog name (okdp-gitops --catalog <name>=<dir>), its regular
	// files only, bounded as a repository's (MaxFileSize,
	// MaxCatalogSize).
	Dirs map[string]string
	// FileURLs lets catalogs.yaml name file:// repositories: for a
	// platform whose own repository is local (okdp-gitops compile of a
	// topology of local directories or file:// repositories), whose
	// catalogs.yaml is then the operator's; the control plane never.
	FileURLs bool
	// Auth returns the credentials of a repository url, nil for anonymous
	// access (public repositories), an error for none it may send
	// (gitops.Credentials.Catalogs: the credentials of the platform's
	// host, never over http:// to a host they do not allow).
	Auth func(url string) (transport.AuthMethod, error)
	// RefreshInterval: DefaultRefreshInterval when zero.
	RefreshInterval time.Duration
	// CABundle is PEM certificates an https repository is trusted with
	// beside the system's (gitops.Credentials.CABundle).
	CABundle []byte

	mu    sync.Mutex
	repos map[string]*remoteRepo

	// maxRepoSize is MaxRepoSize, the tests' to lower.
	maxRepoSize int64
}

type remoteRepo struct {
	mu   sync.Mutex
	repo *git.Repository
	// fetchedAt: when each ref was fetched last.
	fetchedAt map[string]time.Time
	// trees: the files read at the last commit of each ref, no older
	// revision is kept.
	trees map[string]refTree
}

// refTree is the files of a commit.
type refTree struct {
	hash  plumbing.Hash
	files mapfs.FS
}

// Open returns the catalogs of repositories, in their order.
func (l *Loader) Open(ctx context.Context, repositories []treeedit.CatalogRepository) (*Catalogs, error) {
	var list []Catalog
	for _, r := range repositories {
		cat, err := l.open(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("catalog %s (%s@%s): %w", r.Name, r.URL, r.Ref, err)
		}
		list = append(list, cat)
	}
	return New(list...), nil
}

func (l *Loader) open(ctx context.Context, r treeedit.CatalogRepository) (Catalog, error) {
	if dir, ok := l.Dirs[r.Name]; ok {
		return localCatalog(r.Name, dir)
	}
	if _, ok := LocalPath(r.URL); ok || (!l.FileURLs && strings.HasPrefix(strings.ToLower(r.URL), "file:")) {
		return Catalog{}, fmt.Errorf("the url %q reads the local file system: a catalog of %s is a Git repository, a local checkout the operator's (okdp-gitops compile --catalog %s=DIR)", r.URL, treeedit.CatalogsFile, r.Name)
	}
	repo := l.repo(r.URL)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	interval := l.RefreshInterval
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	if at, ok := repo.fetchedAt[r.Ref]; !ok || time.Since(at) >= interval {
		auth, err := l.auth(r.URL)
		if err != nil {
			return Catalog{}, err
		}
		if err := repo.fetch(ctx, r.URL, r.Ref, auth, l.CABundle, l.repoBound()); err != nil {
			if !ok {
				return Catalog{}, err
			}
			slog.Warn("Could not fetch a catalog, serving the last fetched state", "error", err, "catalog", r.Name)
		}
	}
	hash, err := resolve(repo.repo, r.Ref)
	if err != nil {
		return Catalog{}, err
	}
	tree, ok := repo.trees[r.Ref]
	if !ok || tree.hash != hash {
		files, err := readCommit(repo.repo, hash)
		if err != nil {
			return Catalog{}, err
		}
		tree = refTree{hash: hash, files: files}
		repo.trees[r.Ref] = tree
	}
	return Catalog{Name: r.Name, Revision: hash.String(), FS: tree.files}, nil
}

// repoBound is the size an in-memory clone may reach before a new one.
func (l *Loader) repoBound() int64 {
	if l.maxRepoSize > 0 {
		return l.maxRepoSize
	}
	return MaxRepoSize
}

func (l *Loader) auth(url string) (transport.AuthMethod, error) {
	if l.Auth == nil {
		return nil, nil
	}
	return l.Auth(url)
}

func (l *Loader) repo(url string) *remoteRepo {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.repos == nil {
		l.repos = map[string]*remoteRepo{}
	}
	r, ok := l.repos[url]
	if !ok {
		r = &remoteRepo{fetchedAt: map[string]time.Time{}, trees: map[string]refTree{}}
		l.repos[url] = r
	}
	return r
}

// fetch fetches ref, a branch, a tag or a commit, and its last commit
// only: the refs of the repository listed, the branch ref names fetched,
// else its tag, else the commit ref is (a server serves a commit it holds
// by its hash). A server that serves no commit by its hash gets a fetch of
// every branch and tag, the commit then one of theirs; a server without
// shallow fetches, a fetch of the history of the ref (gitfetch.Fetch).
// The clone lives in memory and only grows: past bound bytes the fetch is
// made into a new one, which replaces it when the fetch succeeded.
func (r *remoteRepo) fetch(ctx context.Context, url, ref string, auth transport.AuthMethod, caBundle []byte, bound int64) error {
	repo := r.repo
	fresh := false
	if repo == nil || objectsSize(repo) > bound {
		var err error
		if repo, err = git.Init(memory.NewStorage(), nil); err != nil {
			return err
		}
		if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
			return err
		}
		fresh = true
	}
	if isHash(ref) {
		if _, err := repo.CommitObject(plumbing.NewHash(ref)); err == nil {
			// A commit does not change: fetched once.
			r.fetchedAt[ref] = time.Now()
			return nil
		}
	}
	spec, err := refSpec(ctx, repo, ref, auth, caBundle)
	if err != nil {
		return err
	}
	opts := &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{spec},
		Auth:       auth,
		Force:      true,
		Depth:      1,
		Tags:       git.NoTags,
		CABundle:   caBundle,
	}
	err = gitfetch.Fetch(ctx, repo, opts)
	if errors.Is(err, git.ErrExactSHA1NotSupported) {
		opts.RefSpecs = []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"}
		opts.Depth = 0
		err = repo.FetchContext(ctx, opts)
	}
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetching the catalog: %w", err)
	}
	if fresh {
		// The other refs were fetched into the clone dropped.
		r.repo = repo
		clear(r.fetchedAt)
	}
	r.fetchedAt[ref] = time.Now()
	return nil
}

// objectsSize is the bytes the objects of repo, an in-memory clone, hold
// inflated.
func objectsSize(repo *git.Repository) int64 {
	iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return 0
	}
	var size int64
	_ = iter.ForEach(func(o plumbing.EncodedObject) error {
		size += o.Size()
		return nil
	})
	return size
}

// refSpec is the refspec of ref among the refs of the remote: its branch,
// else its tag, else the commit of a hash.
func refSpec(ctx context.Context, repo *git.Repository, ref string, auth transport.AuthMethod, caBundle []byte) (config.RefSpec, error) {
	remote, err := repo.Remote("origin")
	if err != nil {
		return "", err
	}
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth, CABundle: caBundle})
	if err != nil {
		return "", fmt.Errorf("listing the refs of the catalog: %w", err)
	}
	names := map[plumbing.ReferenceName]bool{}
	for _, r := range refs {
		names[r.Name()] = true
	}
	branch, tag := plumbing.NewBranchReferenceName(ref), plumbing.NewTagReferenceName(ref)
	switch {
	case names[branch]:
		return config.RefSpec(fmt.Sprintf("+%s:%s", branch, plumbing.NewRemoteReferenceName("origin", ref))), nil
	case names[tag]:
		return config.RefSpec(fmt.Sprintf("+%s:%s", tag, tag)), nil
	case isHash(ref):
		return config.RefSpec(fmt.Sprintf("%s:refs/okdp/commits/%s", ref, ref)), nil
	}
	return "", fmt.Errorf("ref %s: no such branch, tag or commit", ref)
}

// resolve returns the commit of ref: a branch, a tag or a commit hash.
func resolve(repo *git.Repository, ref string) (plumbing.Hash, error) {
	for _, name := range []plumbing.ReferenceName{plumbing.NewRemoteReferenceName("origin", ref), plumbing.NewTagReferenceName(ref)} {
		r, err := repo.Reference(name, true)
		if err != nil {
			continue
		}
		hash := r.Hash()
		if tag, err := repo.TagObject(hash); err == nil {
			commit, err := tag.Commit()
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("tag %s: %w", ref, err)
			}
			hash = commit.Hash
		}
		return hash, nil
	}
	if isHash(ref) {
		hash := plumbing.NewHash(ref)
		if _, err := repo.CommitObject(hash); err == nil {
			return hash, nil
		}
		return plumbing.ZeroHash, fmt.Errorf("commit %s is not on a branch or a tag of the repository", ref)
	}
	return plumbing.ZeroHash, fmt.Errorf("ref %s: no such branch, tag or commit", ref)
}

func isHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// readCommit reads the files of a commit.
func readCommit(repo *git.Repository, hash plumbing.Hash) (mapfs.FS, error) {
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	files := mapfs.FS{}
	total := int64(0)
	err = tree.Files().ForEach(func(f *object.File) error {
		// A regular file: a symbolic link (mode 120000, which go-git's
		// IsFile counts as a file, its content the path it points to) is
		// left out, as a local directory's are.
		if f.Mode != filemode.Regular && f.Mode != filemode.Executable {
			return nil
		}
		if f.Size > MaxFileSize {
			return fmt.Errorf("%s is %d bytes, more than the %d a catalog file may weigh", f.Name, f.Size, MaxFileSize)
		}
		total += f.Size
		if total > MaxCatalogSize {
			return fmt.Errorf("the catalog holds more than %d bytes", MaxCatalogSize)
		}
		rd, err := f.Reader()
		if err != nil {
			return err
		}
		defer func() { _ = rd.Close() }()
		data, err := io.ReadAll(io.LimitReader(rd, MaxFileSize+1))
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		files[f.Name] = &mapfs.File{Data: data, Mode: 0o444}
		return nil
	})
	return files, err
}

// LocalPath tells a url that is a local directory: an absolute path, or a
// path starting with ./ or ../ (file:// is a git repository); Loader
// refuses both in catalogs.yaml.
func LocalPath(url string) (string, bool) {
	switch {
	case filepath.IsAbs(url), strings.HasPrefix(url, "./"), strings.HasPrefix(url, "../"), url == ".", url == "..":
		return url, true
	}
	return "", false
}

// localCatalog reads the catalog of a local directory as readCommit reads
// a commit's: its regular files only, .git left out, a symbolic link not
// followed (left out, as Git's are), each file of MaxFileSize at most, its
// size known before it is read, the whole of MaxCatalogSize.
func localCatalog(name, dir string) (Catalog, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return Catalog{}, err
	}
	if !info.IsDir() {
		return Catalog{}, fmt.Errorf("%s is not a directory", dir)
	}
	files := mapfs.FS{}
	total := int64(0)
	err = filepath.WalkDir(dir, func(f string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFileSize {
			return fmt.Errorf("%s is %d bytes, more than the %d a catalog file may weigh", rel, info.Size(), MaxFileSize)
		}
		total += info.Size()
		if total > MaxCatalogSize {
			return fmt.Errorf("the catalog holds more than %d bytes", MaxCatalogSize)
		}
		data, err := readBounded(f)
		if err != nil {
			return err
		}
		files[rel] = &mapfs.File{Data: data, Mode: 0o444}
		return nil
	})
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Name: name, FS: files}, nil
}

// readBounded reads a file of MaxFileSize at most, one that grew since its
// size was read refused.
func readBounded(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("%s is more than the %d bytes a catalog file may weigh", file, MaxFileSize)
	}
	return data, nil
}
