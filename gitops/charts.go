package gitops

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/okdp/okdp-gitops/chartcache"
)

// PrivateChartHostsEnv lists the chart hosts the chart puller reaches at
// a private, loopback or link-local address (a registry of the cluster):
// comma separated, each a host, host:port or *.<domain>, as ChartHosts
// parses them. None by default: a chart host at such an address is
// refused (chartcache.RepositoryPuller.PrivateHosts).
const PrivateChartHostsEnv = "GITOPS_PRIVATE_CHART_HOSTS"

// InsecureChartHostsEnv lists the chart hosts the chart puller reaches
// over plain HTTP (a development registry of the cluster, without TLS):
// comma separated, each a host or host:port, as ChartHosts parses them.
// None by default: a chart host is reached over TLS only, an http:// chart
// repository or registry refused (chartcache.RepositoryPuller.InsecureHosts).
// A host listed may be at a private address too.
const InsecureChartHostsEnv = "GITOPS_INSECURE_CHART_HOSTS"

// chartHostPattern is a host name or an IPv4 address, with an optional
// port, or *.<domain>: what chartcache.RepositoryPuller matches a chart
// host against, which the workflow can hold as it is.
var chartHostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// ChartHosts parses a list of chart hosts, $GITOPS_PRIVATE_CHART_HOSTS's
// or the control plane's: comma separated, as chartHosts takes them.
func ChartHosts(raw string) ([]string, error) {
	return chartHosts(strings.Split(raw, ","))
}

// chartHosts: the entries of list, spaces around each trimmed,
// lowercased, the empty ones dropped; an entry that is not a host,
// host:port nor *.<domain> is refused.
func chartHosts(list []string) ([]string, error) {
	var hosts []string
	for _, h := range list {
		if h = strings.ToLower(strings.TrimSpace(h)); h == "" {
			continue
		}
		if !chartHostPattern.MatchString(h) {
			return nil, fmt.Errorf("chart host %q: a host, host:port or *.<domain>", h)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// NewChartPuller is the chart puller of a program of the credentials
// (okdp-gitops compile --charts, the control plane's chart cache): its
// TLS connections trust creds.RootCAs(), the system's certificates and
// the CABundle; the hosts of insecureHosts (host or host:port) are
// reached over plain HTTP, those of privateHosts at a private, loopback
// or link-local address (chartcache.RepositoryPuller.PrivateHosts), each
// list as ChartHosts takes it, an entry it refuses an error.
func NewChartPuller(creds *Credentials, insecureHosts, privateHosts []string) (*chartcache.RepositoryPuller, error) {
	insecure, err := chartHosts(insecureHosts)
	if err != nil {
		return nil, fmt.Errorf("the insecure chart hosts (%s): %w", InsecureChartHostsEnv, err)
	}
	private, err := chartHosts(privateHosts)
	if err != nil {
		return nil, fmt.Errorf("the private chart hosts (%s): %w", PrivateChartHostsEnv, err)
	}
	roots, err := creds.RootCAs()
	if err != nil {
		return nil, err
	}
	p := chartcache.NewRepositoryPuller(insecure)
	p.PrivateHosts = private
	p.SetRootCAs(roots)
	return p, nil
}
