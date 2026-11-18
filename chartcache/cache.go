// Package chartcache keeps a chart cache: the upstream charts the compiled
// instances name, pulled from their own repositories (OCI registries,
// https chart repositories) and unpacked whole, for the checks of a
// compile against them (deploy.Charts), the render of its policy and the
// chart defaults the console shows, with the archive as it was pulled,
// which the umbrella of an instance vendors (Archive):
//
//	<dir>/<repository without scheme>/<chart>-<version>/<chart>/             the chart unpacked: Chart.yaml, values.yaml, templates/...
//	<dir>/<repository without scheme>/<chart>-<version>/<chart>-<version>.tgz  its archive
//
// A chart is whole once its archive is there: an entry of an older cache,
// which held only Chart.yaml, values.yaml and values.schema.json of its
// chart directory, has none, and is pulled again rather than read.
package chartcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"
	"github.com/okdp/okdp-gitops/internal/chartdir"
	"github.com/okdp/okdp-gitops/internal/dirfs"
	"golang.org/x/sync/singleflight"
)

// Bounds of a chart archive: charts are kilobytes. The compressed stream is
// bounded where it is pulled, the inflated archive, each file and their
// number here, since a gzip bomb fits in a few kilobytes.
const (
	MaxChartSize        = 32 << 20
	MaxUncompressedSize = 256 << 20
	MaxFileSize         = 4 << 20
	MaxFiles            = 16384
)

// FetchTimeout bounds one chart pull: a registry that accepts the
// connection and never answers must not pin a compile.
const FetchTimeout = 60 * time.Second

// retryAfter: a chart that could not be pulled is not tried again before.
const retryAfter = time.Minute

// Files are the files of a chart at its root that File reads.
var Files = []string{"Chart.yaml", "values.yaml", "values.schema.json"}

// Puller pulls the archive (.tgz) of a chart version and hands it to read.
type Puller interface {
	Pull(ctx context.Context, ref chart.Ref, read func(io.Reader) error) error
}

// Cache is a chart cache directory, the chart source of a build
// (deploy.ChartSource): Get pulls a chart it lacks, FS gives them. Safe
// for concurrent use.
type Cache struct {
	Dir    string
	puller Puller

	inflight singleflight.Group
	failures sync.Map // chart dir -> time.Time of the last failure
}

// NewCache keeps the charts under dir, pulled with puller.
func NewCache(dir string, puller Puller) *Cache {
	return &Cache{Dir: dir, puller: puller}
}

// ChartDir is the directory of a chart in the cache, slash-separated:
// <repository without scheme>/<chart>-<version>/<chart> (chartdir.Of). A
// repository, a chart or a version that cannot be a directory of the
// cache is refused.
func ChartDir(ref chart.Ref) (string, error) { return chartdir.Of(ref) }

// FS is the cache as the checks of a compile read it (Source), bounded
// and with no symbolic link followed, as okdp-compile reads its --charts: a
// link refused wherever it points, a file that is neither regular nor a
// directory refused, a file over 4 MiB, or the files read of one chart
// through one FS over 64 MiB together, refused (dirfs.NewCharts): each
// chart has its own budget, so that the charts one project names do not
// exhaust the others'. A cache directory that cannot be opened (none yet)
// gives an FS whose every file fails with that error.
func (c *Cache) FS() fs.FS {
	fsys, err := dirfs.NewCharts("the chart cache", c.Dir)
	if err != nil {
		return errFS{err}
	}
	return fsys
}

// errFS: an fs.FS whose every Open fails with err.
type errFS struct{ err error }

func (e errFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: e.err}
}

