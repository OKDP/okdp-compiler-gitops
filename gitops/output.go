package gitops

import (
	"context"
	"errors"
	"github.com/okdp/okdp-gitops/deploy"
	"io/fs"
	"log/slog"
	"strings"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-gitops/treeedit"
)

// ArgoCDRootPath is the Application of the output that Argo CD syncs
// first, written by hand (the engine's entry point): it says where Argo CD
// reads the output from (spec.source.repoURL and targetRevision), which the
// Application of each instance reads too (deploy.ReadArgoRoot). An
// output without it is deployed by Flux only: it has no Applications. It
// is okdp-compiler's (treeedit.ArgoCDRootPath).
const ArgoCDRootPath = treeedit.ArgoCDRootPath

// WriteCompiled compiles the tree tx holds (Compiler.Compile) and writes
// its output, the work okdp-gitops compile does for the CI of the output
// on each of its runs, or for a person: compiled/ rebuilt, the files
// of okdp-compiler's Flux and Argo CD targets (deploy.TreeFiles, Argo CD
// when argocd/root.yaml says where it reads the output from), what no
// instance produces any more removed. The paths the engines read are
// under OutputPrefix.
//
// A project of an instance that does not deploy, whose release name an
// instance of another project has too (both projects kept, a
// *deploy.ReleaseError each), or that depends on an instance of such a
// project, keeps its output as it was, the files the output has of it,
// and is returned, with why, in name order; the other projects' output
// is written (each a compiler.KeptProject: Problems say why, an instance
// each). A problem of the whole tree writes nothing, and is the
// error: a *compiler.LoadError when the tree does not load (a platform file
// that does not parse, a package no catalog has; a project's own load
// problem refuses that project alone, kept like the others), a catalog
// that cannot be read, a
// release of the output a kept project keeps that a project written has
// too, or that two kept outputs hold.
func (d *Deployments) WriteCompiled(ctx context.Context, tx Tx) ([]compiler.KeptProject, error) {
	if d.Compiler == nil {
		return nil, errors.New("no compiler: the catalogs are unknown")
	}
	tree, err := d.Compiler.Compile(ctx, tx)
	if err != nil {
		return nil, err
	}
	targets, err := treeedit.OutputTargets(tx, d.outputPrefix(), d.chartArchives(ctx))
	if err != nil {
		return nil, err
	}
	files, kept, err := deploy.TreeFiles(tree, targets, treeedit.OutputFS(tx), d.chartsFS())
	if err != nil {
		return nil, err
	}
	for _, ti := range tree.Instances {
		if ti.Outcome.Result == nil {
			continue
		}
		for _, w := range ti.Outcome.Result.Warnings {
			slog.Warn(w, "instance", ti.Namespace()+"/"+ti.Name)
		}
	}
	if err := treeedit.WriteOutput(tx, files); err != nil {
		return nil, err
	}
	return kept, nil
}

// chartArchiver: a chart source that keeps the archives it pulled
// (chartcache.Cache), what the umbrellas vendor.
type chartArchiver interface {
	Archive(ctx context.Context, ref chart.Ref) ([]byte, string, error)
}

// chartArchives: the archives of the Compiler's charts; nil when the
// source does not keep them (no chart cache: nothing vendored).
func (d *Deployments) chartArchives(ctx context.Context) deploy.ChartArchives {
	a, ok := d.Compiler.Charts.(chartArchiver)
	if !ok {
		return nil
	}
	return func(ref chart.Ref) ([]byte, string, error) { return a.Archive(ctx, ref) }
}

// chartsFS: the charts of the Compiler's chart source, what the
// withdrawal of a refused instance renders; nil without a source.
func (d *Deployments) chartsFS() fs.FS {
	if d.Compiler.Charts == nil {
		return nil
	}
	return d.Compiler.Charts.FS()
}

// outputPrefix is the directory of the output in its repository: the
// paths the engines read the compiled tree at.
func (d *Deployments) outputPrefix() string { return strings.Trim(d.OutputPrefix, "/") }
