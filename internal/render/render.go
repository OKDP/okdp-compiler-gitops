// Package render renders the chart of one key of an instance with its
// final values, for the platform's policy on what it would deploy
// (internal/helmvalues): the objects of its manifests, hooks and crds/
// included, documents split, nothing else of a release (no cluster
// client, no lookup: no state). The manifests are checked, never written.
package render

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/releaseutil"

	"github.com/okdp/okdp-compiler/chart"

	"github.com/okdp/okdp-compiler/value"
	"github.com/okdp/okdp-gitops/internal/chartdir"

	"github.com/okdp/okdp-gitops/internal/yamldoc"
)

// Timeout: how long one render may take by default. Helm's engine takes no
// context, so a chart that runs past it is left running, its goroutine
// abandoned: a template that loops holds a goroutine, not the compile.
const Timeout = 30 * time.Second

// maxOutput bounds the manifests of one render, in bytes.
const maxOutput = 64 << 20

// Request: one key of an instance, what renders it.
type Request struct {
	Charts    fs.FS     // the chart cache: <chartdir.Of(Ref)>/ the chart unpacked whole
	Ref       chart.Ref // the chart
	Key       string    // the key the chart renders under (its alias in the umbrella)
	Values    map[string]any
	Release   string
	Namespace string
	Timeout   time.Duration // 0: Timeout
}

// Objects: the objects of the manifests of the chart of r, as `helm
// template --include-crds` gives them, its Helm hooks among them: the
// templates rendered (NOTES.txt ignored, the crds/ of the chart and of its
// subcharts added as they are), documents split, each a map, those that are
// not one dropped. The first error fails it: a chart that does not load, a
// template that fails, a manifest that does not parse, a render past its
// timeout, over maxOutput; an object or a host the caller refuses is the
// caller's.
func Objects(r Request) ([]map[string]any, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	type outcome struct {
		objects []map[string]any
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{err: fmt.Errorf("rendering panicked: %v", p)}
			}
		}()
		objects, err := objects(r)
		done <- outcome{objects, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case o := <-done:
		return o.objects, o.err
	case <-timer.C:
		return nil, fmt.Errorf("rendering took more than %s", timeout)
	}
}

// objects renders r, as Objects says, but for the timeout.
func objects(r Request) ([]map[string]any, error) {
	files, err := read(r.Charts, r.Ref)
	if err != nil {
		return nil, err
	}
	chrt, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("loading the chart: %w", err)
	}
	// Under its alias, as the umbrella renders it (.Chart.Name).
	if r.Key != "" {
		chrt.Metadata.Name = r.Key
	}
	// A copy: rendering writes the values it coalesces.
	values := value.AsMap(value.Copy(r.Values))
	if err := chartutil.ProcessDependenciesWithMerge(chrt, values); err != nil {
		return nil, fmt.Errorf("the dependencies of the chart: %w", err)
	}
	options := chartutil.ReleaseOptions{Name: r.Release, Namespace: r.Namespace, Revision: 1, IsInstall: true}
	rendered, err := chartutil.ToRenderValuesWithSchemaValidation(chrt, values, options, capabilities(), true)
	if err != nil {
		return nil, fmt.Errorf("the values of the chart: %w", err)
	}
	manifests, err := engine.Render(chrt, rendered)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	size := 0
	for _, name := range slices.Sorted(maps.Keys(manifests)) {
		if path.Base(name) == "NOTES.txt" {
			continue
		}
		size += len(manifests[name])
		if size > maxOutput {
			return nil, fmt.Errorf("the manifests are more than %d bytes", maxOutput)
		}
		objects, err := decode(name, manifests[name])
		if err != nil {
			return nil, err
		}
		out = append(out, objects...)
	}
	for _, crd := range chrt.CRDObjects() {
		size += len(crd.File.Data)
		if size > maxOutput {
			return nil, fmt.Errorf("the manifests are more than %d bytes", maxOutput)
		}
		objects, err := decode(crd.Filename, string(crd.File.Data))
		if err != nil {
			return nil, err
		}
		out = append(out, objects...)
	}
	return out, nil
}

// read: the files of the chart of ref in charts, each a loader's
// BufferedFile named relative to the chart.
func read(charts fs.FS, ref chart.Ref) ([]*loader.BufferedFile, error) {
	dir, err := chartdir.Of(ref)
	if err != nil {
		return nil, err
	}
	var out []*loader.BufferedFile
	err = fs.WalkDir(charts, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(charts, p)
		if err != nil {
			return err
		}
		out = append(out, &loader.BufferedFile{Name: strings.TrimPrefix(p, dir+"/"), Data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the chart: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("the chart holds no file")
	}
	return out, nil
}

// decode: the objects of the manifest text of name, its YAML documents
// split, comments and empty documents dropped, those that are not a map
// dropped; a document that does not parse is an error, named.
func decode(name, text string) ([]map[string]any, error) {
	docs := releaseutil.SplitManifests(text)
	var out []map[string]any
	for i := 0; ; i++ {
		doc, ok := docs[fmt.Sprintf("manifest-%d", i)]
		if !ok {
			return out, nil
		}
		v, err := yamldoc.Decode([]byte(doc))
		if err != nil {
			return nil, fmt.Errorf("%s: not valid YAML: %w", name, err)
		}
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			out = append(out, m)
		}
	}
}

// capabilities: the capabilities of a render: Helm's defaults, and the API
// groups that gate objects the policy reads (a chart renders its Ingress,
// its Certificate, its route only when it sees their group).
func capabilities() *chartutil.Capabilities {
	caps := *chartutil.DefaultCapabilities
	caps.APIVersions = slices.Concat(chartutil.DefaultVersionSet, chartutil.VersionSet{"cert-manager.io/v1", "gateway.networking.k8s.io/v1"})
	return &caps
}
