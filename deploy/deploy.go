// Package deploy writes what a tree compiled by okdp-compiler deploys: each
// instance's umbrella chart and the files of its GitOps engine, and reads
// them back. It holds what compiling does not: where the files go, Helm's
// merge of the final values, and their checks against the upstream charts,
// which the compiler does not read.
package deploy

import (
	"context"
	"io/fs"
	"slices"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-compiler/chart"

	"github.com/okdp/okdp-gitops/internal/chartcheck"
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
	// ChartSource: where a build reads the upstream charts from, a
	// chartcache.Cache or StaticCharts.
	ChartSource = chartcheck.Source
	// Charts: the upstream charts of one build (NewCharts), each fetched
	// once, and why one could not be (Charts.Failures).
	Charts = chartcheck.Charts
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

// StaticCharts: a chart cache filled already, okdp-compile's --charts:
// nothing pulled, a chart it lacks failing the check of the instance that
// names it, with the command that pulls it.
func StaticCharts(fsys fs.FS) ChartSource { return chartcheck.Static(fsys) }

// NewCharts: the upstream charts of one build, read from src; nil for a
// nil src: none checked. A Charts is valid for one build.
func NewCharts(src ChartSource) *Charts { return chartcheck.New(src) }

// BuildOptions: compiler.BuildOptions, with what okdp-gitops adds to a
// build.
type BuildOptions struct {
	compiler.BuildOptions
	// Charts, when set, checks the final values against the upstream charts
	// (chartcheck): their schemas over their defaults, the lists an instance
	// appends to. Build fetches the charts of every Chart.yaml of the tree
	// before the compile (compiler.BuildOptions.Loaded), so that no fetch
	// counts against an instance's timeout. A chart that cannot be fetched
	// leaves its keys unchecked (Charts.Failures), and refuses the upstream
	// values a project that is not privileged gives it (CheckRendered).
	Charts *Charts
	// SkipRender leaves out the render of the charts a project gives
	// upstream values to, for a long-running process: a render past its
	// timeout keeps its goroutine, Helm's engine taking no context. The
	// CI's compile, the boundary, renders.
	SkipRender bool
}

// Build: compiler.Build with the standard rule set, Helm's merge of the
// final values and the platform's policy, checked against opts.Charts,
// unless the caller gives a rule set, a Finalize or a CheckGlobals.
func Build(ctx context.Context, repos []compiler.Repository, opts BuildOptions) (*compiler.Tree, error) {
	o := opts.BuildOptions
	o.Rules = rules.Or(o.Rules)
	if o.Finalize == nil {
		o.Finalize = helmvalues.New(opts.Charts)
		if opts.SkipRender {
			o.Finalize = helmvalues.WithoutRender(opts.Charts)
		}
	}
	if o.CheckGlobals == nil {
		o.CheckGlobals = policy.CheckGlobals
	}
	if opts.Charts != nil {
		loaded := o.Loaded
		o.Loaded = func(ctx context.Context, instances []compiler.Instance) {
			// A context ended is the build's: its compile says so.
			_ = opts.Charts.Fetch(ctx, dependencies(instances))
			if loaded != nil {
				loaded(ctx, instances)
			}
		}
	}
	return compiler.Build(ctx, repos, o)
}

// dependencies: the upstream charts of the Chart.yaml of instances, once
// each, in order.
func dependencies(instances []compiler.Instance) []chart.Ref {
	var out []chart.Ref
	for _, in := range instances {
		if in.Definition == nil {
			continue
		}
		for _, d := range in.Definition.Chart.Dependencies {
			if ref := d.Ref(); !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

// CoalesceValues: values over a chart's defaults, as Helm coalesces them
// before it renders the chart (chartutil.CoalesceValues): maps merge key by
// key, a null in values deletes the default. It changes neither argument.
func CoalesceValues(values, defaults map[string]any) (map[string]any, error) {
	return chartcheck.Coalesce(defaults, values)
}

// TreeFiles: the files of t for each target, previous what was written
// last, and the projects kept as they are (output.Files); charts, the
// upstream charts the withdrawal of a refused instance renders
// (Charts.FS), nil for none.
func TreeFiles(t *compiler.Tree, targets []Target, previous, charts fs.FS) (map[string][]byte, []compiler.KeptProject, error) {
	return output.Files(t, targets, previous, charts)
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
