package gitops

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// Credentials file names, the keys of the credentials Secret mounted in the
// pod. They follow the Flux GitRepository Secret convention, so the Secret the
// engine reads the repository with can be reused as is (with write access).
const (
	credUsername    = "username"
	credPassword    = "password"
	credBearerToken = "bearerToken"
	credIdentity    = "identity"
	credKnownHosts  = "known_hosts"
	credPassphrase  = "passphrase"
)

// The environment of okdp-gitops compile: where the credentials of the
// repositories are (CredentialsFromEnv).
const (
	// CredentialsDirEnv names a directory of credentials
	// (CredentialsConfig.Dir).
	CredentialsDirEnv = "GITOPS_CREDENTIALS_DIR"
	// TokenEnv is a bearer token for every HTTPS repository: the job of
	// the output repository's workflow gets it from that repository's
	// secret TokenSecret.
	TokenEnv = "GITOPS_TOKEN"
	// InsecureHTTPHostsEnv lists the hosts a credential is sent to over
	// http://, in clear: comma separated, each a host name or a glob of
	// one (path.Match: *.svc.cluster.local, a cluster's services). None
	// by default: a credential for an http:// URL is refused.
	InsecureHTTPHostsEnv = "GITOPS_INSECURE_HTTP_HOSTS"
)

// CredentialsConfig is what Credentials are built of.
type CredentialsConfig struct {
	// Dir is a directory of credentials, a file per key, as Flux's
	// GitRepository Secret holds them; "" for none. Its files are read
	// at each use, so that a rotated Secret is taken at once:
	//
	//   - HTTPS: username + password (a token as password), or
	//     bearerToken.
	//   - SSH: identity (a private key), known_hosts (required unless
	//     InsecureIgnoreHostKey), optional passphrase.
	//
	// A missing or empty directory means anonymous access.
	Dir string
	// Token is a bearer token for every HTTP(S) repository, when Dir is
	// "" (an SSH repository needs the directory).
	Token string
	// InsecureIgnoreHostKey accepts any SSH host key, without
	// known_hosts. Sandboxes only.
	InsecureIgnoreHostKey bool
	// InsecureHTTPHosts are the hosts a credential goes to over http://,
	// in clear: host names or globs of them (path.Match,
	// *.svc.cluster.local), as InsecureHTTPHosts parses them. None: a
	// credential for an http:// URL is refused.
	InsecureHTTPHosts []string
	// CABundle is PEM certificates every TLS connection trusts beside the
	// system's: the repositories' (GitOptions.CABundle), the catalogs'
	// (catalog.Loader.CABundle), the Forgejo API's (NewForgejoClient). A
	// platform's CA, so that no certificate check is turned off.
	CABundle []byte
}

// Credentials are the credentials okdp-gitops and the control plane send:
// to the repositories of a topology (For), to the catalogs of the host of
// the platform's repository (Catalogs), to the Forgejo API
// (ForgejoClient). Built once, they refuse alike, on every path, a
// credential that would travel in clear: one for an http:// URL whose host
// is not one of the InsecureHTTPHosts (CheckInsecureHTTP). A nil
// *Credentials is anonymous access, and allows no http:// host.
type Credentials struct {
	cfg CredentialsConfig
}

// NewCredentials returns the credentials of c; an InsecureHTTPHosts entry
// that is not a host name nor a glob of one is refused, a CABundle of no
// certificate too.
func NewCredentials(c CredentialsConfig) (*Credentials, error) {
	hosts := make([]string, 0, len(c.InsecureHTTPHosts))
	for _, h := range c.InsecureHTTPHosts {
		hosts = append(hosts, strings.ToLower(strings.TrimSpace(h)))
	}
	if err := checkInsecureHosts(hosts); err != nil {
		return nil, err
	}
	c.InsecureHTTPHosts = hosts
	c.Token = strings.TrimSpace(c.Token)
	creds := &Credentials{cfg: c}
	if _, err := creds.RootCAs(); err != nil {
		return nil, err
	}
	return creds, nil
}

