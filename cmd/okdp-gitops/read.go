package main

import (
	"errors"
	"path"
	"strings"

	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/gitops"
)

// projectsOf decodes the ProjectsFile at p of files: none listed when it is
// absent; nil when it does not decode (whose error the compile reports),
// every repository of the projects then read whole.
func projectsOf(files map[string]string, p string) *treeedit.ProjectsList {
	raw, ok := files[p]
	if !ok {
		return &treeedit.ProjectsList{}
	}
	list, err := treeedit.DecodeProjects([]byte(raw))
	if err != nil {
		return nil
	}
	return list
}

// readPaths copies the files under paths of r (gitops.Topology.LayoutPaths)
// into files, each of gitops.MaxTreeFileSize at most, and the symbolic links
// into links, which it does not follow (gitops.ErrSymlink): a path that
// goes through one, its first component that is. It is the CLI's one reader:
// r is the snapshot of a git repository, or gitops.NewDirReader of a local
// directory, which both refuse a link and the size of a file before they read it.
func readPaths(r treeedit.Reader, paths []string, files map[string]string, links map[string]bool) error {
	for _, p := range paths {
		if link := linkOn(r, p); link != "" {
			links[link] = true
			continue
		}
		if err := readEntry(r, p, files, links); err != nil {
			return err
		}
	}
	return nil
}

// linkOn returns the first component of p, but p itself, that is a
// symbolic link of r, "" when none is.
func linkOn(r treeedit.Reader, p string) string {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		q := strings.Join(parts[:i], "/")
		if _, err := r.ReadDir(q); errors.Is(err, gitops.ErrSymlink) {
			return q
		}
	}
	return ""
}

// readEntry copies p of r into files: a file, else the files under the
// directory, a symbolic link into links; nothing when p is not.
func readEntry(r treeedit.Reader, p string, files map[string]string, links map[string]bool) error {
	data, err := r.ReadFile(p)
	switch {
	case err == nil:
		if len(data) > gitops.MaxTreeFileSize {
			return gitops.TooLargeError(p, int64(len(data)))
		}
		files[p] = string(data)
		return nil
	case errors.Is(err, gitops.ErrSymlink):
		links[p] = true
		return nil
	case errors.Is(err, gitops.ErrTooLarge):
		return err
	}
	names, err := r.ReadDir(p)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := readEntry(r, path.Join(p, n), files, links); err != nil {
			return err
		}
	}
	return nil
}
