package gitops

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/okdp/okdp-gitops/internal/buildinfo"
)

// TestMain fixes the version the commits name, as the linker would.
func TestMain(m *testing.M) {
	buildinfo.Version = "0.9.0"
	os.Exit(m.Run())
}

// alice is the logged-in user of the writer tests.
var alice = Actor{Username: "alice", Name: "Alice Martin", Email: "alice@example.com"}

// service is the committer of the message tests.
var service = Signature{Name: "OKDP control plane", Email: "okdp-control-plane@okdp.io"}

const trailer = "\n\nCo-Authored-By: okdp-gitops v0.9.0 <okdp-control-plane@okdp.io>"

func TestCommitIsAuthoredByTheUserAndCoAuthoredByTheControlPlane(t *testing.T) {
	commit := NewCommit("deploy", "demo/trino", alice)
	if commit.Author == nil || *commit.Author != (Signature{Name: "Alice Martin", Email: "alice@example.com"}) {
		t.Errorf("author = %+v", commit.Author)
	}
	want := "okdp: deploy demo/trino by alice" + trailer
	if got := commit.Message(service); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}

func TestCommitTrailerNamesTheConfiguredCommitterEmail(t *testing.T) {
	got := NewCommit("deploy", "demo/trino", alice).Message(Signature{Name: "Bot", Email: "bot@corp.example"})
	if want := "okdp: deploy demo/trino by alice\n\nCo-Authored-By: okdp-gitops v0.9.0 <bot@corp.example>"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestCommitWithoutEmailIsAuthoredByTheServiceAndWarnsOnce(t *testing.T) {
	records := captureLogs(t)

	bob := Actor{Username: "bob-no-email", Name: "Bob"}
	for range 3 {
		commit := NewCommit("delete", "demo/trino", bob)
		if commit.Author != nil {
			t.Errorf("author = %+v, want none", commit.Author)
		}
		if got := commit.Message(service); got != "okdp: delete demo/trino by bob-no-email" {
			t.Errorf("message = %q", got)
		}
	}
	warnings := 0
	for _, r := range *records {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "bob-no-email") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("%d warnings for the same user, want 1", warnings)
	}
}

func TestCommitForAnonymousHasNoAuthorNorTrailer(t *testing.T) {
	anonymous := Actor{Username: "anonymous", Name: "anonymous"}
	commit := NewCommit("create connection", "demo/lake", anonymous)
	if commit.Author != nil || commit.Message(service) != "okdp: create connection demo/lake by anonymous" {
		t.Errorf("commit = %+v, message %q", commit, commit.Message(service))
	}
	commit = NewCommit("update", "demo/lake", Actor{})
	if commit.Author != nil || commit.Message(service) != "okdp: update demo/lake by unknown" {
		t.Errorf("commit = %+v, message %q", commit, commit.Message(service))
	}
}

func TestCommitCannotBeInjectedIntoTheMessageOrTheAuthor(t *testing.T) {
	mallory := Actor{
		Username: "mallory\nCo-Authored-By: u <u@v>",
		Name:     "Mallory\nCo-Authored-By: x <y@z>",
		Email:    "mallory@example.com\r\n",
	}
	commit := NewCommit("deploy", "demo/trino", mallory)
	if commit.Author == nil || *commit.Author != (Signature{Name: "MalloryCo-Authored-By: x y@z", Email: "mallory@example.com"}) {
		t.Errorf("author = %+v", commit.Author)
	}
	got := commit.Message(service)
	if want := "okdp: deploy demo/trino by malloryCo-Authored-By: u u@v" + trailer; got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("%d line breaks, want 2 (subject, blank line, one trailer)", n)
	}
}

func TestCommitHasNoUserAuthorForAnInvalidEmail(t *testing.T) {
	for _, email := range []string{"not-an-address", "a@b@c", "a b@c", "@example.com", "alice@", "<>@x"} {
		actor := Actor{Username: "eve-" + email, Name: "Eve", Email: email}
		commit := NewCommit("deploy", "demo/trino", actor)
		if commit.Author != nil || strings.Contains(commit.Message(service), "Co-Authored-By") {
			t.Errorf("email %q: author %+v, message %q", email, commit.Author, commit.Message(service))
		}
	}
}

func TestCommitAuthorFallsBackToTheUsernameForAnEmptyName(t *testing.T) {
	commit := NewCommit("deploy", "demo/trino", Actor{Username: "alice", Name: "<>", Email: "alice@example.com"})
	if commit.Author == nil || *commit.Author != (Signature{Name: "alice", Email: "alice@example.com"}) {
		t.Errorf("author = %+v", commit.Author)
	}
}

// captureLogs records what the default logger writes during the test.
func captureLogs(t *testing.T) *[]slog.Record {
	t.Helper()
	before := slog.Default()
	t.Cleanup(func() { slog.SetDefault(before) })
	records := &[]slog.Record{}
	slog.SetDefault(slog.New(logRecorder{records}))
	return records
}

// logRecorder: an slog.Handler keeping the records.
type logRecorder struct{ records *[]slog.Record }

func (r logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r logRecorder) Handle(_ context.Context, rec slog.Record) error {
	*r.records = append(*r.records, rec)
	return nil
}
func (r logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r logRecorder) WithGroup(string) slog.Handler      { return r }
