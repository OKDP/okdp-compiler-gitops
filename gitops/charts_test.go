package gitops

import (
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
	"go.yaml.in/yaml/v3"
)

// ChartHosts: comma separated, trimmed, lowercased; an entry that is not
// a host, host:port nor *.<domain> refused.
func TestChartHosts(t *testing.T) {
	hosts, err := ChartHosts(" Registry.Local:5000, ,*.svc.cluster.local,10.0.0.5 ")
	if err != nil || !slices.Equal(hosts, []string{"registry.local:5000", "*.svc.cluster.local", "10.0.0.5"}) {
		t.Errorf("%q, %v", hosts, err)
	}
	if hosts, err := ChartHosts(""); err != nil || hosts != nil {
		t.Errorf("none: %q, %v", hosts, err)
	}
	for _, raw := range []string{"*", "*.", "a b", "https://registry.local", "registry.local/charts", `a"b`, "-a.b", "a:port"} {
		if _, err := ChartHosts(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

// NewChartPuller: an https chart repository of the cluster, its
// certificate signed by the CA of the credentials, at a loopback
// address: pulled with the CA and the host allowed, refused without
// either.
func TestNewChartPuller(t *testing.T) {
	var s *httptest.Server
	s = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = fmt.Fprintf(w, "entries:\n  app:\n    - version: 1.0.0\n      urls: [%q]\n", s.URL+"/app-1.0.0.tgz")
		default:
			_, _ = w.Write([]byte("archive"))
		}
	}))
	defer s.Close()
	ca, err := NewCredentials(CredentialsConfig{CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})})
	if err != nil {
		t.Fatal(err)
	}
	pull := func(creds *Credentials, private ...string) error {
		p, err := NewChartPuller(creds, nil, private)
		if err != nil {
			return err
		}
		return p.Pull(t.Context(), compiler.ChartRef{Repository: s.URL, Chart: "app", Version: "1.0.0"}, func(r io.Reader) error {
			_, err := io.ReadAll(r)
			return err
		})
	}
	if err := pull(ca, "127.0.0.1"); err != nil {
		t.Errorf("the CA trusted, the host allowed: %v", err)
	}
	if err := pull(nil, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("the CA not trusted: %v", err)
	}
	if err := pull(ca); err == nil || !strings.Contains(err.Error(), "a private, loopback or link-local address") {
		t.Errorf("the host not allowed: %v", err)
	}
	if err := pull(ca, "a b"); err == nil || !strings.Contains(err.Error(), PrivateChartHostsEnv) {
		t.Errorf("a host refused: %v", err)
	}
	if _, err := NewChartPuller(nil, []string{"registry/x"}, nil); err == nil {
		t.Error("an insecure host that is no host accepted")
	}
}

// The job of the output's workflow carries the private chart hosts.
func TestWorkflowCarriesThePrivateChartHosts(t *testing.T) {
	raw, err := OutputWorkflowWith([]byte(ioTopology), WorkflowOptions{PrivateChartHosts: []string{"*.svc.cluster.local", "Registry.Local:5000"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	if env := doc.Jobs["compile"].Steps[0].Env; len(env) != 2 || env[PrivateChartHostsEnv] != "*.svc.cluster.local,registry.local:5000" {
		t.Errorf("env: %v", env)
	}
	if _, err := OutputWorkflowWith([]byte(ioTopology), WorkflowOptions{PrivateChartHosts: []string{"a b"}}); err == nil || !strings.Contains(err.Error(), PrivateChartHostsEnv) {
		t.Errorf("a host the workflow cannot hold: %v", err)
	}
	if plain, err := OutputWorkflow([]byte(ioTopology), nil); err != nil || strings.Contains(string(plain), PrivateChartHostsEnv) {
		t.Errorf("no private host: %v\n%s", err, plain)
	}
}
