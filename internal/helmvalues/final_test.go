package helmvalues

import (
	"context"
	"errors"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/rules"
)

// TestCheckFinalValues: the finalValues hook runs through the compile's
// FinalInput.CheckFinalValues when it gives one, and here without it.
func TestCheckFinalValues(t *testing.T) {
	values := map[string]map[string]any{"app": {"ingress": M{"host": "web.example.org"}}}
	in := compiler.FinalInput{Rules: rules.Standard(), Namespace: "demo", Settings: M{"ingress": M{"suffix": "okdp.sandbox"}}}
	if err := checkFinalValues(context.Background(), in, values); err == nil || !strings.HasPrefix(err.Error(), `app: ingress.host: host "web.example.org" is not served by demo`) {
		t.Errorf("without the compile's: %v", err)
	}
	given := errors.New("the compile's")
	in.CheckFinalValues = func(context.Context, map[string]map[string]any) error { return given }
	if err := checkFinalValues(context.Background(), in, values); !errors.Is(err, given) {
		t.Errorf("with the compile's: %v", err)
	}
}
