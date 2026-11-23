// Package helmvalues is the compiler's merge of the final values with the
// platform's policy over them, which okdp-gitops applies, their checks
// against the upstream charts (chartcheck), which the compiler does not
// read, and the render of the charts a project gives its own values to,
// the policy's check of what they deploy.
package helmvalues

import (
	"context"
	"fmt"
	"maps"
	"slices"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-compiler-security/policy"

	"github.com/okdp/okdp-gitops/internal/chartcheck"
	"github.com/okdp/okdp-gitops/internal/render"
)

// M: a values map.
type M = value.M

// New: a compiler.Finalize of Helm's merge and the platform's policy, the
// final values checked against the upstream charts of charts (the build's,
// nil for none: unchecked) and the render of the charts included.
func New(charts *chartcheck.Charts) compiler.Finalize { return finalize(charts, true) }

// WithoutRender: New without CheckRendered, every other check kept. For the
// compile of a long-running process: Helm's engine takes no context, so a
// render past its timeout leaves its goroutine running, and its memory is
// bounded only once it returns. The compile of the CI, New's, is the boundary.
func WithoutRender(charts *chartcheck.Charts) compiler.Finalize { return finalize(charts, false) }

func finalize(charts *chartcheck.Charts, rendered bool) compiler.Finalize {
	return func(ctx context.Context, in compiler.FinalInput) (map[string]map[string]any, map[string]chart.Ref, []string, error) {
		privileged := policy.Privileged(in.Settings, in.Namespace)
		check := policy.UpstreamCheck(ctx, in.Rules, in.Settings, in.Namespace, privileged)
		final, err := compiler.FinalValues(in, check)
		if err != nil {
			return nil, nil, nil, err
		}
		values := final.Values
		// The final values against the upstream charts: their schemas, the
		// lists the instance appended to.
		warnings, err := charts.Check(ctx, final)
		if err != nil {
			return nil, nil, nil, err
		}
		// What the charts a project gives upstream values to deploy. The
		// compile then runs the rule set's finalValues hook on the values
		// returned (the hosts of the charts' ingresses).
		if !privileged && rendered {
			if err := CheckRendered(ctx, charts, in, values); err != nil {
				return nil, nil, nil, err
			}
		}
		// A literal of the instance's upstream values at a sensitive key, a
		// credential in clear most likely: a warning, unless allowed.
		if !in.AllowPlainSecrets {
			warnings = append(warnings, policy.PlainUpstream(value.AsMap(in.Values["upstream"]))...)
		}
		return values, final.Charts, warnings, nil
	}
}

// CheckRendered: the policy on what the charts of in, an instance of a
// project that is not privileged, deploy, for each key the instance gives
// its own upstream values to (package-reviewed charts without them are not
// rendered): the chart of the key rendered with its final values, in the
// instance's namespace, under its release (render.Objects), the manifests'
// objects checked once, with the rule set's objects hook
// (compiler.CheckObjects), credentials in clear allowed: the rendered
// output is never written, the values that give them are warned of
// (policy.PlainUpstream). A key whose chart could not be fetched
// refuses the instance alone (charts.Failed): what the check would read
// does not exist, and tenant values cannot be vouched for. The objects
// chart is the compiler's. No charts (nil), no render: unchecked as the
// other checks against a chart. The rendered output is checked, never
// written.
func CheckRendered(ctx context.Context, charts *chartcheck.Charts, in compiler.FinalInput, values map[string]map[string]any) error {
	upstream := value.AsMap(in.Values["upstream"])
	for _, key := range slices.Sorted(maps.Keys(in.Specs)) {
		spec := in.Specs[key]
		if len(value.AsMap(upstream[key])) == 0 {
			continue
		}
		if err := charts.Fetch(ctx, []chart.Ref{spec.Source}); err != nil {
			return err
		}
		if why := charts.Failed(spec.Source); why != nil {
			return fmt.Errorf("upstream.%s: the chart %s could not be fetched (%w): its upstream values are refused, nothing can check them", key, spec.Source.ID(), why)
		}
		if charts == nil {
			continue
		}
		objects, err := render.Objects(render.Request{Charts: charts.FS(), Ref: spec.Source, Key: key, Values: values[key], Release: in.Release, Namespace: in.Namespace})
		if err != nil {
			return fmt.Errorf("upstream.%s: rendering the chart %s with its final values: %w", key, spec.Source.ID(), err)
		}
		svc := "upstream." + key + " (rendered)"
		if err := compiler.CheckObjects(ctx, in.Rules, svc, in.Namespace, in.Settings, true, objects); err != nil {
			return err
		}
	}
	return nil
}