// Archive: the chart archive of ref (.tgz) as it was pulled, and its
// digest, sha256:<hex>, the OCI layer's of an OCI chart (Get pulls it
// first when the cache lacks it): what the umbrella of an instance
// vendors (deploy, helm.Umbrella.Charts). Read from the cache directory,
// not through FS: MaxChartSize bounds it, not the bound of a file there.
func (c *Cache) Archive(ctx context.Context, ref chart.Ref) ([]byte, string, error) {
	if err := c.Get(ctx, ref); err != nil {
		return nil, "", err
	}
	dir, _ := ChartDir(ref)
	b, err := os.ReadFile(archivePath(filepath.Join(c.Dir, filepath.FromSlash(dir)), ref))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// archivePath: the archive of ref beside its chart directory dir:
// <dir's parent>/<chart>-<version>.tgz. Its presence is what says the chart
// is whole (Get).
func archivePath(dir string, ref chart.Ref) string {
	return filepath.Join(filepath.Dir(dir), ref.Chart+"-"+ref.Version+".tgz")
}

// File returns a file of a chart, one of Files (values.yaml...), pulled
// first when the cache lacks it, read through FS. A chart without the file
// answers fs.ErrNotExist; a name not in Files is refused.
func (c *Cache) File(ctx context.Context, ref chart.Ref, name string) ([]byte, error) {
	if !slices.Contains(Files, name) {
		return nil, fmt.Errorf("%q is no file of a chart the cache keeps: %s", name, strings.Join(Files, ", "))
	}
	if err := c.Get(ctx, ref); err != nil {
		return nil, err
	}
	dir, _ := ChartDir(ref)
	fsys, err := dirfs.NewCharts("the chart cache", c.Dir)
	if err != nil {
		return nil, err
	}
	defer fsys.Close()
	return fs.ReadFile(fsys, dir+"/"+name)
}

// Get pulls a chart into the cache unless it is there. Concurrent calls for
// one chart share the pull; a failed pull is not tried again for a minute.
func (c *Cache) Get(ctx context.Context, ref chart.Ref) error {
	dir, err := ChartDir(ref)
	if err != nil {
		return err
	}
	full := filepath.Join(c.Dir, filepath.FromSlash(dir))
	if _, err := os.Stat(archivePath(full, ref)); err == nil {
		return nil
	}
	if at, failed := c.failures.Load(dir); failed && time.Since(at.(time.Time)) < retryAfter {
		return fmt.Errorf("%s %s of %s could not be pulled a moment ago", ref.Chart, ref.Version, ref.Repository)
	}
	result := c.inflight.DoChan(dir, func() (any, error) {
		pullCtx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
		defer cancel()
		err := c.pull(pullCtx, ref, full)
		if err != nil {
			c.failures.Store(dir, time.Now())
		} else {
			c.failures.Delete(dir)
		}
		return nil, err
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-result:
		return r.Err
	}
}

// pull unpacks a chart into a temporary directory, then moves it in place;
// a pull that fails leaves no directory of the chart (its parent, created
// for it, removed when it is left empty).
func (c *Cache) pull(ctx context.Context, ref chart.Ref, dir string) (err error) {
	if c.puller == nil {
		return errors.New("no chart puller")
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(parent) // only when empty
		}
	}()
	tmp, err := os.MkdirTemp(parent, ".pull-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	var pulled []byte
	err = c.puller.Pull(ctx, ref, func(archive io.Reader) error {
		// Whole, bounded, before any of it is unpacked: the archive is what
		// is vendored, byte for byte.
		data, err := io.ReadAll(io.LimitReader(archive, MaxChartSize+1))
		if err != nil {
			return fmt.Errorf("reading the chart: %w", err)
		}
		if int64(len(data)) > MaxChartSize {
			return fmt.Errorf("the chart is more than the %d bytes a chart may weigh", MaxChartSize)
		}
		pulled = data
		return unpack(bytes.NewReader(data), ref.Chart, tmp)
	})
	if err != nil {
		return fmt.Errorf("pulling %s %s of %s: %w", ref.Chart, ref.Version, ref.Repository, err)
	}
	if err := checkChart(tmp, ref); err != nil {
		return fmt.Errorf("pulling %s %s of %s: %w", ref.Chart, ref.Version, ref.Repository, err)
	}
	// Readable by every user of a shared cache: a pull's job of another
	// uid reads what this one pulled (MkdirTemp creates it 0700).
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	archive := archivePath(dir, ref)
	if _, statErr := os.Stat(archive); statErr == nil {
		return nil // whole, pulled by another process meanwhile
	}
	// An entry of an older cache, its three files alone, or a pull cut
	// between the rename and the archive: replaced whole.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil && !errors.Is(err, fs.ErrExist) {
		if _, statErr := os.Stat(archive); statErr == nil {
			return nil // unpacked by another process meanwhile
		}
		return err
	}
	// The archive last, atomically: it says the chart is whole.
	af, err := os.CreateTemp(parent, ".archive-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(af.Name()) }()
	if _, err := af.Write(pulled); err != nil {
		_ = af.Close()
		return err
	}
	if err := af.Chmod(0o644); err != nil {
		_ = af.Close()
		return err
	}
	if err := af.Close(); err != nil {
		return err
	}
	return os.Rename(af.Name(), archive)
}

// checkChart: nil when dir holds the Chart.yaml of ref: its name the
// chart, its version the version, as the ref names them; an archive of
// another chart, or of another version, refused.
func checkChart(dir string, ref chart.Ref) error {
	b, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return fmt.Errorf("the archive holds no %s/Chart.yaml", ref.Chart)
	}
	meta, err := chart.ParseValues(b)
	if err != nil {
		return fmt.Errorf("its Chart.yaml: %w", err)
	}
	if name, version := value.Str(meta["name"]), value.Str(meta["version"]); name != ref.Chart || version != ref.Version {
		return fmt.Errorf("its Chart.yaml is of %q %q, not the chart named", name, version)
	}
	return nil
}

// unpack writes the files of the chart directory of a chart archive under
// dir, <name>/<path> at <path>: regular files only, each of MaxFileSize at
// most, MaxFiles at most, a path out of the chart (.., absolute) skipped,
// symbolic links never followed: whatever is not a regular file skipped.
func unpack(r io.Reader, name, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("the chart is not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(io.LimitReader(gz, MaxUncompressedSize))
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the chart archive: %w", err)
		}
		entry := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		rel, ok := strings.CutPrefix(entry, name+"/")
		if hdr.Typeflag != tar.TypeReg || !ok || !filepath.IsLocal(rel) {
			continue
		}
		files++
		if files > MaxFiles {
			return fmt.Errorf("the chart archive holds more than %d files", MaxFiles)
		}
		if hdr.Size > MaxFileSize {
			return fmt.Errorf("%s is %d bytes, more than the %d it may weigh", hdr.Name, hdr.Size, MaxFileSize)
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxFileSize+1))
		if err != nil {
			return fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		if int64(len(data)) > MaxFileSize {
			return fmt.Errorf("%s is more than %d bytes", hdr.Name, MaxFileSize)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
}