// CredentialsFromEnv returns the credentials of the environment, those of
// okdp-gitops compile: the directory $GITOPS_CREDENTIALS_DIR names when it
// is set, else the bearer token $GITOPS_TOKEN, else none (anonymous); sent
// over http:// to the hosts of $GITOPS_INSECURE_HTTP_HOSTS only.
func CredentialsFromEnv() (*Credentials, error) {
	return NewCredentials(CredentialsConfig{
		Dir:               os.Getenv(CredentialsDirEnv),
		Token:             os.Getenv(TokenEnv),
		InsecureHTTPHosts: InsecureHTTPHosts(os.Getenv(InsecureHTTPHostsEnv)),
	})
}

// CABundle is the CredentialsConfig's CABundle, nil for none.
func (c *Credentials) CABundle() []byte {
	if c == nil {
		return nil
	}
	return c.cfg.CABundle
}

// RootCAs are the system's certificates and those of CABundle: what a
// TLS client of the credentials trusts. The system's alone without a
// bundle; an error for a bundle that holds no certificate.
func (c *Credentials) RootCAs() (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if bundle := c.CABundle(); len(bundle) > 0 && !pool.AppendCertsFromPEM(bundle) {
		return nil, errors.New("the CA bundle holds no PEM certificate")
	}
	return pool, nil
}

