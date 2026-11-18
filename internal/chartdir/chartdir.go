// Package chartdir is the layout of a chart cache: where each upstream
// chart is unpacked, and the command that unpacks it there by hand. The
// chart cache (chartcache), the render (render) and the checks of a
// compile (chartcheck) read it.
package chartdir

import (
	"fmt"
	"path"
	"strings"

	"github.com/okdp/okdp-compiler/chart"
)

// Of is the directory of a chart in the cache, slash-separated:
//
//	<repository without scheme>/<chart>-<version>/<chart>
//
// http:// has one too, for a cache that pulls from a host the platform
// trusts (chartcache.RepositoryPuller's insecure hosts): the compiler
// refuses it in a package. A repository, a chart or a version that cannot
// be a directory of the cache (an empty segment, . or .., a \, a URL's ?
// or #) is refused.
func Of(ref chart.Ref) (string, error) {
	repo := repoDir(ref.Repository)
	if repo == "" {
		return "", fmt.Errorf("repository %q is no directory of the chart cache", ref.Repository)
	}
	for _, s := range []string{ref.Chart, ref.Version} {
		if s == "" || strings.ContainsAny(s, `/\?#`) || s == "." || s == ".." {
			return "", fmt.Errorf("chart %q %q is no directory of the chart cache", ref.Chart, ref.Version)
		}
	}
	return path.Join(repo, ref.ID(), ref.Chart), nil
}

// repoDir: the directory of the cache that holds the charts of the
// repository, its URL without the scheme (oci://, https://, http://) and
// the trailing / (quay.io/adaltas for oci://quay.io/adaltas); "" when it
// has none.
func repoDir(repository string) string {
	for _, scheme := range []string{"oci://", "https://", "http://"} {
		if rest, ok := strings.CutPrefix(repository, scheme); ok {
			dir := strings.TrimSuffix(rest, "/")
			for segment := range strings.SplitSeq(dir, "/") {
				if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `\?#`) {
					return ""
				}
			}
			return dir
		}
	}
	return ""
}

// Pull is the command that downloads the chart of ref and unpacks it at
// its directory (Of) of a cache filled by hand (deploy.StaticCharts), the
// cache written <chart cache>: helm pull --untar creates the <chart>
// directory itself. The catalogs' scripts/compile-charts.sh runs it when
// the check reports a chart missing.
func Pull(ref chart.Ref) string {
	to := path.Join("<chart cache>", repoDir(ref.Repository), ref.ID())
	if strings.HasPrefix(ref.Repository, "oci://") {
		return fmt.Sprintf("helm pull %s/%s --version %s --untar -d %s", strings.TrimSuffix(ref.Repository, "/"), ref.Chart, ref.Version, to)
	}
	return fmt.Sprintf("helm pull %s --repo %s --version %s --untar -d %s", ref.Chart, ref.Repository, ref.Version, to)
}
