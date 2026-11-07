package gitops

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/okdp/okdp-gitops/treeedit"

	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/internal/gittest"
)

// The repositories, the catalogs and the Forgejo API over https, their
// certificates signed by a CA the system does not trust: reached with
// the CA bundle of the credentials, refused without it.
func TestTheCABundleIsTrustedBesideTheSystems(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(filepath.Join(root, "remote.git"), true); err != nil {
		t.Fatal(err)
	}
	base, ca := gittest.ServeTLS(t, root)
	url := base + "/remote.git"
	ctx := context.Background()
	creds, err := NewCredentials(CredentialsConfig{CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}

	store := func(bundle []byte) *GitStore {
		s, err := NewGitStore(GitOptions{URL: url, Branch: "main", CABundle: bundle})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	write := func(s *GitStore) error {
		_, err := s.Update(ctx, Commit{Subject: "seed"}, func(tx Tx) error {
			return tx.WriteFile("catalog/app/1.0.0/Chart.yaml", []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n"))
		})
		return err
	}
	if err := write(store(nil)); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("without the CA: %v", err)
	}
	if err := write(store(creds.CABundle())); err != nil {
		t.Fatalf("with the CA: %v", err)
	}
	if err := store(creds.CABundle()).View(ctx, func(treeedit.Reader) error { return nil }); err != nil {
		t.Fatalf("a read with the CA: %v", err)
	}

	repos := []treeedit.CatalogRepository{{Name: "c", URL: url, Ref: "main"}}
	if _, err := (&catalog.Loader{}).Open(ctx, repos); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a catalog without the CA: %v", err)
	}
	if cats, err := (&catalog.Loader{CABundle: creds.CABundle()}).Open(ctx, repos); err != nil || len(cats.List()) != 1 {
		t.Fatalf("a catalog with the CA: %v", err)
	}

	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forgejo := ForgejoConfig{URL: api.URL, Owner: "okdp", TokenFile: tokenFile}
	apiCA, err := NewCredentials(CredentialsConfig{CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewForgejoClient(forgejo, nil).CreateRepository(ctx, "p", "main", ""); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("Forgejo without the CA: %v", err)
	}
	if err := NewForgejoClient(forgejo, apiCA).CreateRepository(ctx, "p", "main", ""); err != nil {
		t.Fatalf("Forgejo with the CA: %v", err)
	}

	if _, err := NewCredentials(CredentialsConfig{CABundle: []byte("not a certificate")}); err == nil {
		t.Error("a CA bundle of no certificate was accepted")
	}
}
