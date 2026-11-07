package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func newCredentials(t *testing.T, c CredentialsConfig) *Credentials {
	t.Helper()
	creds, err := NewCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func credentialsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A catalog on the host of the platform's repository is read with its
// credentials; one elsewhere, or with a local platform, anonymously.
func TestCatalogsKeepTheCredentialsOnTheirHost(t *testing.T) {
	dir := credentialsDir(t, map[string]string{"username": "okdp-admin", "password": "s3cr3t"})
	creds := newCredentials(t, CredentialsConfig{Dir: dir, InsecureHTTPHosts: []string{"forgejo.local"}})
	auth := creds.Catalogs("http://forgejo.local:3000/okdp/okdp-platform.git")
	got, err := auth("http://FORGEJO.local:3000/okdp/platform-charts.git")
	if basic, ok := got.(*githttp.BasicAuth); err != nil || !ok || basic.Username != "okdp-admin" || basic.Password != "s3cr3t" {
		t.Errorf("same host: %#v %v", got, err)
	}
	if got, err := auth("https://github.com/OKDP/platform-charts.git"); got != nil || err != nil {
		t.Errorf("another host: %#v %v", got, err)
	}
	if got, err := creds.Catalogs("/srv/layout")("/srv/catalog"); got != nil || err != nil {
		t.Errorf("a local platform: %#v %v", got, err)
	}
	// Over http:// to a host the credentials do not allow: refused, not
	// read anonymously.
	creds = newCredentials(t, CredentialsConfig{Dir: dir})
	if _, err := creds.Catalogs("http://forgejo.local:3000/okdp/okdp-platform.git")("http://forgejo.local:3000/okdp/platform-charts.git"); err == nil || !strings.Contains(err.Error(), "refusing to send the credentials") {
		t.Errorf("a catalog over http: %v", err)
	}
}

// The credentials of okdp-gitops compile: the directory of
// $GITOPS_CREDENTIALS_DIR when it is set, else the bearer token
// $GITOPS_TOKEN, else none; the token goes to the platform's host only.
func TestCredentialsFromEnvTakeTheDirectoryThenTheToken(t *testing.T) {
	repo := "https://git.example.org/okdp/okdp-output.git"
	t.Setenv(CredentialsDirEnv, "")
	t.Setenv(TokenEnv, "")
	t.Setenv(InsecureHTTPHostsEnv, "")
	if got, err := envCredentials(t).For(repo); got != nil || err != nil {
		t.Errorf("none: %#v %v", got, err)
	}

	t.Setenv(TokenEnv, " t0ken\n")
	creds := envCredentials(t)
	token, ok := mustAuth(t, creds, repo).(*githttp.TokenAuth)
	if !ok || token.Token != "t0ken" {
		t.Errorf("the token: %#v", token)
	}
	if _, err := creds.For("git@git.example.org:okdp/okdp-output.git"); err == nil || !strings.Contains(err.Error(), "$GITOPS_TOKEN is a token for HTTPS") {
		t.Errorf("SSH with a token: %v", err)
	}
	auth := creds.Catalogs("https://git.example.org/okdp/okdp-platform.git")
	if got, err := auth("https://git.example.org/okdp/platform-charts.git"); err != nil || got.(*githttp.TokenAuth).Token != "t0ken" {
		t.Errorf("a catalog of the platform's host: %#v %v", got, err)
	}
	if got, err := auth("https://github.com/OKDP/platform-charts.git"); got != nil || err != nil {
		t.Errorf("a catalog of another host: %#v %v", got, err)
	}

	t.Setenv(CredentialsDirEnv, credentialsDir(t, map[string]string{"password": "s3cr3t"}))
	if basic, ok := mustAuth(t, envCredentials(t), repo).(*githttp.BasicAuth); !ok || basic.Username != "git" || basic.Password != "s3cr3t" {
		t.Errorf("the directory first: %#v", basic)
	}

	t.Setenv(InsecureHTTPHostsEnv, "git.example.org, a b")
	if _, err := CredentialsFromEnv(); err == nil || !strings.Contains(err.Error(), `GITOPS_INSECURE_HTTP_HOSTS: "a b" is not a host name nor a glob of one`) {
		t.Errorf("an invalid host: %v", err)
	}
}

func envCredentials(t *testing.T) *Credentials {
	t.Helper()
	creds, err := CredentialsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func mustAuth(t *testing.T, creds *Credentials, url string) transport.AuthMethod {
	t.Helper()
	auth, err := creds.For(url)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

// A credential goes over http://, in clear, to the hosts of
// $GITOPS_INSECURE_HTTP_HOSTS only: refused for any other, the token as
// the directory's, a password of the URL too; nothing to refuse without
// one, nor over https://.
func TestCredentialsRefuseACredentialOverHTTP(t *testing.T) {
	forgejo := "http://forgejo-http.forgejo.svc.cluster.local:3000/okdp/okdp-output.git"
	if got, err := newCredentials(t, CredentialsConfig{}).For(forgejo); got != nil || err != nil {
		t.Errorf("anonymous over http: %#v %v", got, err)
	}
	var none *Credentials
	if got, err := none.For(forgejo); got != nil || err != nil {
		t.Errorf("no credentials over http: %#v %v", got, err)
	}
	if _, err := none.For("http://bot:pw@git.example.org/okdp/o.git"); err == nil {
		t.Error("a password of the URL over http: accepted")
	}

	creds := newCredentials(t, CredentialsConfig{Token: "t0ken"})
	_, err := creds.For("http://bot@git.example.org/okdp/okdp-output.git")
	if err == nil || !strings.Contains(err.Error(), "refusing to send the credentials of http://git.example.org/okdp/okdp-output.git over http://, in clear: its host git.example.org is not in $GITOPS_INSECURE_HTTP_HOSTS") {
		t.Errorf("the token over http: %v", err)
	}
	if _, err := creds.For(forgejo); err == nil {
		t.Error("the token over http, no host allowed: accepted")
	}
	mustAuth(t, creds, "https://git.example.org/okdp/okdp-output.git")

	creds = newCredentials(t, CredentialsConfig{Token: "t0ken", InsecureHTTPHosts: InsecureHTTPHosts(" git.example.org, *.SVC.cluster.local ")})
	if token, ok := mustAuth(t, creds, forgejo).(*githttp.TokenAuth); !ok || token.Token != "t0ken" {
		t.Errorf("a glob: %#v", token)
	}
	mustAuth(t, creds, "http://git.example.org/okdp/okdp-output.git")
	if _, err := creds.For("http://git.example.org.evil.example/okdp/o.git"); err == nil {
		t.Error("a host the list does not name: accepted")
	}
	if _, err := creds.For("http://svc.cluster.local/okdp/o.git"); err == nil {
		t.Error("the domain of a glob of its subdomains: accepted")
	}

	// The directory's credentials, the control plane's: checked alike.
	creds = newCredentials(t, CredentialsConfig{Dir: credentialsDir(t, map[string]string{"password": "s3cr3t"})})
	if _, err := creds.For(forgejo); err == nil {
		t.Error("the directory's credentials over http: accepted")
	}
	if err := creds.CheckInsecureHTTP("http://forgejo.local:3000"); err == nil {
		t.Error("the Forgejo API over http: accepted")
	}
	if err := none.CheckInsecureHTTP("https://forgejo.local"); err != nil {
		t.Errorf("the Forgejo API over https: %v", err)
	}
}
