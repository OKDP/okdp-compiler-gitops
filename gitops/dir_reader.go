package gitops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/okdp/okdp-gitops/treeedit"
)

// NewDirReader reads the local directory dir as the tree of a repository:
// the reads of a GitStore's clone (a path relative to dir, slash-separated;
// one that is, or goes through, a symbolic link refused with SymlinkError,
// never followed), over the billy filesystem rooted at dir (osfs) as the
// clone's is: how the compile reads a local directory in place.
//
// Of a directory it lists the names but .git; a file over MaxTreeFileSize
// is refused by its size (TooLargeError) before it is read; what is
// neither a regular file nor a directory (a socket, a named pipe) is listed
// but not read (not found), and a path below a file is none.
func NewDirReader(dir string) treeedit.Reader {
	return &dirReader{fsTx{fs: osfs.New(dir), url: dir}}
}

// dirReader holds an fsTx for its reads only: it has no write.
type dirReader struct {
	tx fsTx
}

// stat validates p (fsTx.resolve) and returns it cleaned, with its
// information: nil when nothing is there, or the path is below a file.
func (r *dirReader) stat(p string) (string, fs.FileInfo, error) {
	cleaned, err := r.tx.resolve(p)
	if err != nil {
		return "", nil, err
	}
	info, err := r.tx.fs.Lstat(cleaned)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return cleaned, nil, nil
	}
	return cleaned, info, err
}

func (r *dirReader) ReadFile(p string) ([]byte, error) {
	cleaned, info, err := r.stat(p)
	switch {
	case err != nil:
		return nil, err
	case info == nil:
		return nil, fmt.Errorf("%s: %w", cleaned, treeedit.ErrNotFound)
	case info.IsDir():
		return nil, fmt.Errorf("%s: is a directory", cleaned)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s: %w", cleaned, treeedit.ErrNotFound)
	case info.Size() > MaxTreeFileSize:
		return nil, TooLargeError(cleaned, info.Size())
	}
	return r.tx.ReadFile(cleaned)
}

func (r *dirReader) ReadDir(p string) ([]string, error) {
	cleaned, info, err := r.stat(p)
	if err != nil || info == nil || !info.IsDir() {
		return nil, err
	}
	return r.tx.ReadDir(cleaned)
}

func (r *dirReader) Exists(p string) bool {
	_, info, err := r.stat(p)
	return err == nil && info != nil
}
