package gitops

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ForgejoClient creates the repository of a new project of a split
// topology, through the Forgejo API, with the token of a file (read at each
// call, so a rotated Secret is taken at once). The token goes over
// http:// only to the hosts the Credentials allow
// (Credentials.CheckInsecureHTTP), as the credentials of the repositories.
type ForgejoClient struct {
	Config ForgejoConfig
	// Credentials: their InsecureHTTPHosts are the hosts the token may go
	// to over http://; nil allows none.
	Credentials *Credentials
	HTTP        *http.Client
}

// NewForgejoClient returns the client of c, its token sent over http://
// to the hosts creds allows only, its TLS connections trusting the
// system's certificates and creds' CABundle.
func NewForgejoClient(c ForgejoConfig, creds *Credentials) *ForgejoClient {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameOrigin}
	if len(creds.CABundle()) > 0 {
		// RootCAs cannot fail: NewCredentials checked the bundle. Cloned,
		// not built from scratch: a bare Transport has no Proxy.
		roots, _ := creds.RootCAs()
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		client.Transport = transport
	}
	return &ForgejoClient{Config: c, Credentials: creds, HTTP: client}
}

// sameOrigin is the redirect policy of the Forgejo client: Go follows up to
// 10 redirects and keeps the Authorization header across those that stay on
// the host, an https:// request redirected to http:// included, where the
// token would go in clear. A redirect that changes the scheme or the host
// (or the port) is refused: the API is where the topology says it is.
func sameOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Scheme, first.Scheme) || !strings.EqualFold(req.URL.Host, first.Host) {
		return fmt.Errorf("refusing to follow a redirect of %s to %s: the Forgejo API does not move to another scheme or host", first.Redacted(), req.URL.Redacted())
	}
	return nil
}

// ErrRepositoryNotEmpty is returned by ForgejoClient.CreateRepository for
// a repository that exists already and holds commits: it is not taken as
// the new project's, who knows whose content it is (a project deleted
// since keeps its repository, with the content).
var ErrRepositoryNotEmpty = errors.New("the repository exists already and is not empty")

// CreateRepository creates <owner>/<name>, empty, private, its default
// branch branch, described by description. A repository that exists
// already is taken as it is when it is empty (a creation that failed
// halfway), else refused (ErrRepositoryNotEmpty). A server over http://
// whose host the Credentials do not allow is refused before the token is
// read.
func (f *ForgejoClient) CreateRepository(ctx context.Context, name, branch, description string) error {
	if err := f.Credentials.CheckInsecureHTTP(f.Config.URL); err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	raw, err := os.ReadFile(f.Config.TokenFile)
	if err != nil {
		return fmt.Errorf("reading the Forgejo token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	body, err := json.Marshal(map[string]any{
		"name":           name,
		"default_branch": branch,
		"private":        true,
		"auto_init":      false,
		"description":    description,
	})
	if err != nil {
		return err
	}
	api := strings.TrimSuffix(f.Config.URL, "/") + "/api/v1"
	endpoint := api + "/orgs/" + url.PathEscape(f.Config.Owner) + "/repos"
	status, msg, err := f.do(ctx, http.MethodPost, endpoint, token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	switch status {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict:
		return f.takeEmpty(ctx, api, token, name)
	default:
		return fmt.Errorf("POST %s: %d %s: %s", endpoint, status, http.StatusText(status), strings.TrimSpace(string(msg)))
	}
}

// takeEmpty accepts the repository <owner>/<name> that exists when it
// holds no commit, ErrRepositoryNotEmpty otherwise.
func (f *ForgejoClient) takeEmpty(ctx context.Context, api, token, name string) error {
	endpoint := api + "/repos/" + url.PathEscape(f.Config.Owner) + "/" + url.PathEscape(name)
	status, msg, err := f.do(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s: %s", endpoint, status, http.StatusText(status), strings.TrimSpace(string(msg)))
	}
	var repo struct {
		Empty bool `json:"empty"`
	}
	if err := json.Unmarshal(msg, &repo); err != nil {
		return fmt.Errorf("GET %s: %w", endpoint, err)
	}
	if !repo.Empty {
		return fmt.Errorf("%s/%s: %w: add the project to %s by hand to adopt it, or delete the repository", f.Config.Owner, name, ErrRepositoryNotEmpty, "projects.yaml")
	}
	return nil
}

// do sends a request of the API with the token, and returns the status and
// the start of the response.
func (f *ForgejoClient) do(ctx context.Context, method, endpoint, token string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	msg, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, msg, err
}
