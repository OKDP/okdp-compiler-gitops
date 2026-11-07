// Package gittest serves Git repositories over smart HTTP for the tests,
// with git http-backend: a server that fetches shallow, and serves a
// commit by its hash, as forges do, which go-git's own server does not.
package gittest

import (
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"testing"
)

// Serve serves the repositories under root over HTTP, pushes allowed, and
// returns the base url: <url>/<path of a repository under root>. The test
// is skipped without a git binary.
func Serve(t *testing.T, root string) string {
	t.Helper()
	srv := httptest.NewServer(backend(t, root))
	t.Cleanup(srv.Close)
	return srv.URL
}

// ServeTLS is Serve over https, its certificate signed by a CA of the
// test's own, which it returns, PEM encoded: a client trusts it with the
// CA only.
func ServeTLS(t *testing.T, root string) (url string, ca []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(backend(t, root))
	t.Cleanup(srv.Close)
	return srv.URL, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// backend is git http-backend serving root.
func backend(t *testing.T, root string) http.Handler {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git binary for git http-backend")
	}
	home := t.TempDir()
	return &cgi.Handler{
		Path: bin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + home,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http.receivepack",
			"GIT_CONFIG_VALUE_0=true",
			"GIT_CONFIG_KEY_1=uploadpack.allowReachableSHA1InWant",
			"GIT_CONFIG_VALUE_1=true",
		},
	}
}
