// Package helmvalues is the compiler's merge of the final values with the
// platform's policy over them, which okdp-gitops applies, and the render of
// the charts a project gives its own values to, the policy's check of what
// they deploy.
package helmvalues

import (
	"context"
	"fmt"
	"maps"
	"slices"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-gitops/internal/policy"
	"github.com/okdp/okdp-gitops/internal/render"
)

// M: a values map.
type M = value.M

// New: a compiler.Finalize of Helm's merge and the platform's policy, the
// render of the charts included: each chart read once for the compiles it
// serves.
func New() compiler.Finalize { return finalize(true) }

// WithoutRender: New without CheckRendered, every other check kept. For the
// compile of a long-running process: Helm's engine takes no context, so a
// render past its timeout leaves its goroutine running, and its memory is
// bounded only once it returns. The compile of the CI, New's, is the boundary.
func WithoutRender() compiler.Finalize { return finalize(false) }

func finalize(rendered bool) compiler.Finalize {
	caches := &compiler.ChartCaches{}
	return func(ctx context.Context, in compiler.FinalInput) (map[string]map[string]any, map[string]chart.Ref, []string, error) {
		check := policy.UpstreamCheck(ctx, in.Rules, in.Settings, in.Namespace, in.Privileged)
		values, charts, warnings, err := compiler.FinalValues(caches, in, check)
		if err != nil {
			return nil, nil, nil, err
		}
		// The hosts of the charts' ingresses, those of the objects being the
		// compile's own; then what the charts a project gives upstream
		// values to deploy: an early message of the values first, the
		// render last.
		if !in.Privileged {
			if err := policy.CheckIngressValues(ctx, in.Rules, in.Namespace, in.Settings, values); err != nil {
				return nil, nil, nil, err
			}
			if rendered {
				if err := CheckRendered(ctx, in, values); err != nil {
					return nil, nil, nil, err
				}
			}
		}
		// A literal of the instance's upstream values at a sensitive key, a
		// credential in clear most likely: a warning, unless allowed.
		if !in.AllowPlainSecrets {
			warnings = append(warnings, policy.PlainUpstream(value.AsMap(in.Values["upstream"]))...)
		}
		return values, charts, warnings, nil
	}
}

// CheckRendered: the policy on what the charts of in, an instance of a
// project that is not privileged, deploy, for each key the instance gives
// its own upstream values to (package-reviewed charts without them are not
// rendered): the chart of the key rendered with its final values, in the
// instance's namespace, under its release (render.Objects), the manifests'
// objects and hosts checked (compiler.CheckObjects,
// compiler.CheckIngressHosts). A key whose chart could not be fetched
// refuses the instance alone (in.ChartFailures): what the check would read
// does not exist, and tenant values cannot be vouched for
// (compiler.Inputs.RequireRender, which Build does not set of its own:
// BuildOptions has no such option). The objects chart is the compiler's.
// No chart cache (in.Charts nil), no render:
// unchecked as the other checks against a chart. The rendered output is
// checked, never written.
func CheckRendered(ctx context.Context, in compiler.FinalInput, values map[string]map[string]any) error {
	upstream := value.AsMap(in.Values["upstream"])
	for _, key := range slices.Sorted(maps.Keys(in.Specs)) {
		spec := in.Specs[key]
		if len(value.AsMap(upstream[key])) == 0 {
			continue
		}
		if why := in.ChartFailures[spec.Source]; why != nil {
			return fmt.Errorf("upstream.%s: the chart %s could not be fetched (%v): its upstream values are refused, nothing can check them", key, spec.Source.ID(), why)
		}
		if in.Charts == nil {
			continue
		}
		objects, err := render.Objects(render.Request{Charts: in.Charts, Ref: spec.Source, Key: key, Values: values[key], Release: in.Release, Namespace: in.Namespace})
		if err != nil {
			return fmt.Errorf("upstream.%s: rendering the chart %s with its final values: %w", key, spec.Source.ID(), err)
		}
		svc := "upstream." + key + " (rendered)"
		if err := compiler.CheckObjects(ctx, in.Rules, svc, in.Namespace, objects); err != nil {
			return err
		}
		if err := compiler.CheckIngressHosts(ctx, in.Rules, svc, in.Namespace, in.Settings, objects); err != nil {
			return err
		}
	}
	return nil
}
