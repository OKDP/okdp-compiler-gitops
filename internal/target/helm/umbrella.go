// Package helm writes a compile as the files of okdp-gitops-compile (Write):
// one chart for the whole instance, an umbrella over the upstream charts of
// its keys and the chart of OKDP's own objects, for one Helm release per instance under
// the release name its values were compiled for, and what the instance
// publishes. The caller writes them (the CLI to a directory, a server to
// git).
package helm

import (
	"bytes"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-gitops/internal/target"
)

// The files Write gives besides Chart.yaml.
const (
	// ValuesFile: the umbrella's values file, given to Helm as user values
	// (helm -f, Argo CD valueFiles, Flux valuesFrom). As the chart's own
	// values.yaml they would be defaults, and Helm drops the nulls of
	// defaults: a null that deletes an upstream default would be lost.
	ValuesFile = "instance.values.yaml"
	// InfoFile: Result.Info, what the instance publishes: url,
	// usage, and provides, the connections it provides, each key written
	// when set, what a control plane or a console reads of it in the
	// deployments repository. Written when it publishes something. Helm
	// ignores it.
	InfoFile = compiler.InfoFile
)

// Write: the umbrella of res, path -> content, what the flux and argocd
// targets deploy as one release:
//
//   - Chart.yaml, the chart of the instance: the package's Chart.yaml
//     (Result.Chart), its fields kept, with one dependency per compiled
//     key: first those of Chart.yaml, in its order, each as written; then
//     the okdp-objects chart (Result.ObjectsSource), when res has objects or
//     OAuth clients;
//   - ValuesFile, the final values of each key under the key, each with its
//     own global, marked generated (target.Generated), and, when res has
//     objects, Result.Objects as one YAML stream, one document per object,
//     under compiler.ObjectsChart, compiler.ObjectsValue: a string, so that Helm
//     changes neither the {{ of an ExternalSecret's template, nor a null,
//     nor an integer beyond the precision of a float64; an object Helm
//     keeps given Argo CD's sync options Prune=false,Delete=false (kept);
//   - InfoFile, when the instance publishes something: a URL, a usage
//     or a connection.
//
// The YAML files are indented by 2 spaces. Every file is built before
// Write returns: one that fails leaves the caller nothing to write. res is
// what Compile returned: Write does not check it again.
func Write(res *compiler.Result) (map[string][]byte, error) {
	values := compiler.ChartValues(res)
	objects, err := compiler.ObjectsChartValues(kept(res))
	if err != nil {
		return nil, err
	}
	// The rendered dependencies of Chart.yaml, in its order: a dependency
	// the mapping leaves out is not listed, its condition has no use here.
	c := res.Chart
	c.Dependencies = nil
	for _, d := range res.Chart.Dependencies {
		if _, ok := values[d.Key()]; ok {
			d.Condition = nil
			c.Dependencies = append(c.Dependencies, d)
		}
	}
	if objects != nil {
		ref := res.ObjectsSource
		d := chart.Dependency{Name: ref.Chart, Repository: ref.Repository, Version: ref.Version}
		if compiler.ObjectsChart != ref.Chart {
			d.Alias = compiler.ObjectsChart
		}
		c.Dependencies = append(c.Dependencies, d)
		values[compiler.ObjectsChart] = objects
	}
	docs := map[string][]any{"Chart.yaml": {c}, ValuesFile: {values}}
	if i := res.Info; i.URL != "" || i.Usage != "" || len(i.Provides) > 0 {
		docs[InfoFile] = []any{i}
	}
	files := map[string][]byte{}
	for name, vs := range docs {
		b, err := encode(vs)
		if err != nil {
			return nil, err
		}
		if name == ValuesFile {
			b = append([]byte(target.Generated), b...)
		}
		files[name] = b
	}
	return files, nil
}

// encode: vs as a YAML stream, one document each, indented by 2 spaces.
func encode(vs []any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, v := range vs {
		if err := enc.Encode(v); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// ReleaseMax: the longest release of a plain install: Helm's limit of 53 less
// the "-okdp" of the release of its objects.
const ReleaseMax = 48

// The files of a compile for a plain helm install, the compiler's (Write).
const (
	PlainValues   = compiler.ValuesFile
	ObjectsValues = compiler.ObjectsValuesFile
)

// WriteValues: what a plain helm install of res needs, the compiler's,
// an object Helm keeps given Argo CD's sync options as Write gives it
// (kept).
func WriteValues(res *compiler.Result) (map[string][]byte, error) { return compiler.Write(kept(res)) }
