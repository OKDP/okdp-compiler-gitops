// Package deploy writes what a tree compiled by okdp-compiler deploys: each
// instance's umbrella chart and the files of its GitOps engine, and reads
// them back. It holds what compiling does not: where the files go, and
// Helm's merge of the final values.
package deploy

import (
	"context"
	"io/fs"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/internal/helmvalues"
	"github.com/okdp/okdp-gitops/internal/output"
	"github.com/okdp/okdp-gitops/internal/policy"
	"github.com/okdp/okdp-gitops/internal/target"
	"github.com/okdp/okdp-gitops/internal/target/argocd"
	"github.com/okdp/okdp-gitops/internal/target/flux"
	"github.com/okdp/okdp-gitops/internal/target/helm"
	"github.com/okdp/okdp-gitops/rules"
)

type (
	// Target: what is written for a compiled tree.
	Target = target.Target
	// Helm: what a plain helm install of each instance needs: values only.
	Helm = helm.Target
	// Umbrella: one chart per instance, what Flux and Argo CD deploy.
	Umbrella = helm.Umbrella
	// Flux: the Helm target's files, with Flux's.
	Flux = flux.Target
	// ArgoCD: the Helm target's files, with Argo CD's.
	ArgoCD = argocd.Target
	// ReleaseError: a release name in two namespaces or more.
	ReleaseError = target.ReleaseError
	// ArgoSource: the repository and revision Argo CD reads a tree from.
	ArgoSource = argocd.Source
	// Published: what a compiled instance publishes, read back.
	Published = helm.Published
	// Compiled: an instance's umbrella chart, read back.
	Compiled = helm.Compiled
	// ChartArchives: the archive of a chart, what an umbrella vendors.
	ChartArchives = helm.ChartArchives
)

const (
	// UmbrellaValues: the values file of an instance's umbrella.
	UmbrellaValues = helm.ValuesFile
	// PlainValues: the values of the catalog's chart, for a plain helm install.
	PlainValues = helm.PlainValues
	// ObjectsValues: the values of the okdp-objects chart.
	ObjectsValues = helm.ObjectsValues
	// InfoFile: what an instance publishes.
	InfoFile = helm.InfoFile
	// DefaultFluxSource: the GitRepository of the flux target by default.
	DefaultFluxSource = flux.DefaultSource
)

// Build: compiler.Build with the standard rule set and Helm's merge of the
// final values, unless the caller gives others.
func Build(ctx context.Context, repos []compiler.Repository, opts compiler.BuildOptions) (*compiler.Tree, error) {
	opts.Rules = rules.Or(opts.Rules)
	if opts.Finalize == nil {
		opts.Finalize = helmvalues.New()
	}
	if opts.CheckGlobals == nil {
		opts.CheckGlobals = policy.CheckGlobals
	}
	return compiler.Build(ctx, repos, opts)
}

// WithoutRender: a BuildOptions.Finalize of Build's own, without the render
// of the charts a project gives upstream values to (internal/render) and so
// the refusal of those values when their chart could not be fetched; the
// other checks are kept. For a long-running process only: the CI's compile,
// Build's default, is the boundary (gitops.Compiler.SkipRender).
func WithoutRender() compiler.Finalize { return helmvalues.WithoutRender() }

// TreeFiles: the files of t for each target, previous what was written
// last, and the projects kept as they are (output.Files).
func TreeFiles(t *compiler.Tree, targets []Target, previous fs.FS) (map[string][]byte, []compiler.KeptProject, error) {
	return output.Files(t, targets, previous)
}

// ReleaseTaken: the compiled instance of t, of another namespace, whose
// release the instance name of namespace has too; nil when none.
func ReleaseTaken(t *compiler.Tree, namespace, name string) *compiler.TreeInstance {
	return output.ReleaseTaken(t, namespace, name)
}

// CheckReleases: nil when every release name of results is in one namespace.
func CheckReleases(results []*compiler.Result) error { return target.Releases(results) }

// ProjectDir: projects/<namespace>/.
func ProjectDir(namespace string) string { return target.ProjectDir(namespace) }

// InstanceDir: the directory of an instance's files.
func InstanceDir(namespace, release string) string { return target.InstanceDir(namespace, release) }

// ProjectManifestsDir: the directory of a project's manifests.
func ProjectManifestsDir(namespace string) string { return target.ProjectManifests(namespace) }

// ReadArgoRoot: the ArgoSource of an Argo CD entry point.
func ReadArgoRoot(raw []byte) (*ArgoSource, error) { return argocd.ReadRoot(raw) }

// ReadPublished: what the compiled instances of namespace publish in output.
func ReadPublished(output fs.FS, namespace string) ([]Published, error) {
	return helm.ReadPublished(output, namespace)
}

// ReadCompiled: an instance's umbrella in output.
func ReadCompiled(output fs.FS, namespace, release string) (*Compiled, error) {
	return helm.ReadCompiled(output, namespace, release)
}

// Write: the files of an instance's compile for a plain helm install, path
// -> content: values only, the chart being the catalog's.
func Write(res *compiler.Result) (map[string][]byte, error) { return helm.WriteValues(res) }

// ProjectAccess: the roles of the groups of the project of namespace, file
// its project.yaml as written (nil: none), for a program that mirrors the
// project's authority. set is the rule set it compiles with; nil, the
// standard one, as Build.
func ProjectAccess(set *compiler.RuleSet, file []byte, namespace string) (map[string]compiler.AccessRole, error) {
	set = rules.Or(set)
	if err := compiler.CheckProjectName(context.Background(), set, namespace, nil); err != nil {
		return nil, err
	}
	values, err := compiler.ParseValues(file)
	if err != nil {
		return nil, err
	}
	return compiler.ParseAccess(set, values[compiler.ProjectAccessKey], namespace)
}
