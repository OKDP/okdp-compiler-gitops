package gitops

import "github.com/okdp/okdp-gitops/treeedit"

// The tree is okdp-compiler's (tree.Load, package
// treeedit): platform.yaml, beside it treeedit.CatalogsFile and, in a
// split topology, treeedit.ProjectsFile, and projects/<project>/ with
// its project.yaml and its instances; its names and its paths are the
// compiler's (tree.PlatformFile, treeedit.ProjectDir...). The output,
// CompiledDir, is what a compile writes, in a repository of its own.
const (
	// CompiledDir holds the output: the files of okdp-compiler's Flux and
	// Argo CD targets, as they write them (compiled/projects/<project>/...).
	// Never edited by hand. It is okdp-compiler's (treeedit.CompiledDir).
	CompiledDir = treeedit.CompiledDir
	// SourcesFile, beside CompiledDir in the output, records what
	// okdp-gitops compile compiled it from: the revisions of the
	// repositories of the sources and of the catalogs, the okdp-gitops
	// version, digests of the topology and of the output. A run whose
	// sources did not move stops early. Never edited by hand.
	SourcesFile = "sources.yaml"
)
