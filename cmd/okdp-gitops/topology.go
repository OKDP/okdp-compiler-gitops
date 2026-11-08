package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/gitops"
)

// localTopology is the io topology of two local directories (--tree,
// --output): the platform and the projects in tree, the output in
// output, never the same one.
func localTopology(tree, output string) (*gitops.Topology, error) {
	absTree, err := filepath.Abs(tree)
	if err != nil {
		return nil, err
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if absTree == absOutput {
		return nil, fmt.Errorf("--tree and --output are one directory, %s: the output is in a directory of its own", tree)
	}
	local := func(dir string) string {
		dir = filepath.ToSlash(filepath.Clean(dir))
		if !gitops.IsLocal(dir) {
			dir = "./" + dir
		}
		return dir
	}
	t := &gitops.Topology{
		Platform: gitops.Location{URL: local(tree), Branch: "main"},
		Projects: gitops.Location{URL: local(tree), Branch: "main", Path: treeedit.ProjectDir(gitops.ProjectPlaceholder)},
		Output:   gitops.Location{URL: local(output), Branch: "main"},
	}
	return t, t.Validate()
}

// gitPrefix is the path of dir in its Git repository, "" outside one.
func gitPrefix(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-prefix").Output()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(out)), "/")
}