// InsecureHTTPHosts parses a list of $GITOPS_INSECURE_HTTP_HOSTS: comma
// separated, spaces around each trimmed, lowercased, the empty ones
// dropped.
func InsecureHTTPHosts(raw string) []string {
	var hosts []string
	for h := range strings.SplitSeq(raw, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// insecureHostPattern is a host name or a glob of one, which the workflow
// can hold as it is.
var insecureHostPattern = regexp.MustCompile(`^[a-z0-9.*?\[\]-]+$`)

// checkInsecureHosts refuses an entry of InsecureHTTPHosts that is not a
// host name nor a glob of one.
func checkInsecureHosts(hosts []string) error {
	for _, h := range hosts {
		if _, err := path.Match(h, ""); err != nil || !insecureHostPattern.MatchString(h) {
			return fmt.Errorf("%s: %q is not a host name nor a glob of one", InsecureHTTPHostsEnv, h)
		}
	}
	return nil
}

// For returns the transport credentials of the repository url, nil for
// anonymous access. A credential for an http:// URL, the configuration's
// or a password in the URL, is refused unless its host is one of the
// InsecureHTTPHosts (CheckInsecureHTTP): it would travel in clear.
func (c *Credentials) For(url string) (transport.AuthMethod, error) {
	var auth transport.AuthMethod
	if c != nil {
		var err error
		if auth, err = c.auth(url); err != nil {
			return nil, err
		}
	}
	if auth == nil && !hasPassword(url) {
		return nil, nil
	}
	if err := c.CheckInsecureHTTP(url); err != nil {
		return nil, err
	}
	return auth, nil
}

// CheckInsecureHTTP refuses a credential for rawURL when it is an http://
// URL whose host matches none of the InsecureHTTPHosts: the credential
// would travel in clear. Any other URL passes (an scp-like SSH one is no
// URL). For the credential of a client of its own, the Forgejo API
// token: For checks the repositories'.
func (c *Credentials) CheckInsecureHTTP(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return nil //nolint:nilerr // not an http:// URL: no credential in clear
	}
	host := strings.ToLower(u.Hostname())
	if c != nil && insecureHostAllowed(host, c.cfg.InsecureHTTPHosts) {
		return nil
	}
	u.User = nil
	return fmt.Errorf("refusing to send the credentials of %s over http://, in clear: its host %s is not in $%s (comma-separated hosts or globs, e.g. *.svc.cluster.local); use https://", u, host, InsecureHTTPHostsEnv)
}

// Catalogs gives a catalog repository the credentials of the deployments
// repositories (For) when it is on the host of platformURL, and none
// elsewhere: a credential is never sent to another forge.
func (c *Credentials) Catalogs(platformURL string) func(string) (transport.AuthMethod, error) {
	platformHost := urlHost(platformURL)
	return func(repo string) (transport.AuthMethod, error) {
		if platformHost == "" || !strings.EqualFold(urlHost(repo), platformHost) {
			return nil, nil
		}
		return c.For(repo)
	}
}

// insecureHostAllowed tells whether host matches one of the hosts or
// globs of hosts.
func insecureHostAllowed(host string, hosts []string) bool {
	for _, pattern := range hosts {
		if ok, err := path.Match(pattern, host); err == nil && ok {
			return true
		}
	}
	return false
}

// hasPassword tells a URL that carries a password, which go-git sends
// when it has no other credentials.
func hasPassword(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	_, ok := u.User.Password()
	return ok
}

// auth is For but the check of an http:// URL: the directory's
// credentials when there is one, else the token.
func (c *Credentials) auth(url string) (transport.AuthMethod, error) {
	if c.cfg.Dir != "" {
		return authFromDir(url, c.cfg.Dir, c.cfg.InsecureIgnoreHostKey)
	}
	if c.cfg.Token == "" {
		return nil, nil
	}
	if isSSHURL(url) {
		return nil, fmt.Errorf("an SSH repository URL needs the credentials of $%s (identity, known_hosts): $%s is a token for HTTPS", CredentialsDirEnv, TokenEnv)
	}
	return &githttp.TokenAuth{Token: c.cfg.Token}, nil
}

// authFromDir builds the transport credentials for url from a directory
// holding the credentials Secret (CredentialsConfig.Dir).
func authFromDir(url, dir string, insecureIgnoreHostKey bool) (transport.AuthMethod, error) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return string(data)
	}

	if isSSHURL(url) {
		identity := read(credIdentity)
		if identity == "" {
			return nil, fmt.Errorf("an SSH repository URL needs an %q key in the credentials Secret", credIdentity)
		}
		user := "git"
		if endpoint, err := transport.NewEndpoint(url); err == nil && endpoint.User != "" {
			user = endpoint.User
		}
		keys, err := gitssh.NewPublicKeys(user, []byte(identity), strings.TrimSpace(read(credPassphrase)))
		if err != nil {
			return nil, fmt.Errorf("invalid SSH identity: %w", err)
		}
		switch {
		case read(credKnownHosts) != "":
			callback, err := gitssh.NewKnownHostsCallback(filepath.Join(dir, credKnownHosts))
			if err != nil {
				return nil, fmt.Errorf("invalid known_hosts: %w", err)
			}
			keys.HostKeyCallback = callback
		case insecureIgnoreHostKey:
			keys.HostKeyCallback = ssh.InsecureIgnoreHostKey() // #nosec G106 -- explicit opt-in, sandboxes only
		default:
			return nil, errors.New("an SSH repository URL needs a known_hosts key in the credentials Secret")
		}
		return keys, nil
	}

	if token := strings.TrimSpace(read(credBearerToken)); token != "" {
		return &githttp.TokenAuth{Token: token}, nil
	}
	username := strings.TrimSpace(read(credUsername))
	password := strings.TrimSpace(read(credPassword))
	if username == "" && password == "" {
		return nil, nil
	}
	if username == "" {
		// Most forges accept any user name with a token as password.
		username = "git"
	}
	return &githttp.BasicAuth{Username: username, Password: password}, nil
}

func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	if strings.Contains(url, "://") {
		return false
	}
	// scp-like syntax: git@host:org/repo.git
	at := strings.Index(url, "@")
	colon := strings.Index(url, ":")
	return at >= 0 && colon > at
}

// urlHost is the host of a repository url, https://host/... or
// user@host:path; "" for a local directory.
func urlHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Hostname()
	}
	if IsLocal(raw) {
		return ""
	}
	if at := strings.Index(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	h, _, _ := strings.Cut(raw, ":")
	return h
}
