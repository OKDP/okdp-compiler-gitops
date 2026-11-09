package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/okdp/okdp-gitops/treeedit"
	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/gitops"
)

func init() {
	// Serve file:// in process: the tests need no git binary.
	client.InstallProtocol("file", server.DefaultServer)
}

// Another run pushes the output while okdp-gitops compiles, of a newer
// commit of the sources (a console change): the compile of the old sources
// is not pushed over it, the tree is compiled again from the new revisions.
func TestACompileRacingACommitCompilesItsSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "deployments.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)
	url := "file://" + remote
	output := filepath.Join(dir, "okdp-output.git")
	_, err = git.PlainInit(output, true)
	require.NoError(t, err)
	outputURL := "file://" + output
	other, err := gitops.NewGitStore(gitops.GitOptions{URL: outputURL, Branch: "main"})
	require.NoError(t, err)

	person, err := gitops.NewGitStore(gitops.GitOptions{URL: url, Branch: "main", AuthorName: "a person", AuthorEmail: "person@example.com"})
	require.NoError(t, err)
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	_, err = person.Update(ctx, gitops.Commit{Subject: "the sources"}, func(tx gitops.Tx) error {
		for p, content := range tree {
			if err := tx.WriteFile(p, []byte(content)); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	config := "projects/demo/instances/legacy/config.yaml"
	writes := 0
	beforeWrite = func() {
		writes++
		if writes > 1 {
			return
		}
		_, err := person.Update(ctx, gitops.Commit{Subject: "a change of the sources"}, func(tx gitops.Tx) error {
			return tx.WriteFile(config, []byte("database: {class: large}\n"))
		})
		require.NoError(t, err)
		_, err = other.Update(ctx, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error {
			return tx.WriteFile("compiled/projects/kustomization.yaml", []byte("resources: []\n"))
		})
		require.NoError(t, err)
	}
	t.Cleanup(func() { beforeWrite = func() {} })

	topology := filepath.Join(dir, "topology.yaml")
	write(t, topology, "platform: {url: '"+url+"'}\nprojects: {url: '"+url+"', path: 'projects/{project}'}\noutput: {url: '"+outputURL+"'}\n")
	args := []string{"compile", "--config", topology, "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args))
	require.Equal(t, 2, writes, "compiled once, then again from the new revisions")

	require.NoError(t, other.Sync(ctx))
	require.NoError(t, other.View(ctx, func(r treeedit.Reader) error {
		values, err := r.ReadFile("compiled/projects/demo/instances/demo-legacy/instance.values.yaml")
		if err != nil {
			return err
		}
		if !strings.Contains(string(values), "okdp-objects:") {
			return errors.New("no objects compiled")
		}
		return nil
	}), "the compile of the new sources, a claim, is pushed")
	require.Equal(t, 0, run(append(args, "--check")))
}
