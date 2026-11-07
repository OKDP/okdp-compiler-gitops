// Package gitfetch fetches with go-git the commits a read needs, not their
// history: a shallow fetch, of the depth asked, from a server that
// supports it.
package gitfetch

import (
	"context"
	"errors"
	"strings"

	"github.com/go-git/go-git/v5"
)

// Fetch fetches opts from the remote of repo. A server without shallow
// fetches (go-git's own, which the tests serve file:// with) refuses a
// depth: the fetch is then made again without one, the whole history of
// the refs fetched. Forges (Forgejo, GitHub, GitLab), on git, take it.
func Fetch(ctx context.Context, repo *git.Repository, opts *git.FetchOptions) error {
	err := classify(repo.FetchContext(ctx, opts))
	if opts.Depth == 0 || !errors.Is(err, ErrUnsupported) || ctx.Err() != nil {
		return err
	}
	full := *opts
	full.Depth = 0
	return repo.FetchContext(ctx, &full)
}

// ErrUnsupported is the error of a server that does not fetch shallow.
var ErrUnsupported = errors.New("the server does not fetch shallow")

// unsupportedError: the error go-git gives for a server that does not
// fetch shallow, its text kept, ErrUnsupported to errors.Is.
type unsupportedError struct{ error }

func (e unsupportedError) Unwrap() []error { return []error{ErrUnsupported, e.error} }

// classify: err, an unsupportedError when a server that does not fetch
// shallow made it. The only place that reads the text of a go-git error:
// go-git makes this one by fmt.Errorf("shallow not supported"), with
// neither a type nor a sentinel to match.
func classify(err error) error {
	if err != nil && strings.Contains(err.Error(), "shallow") {
		return unsupportedError{err}
	}
	return err
}
