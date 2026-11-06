package helm

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-gitops/internal/target"
)

// Target: the helm target: what a plain helm install of each instance needs,
// WriteValues in its target.InstanceDir, no chart: the catalog's package is
// installed as it is, helm install <release> <package> -f values.yaml, after
// helm install <release>-okdp okdp-objects -f okdp-objects.values.yaml.
type Target struct{}

// Files: the files of WriteValues of each result, in its target.InstanceDir.
// A release too long for the release of its objects is refused. The graph
// is not read.
func (Target) Files(results []*compiler.Result, _ *compiler.Graph) (map[string][]byte, error) {
	return files(results, func(res *compiler.Result) (map[string][]byte, error) {
		written, err := WriteValues(res)
		if _, ok := written[ObjectsValues]; ok && len(res.Release) > ReleaseMax {
			return nil, fmt.Errorf("the release is longer than %d characters: the release of its objects adds -okdp, and Helm takes 53", ReleaseMax)
		}
		return written, err
	})
}

// ChartArchives: the archive of a chart as a compile fetched it (its .tgz)
// and its digest, sha256:<hex> (chartcache.Cache.Archive): what the
// umbrella of an instance vendors, an error when the compile has none.
type ChartArchives func(ref chart.Ref) (archive []byte, digest string, err error)

// VendorMaxSize bounds a chart archive the umbrella vendors, in bytes: a
// file of the output repository under treeedit.MaxTreeFileSize, which a
// compile reads whole. A chart over it is deployed from its repository, its
// digest recorded all the same.
const VendorMaxSize = 4 << 20

// DigestsAnnotation: the annotation of an umbrella's Chart.yaml holding
// the digest of each chart it vendors or was checked against:
// <name>-<version>=sha256:<hex>, one per chart, comma separated.
const DigestsAnnotation = "okdp.io/chart-digests"

// Umbrella: one chart per instance, what Write gives, in its
// target.InstanceDir: a deployer installs one release of the package's
// charts and of the okdp-objects chart (the flux and argocd targets).
// Charts, when set, vendors each chart of the umbrella the compile fetched
// (Vendor).
type Umbrella struct {
	Charts ChartArchives
}

// Files: the files of Write of each result, in its target.InstanceDir,
// each chart of the compile vendored (Vendor). The graph is not read.
func (u Umbrella) Files(results []*compiler.Result, _ *compiler.Graph) (map[string][]byte, error) {
	return files(results, func(res *compiler.Result) (map[string][]byte, error) {
		written, err := Write(res)
		if err != nil {
			return nil, err
		}
		if u.Charts != nil {
			if err := Vendor(written, u.Charts); err != nil {
				return nil, err
			}
		}
		return written, nil
	})
}

// Vendor: written, the files of an umbrella (Write), each of its
// dependencies vendored: Helm reads what charts/ holds and never fetches
// it (a chart in charts/ that matches a dependency is the dependency: `helm
// template`, Argo CD, Flux's source-controller): the archive compiled and
// checked, charts/<name>-<version>.tgz, byte for byte, its digest recorded
// in Chart.yaml (DigestsAnnotation). A dependency's Chart.yaml digest in a
// Chart.lock says nothing of the chart's bytes: Helm's digest is of the
// dependency's declaration, and engines fetch a dependency again from its
// repository, by tag. A chart whose archive the compile has none of (not
// fetched: Charts' error), or over VendorMaxSize, is deployed from its
// repository, as written; the rest of the umbrella vendored.
func Vendor(written map[string][]byte, charts ChartArchives) error {
	var c chart.Metadata
	if err := yaml.Unmarshal(written["Chart.yaml"], &c); err != nil {
		return err
	}
	var digests []string
	seen := map[string]bool{}
	for _, d := range c.Dependencies {
		name := d.Name + "-" + d.Version
		if seen[name] {
			continue
		}
		seen[name] = true
		archive, digest, err := charts(chart.Ref{Repository: d.Repository, Chart: d.Name, Version: d.Version})
		if err != nil {
			continue
		}
		digests = append(digests, name+"="+digest)
		if len(archive) > VendorMaxSize {
			continue
		}
		written["charts/"+name+".tgz"] = archive
	}
	if len(digests) == 0 {
		return nil
	}
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations[DigestsAnnotation] = strings.Join(digests, ",")
	b, err := encode([]any{c})
	if err != nil {
		return err
	}
	written["Chart.yaml"] = b
	return nil
}

// files: what write gives of each result, in its target.InstanceDir.
func files(results []*compiler.Result, write func(*compiler.Result) (map[string][]byte, error)) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, res := range results {
		written, err := write(res)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", res.Namespace, res.Release, err)
		}
		for name, data := range written {
			out[path.Join(target.InstanceDir(res.Namespace, res.Release), name)] = data
		}
	}
	return out, nil
}
