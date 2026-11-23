package output

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-compiler-security/policy"

	"github.com/okdp/okdp-gitops/internal/render"
	"github.com/okdp/okdp-gitops/internal/target"
	"github.com/okdp/okdp-gitops/internal/target/helm"
)

// withdraw: the files of the project kept k, files what the output holds
// of it, less the output of each of its refused instances that today's
// platform policy refuses on its own (violation): not kept, the engines
// prune it; and a problem per instance withdrawn, said in k's. A project
// keeps its output so that a mistake in its sources does not take down what
// runs: not so that a value the platform now denies stays deployed.
// charts holds the upstream charts the check renders, nil for none.
func withdraw(k tree.Kept, files map[string][]byte, charts fs.FS) (map[string][]byte, []string) {
	out := files
	var problems []string
	for _, inst := range k.Instances {
		if inst.Depends || inst.Outcome.Err == nil {
			continue
		}
		why := violation(inst.Instance, files, charts)
		if why == nil {
			continue
		}
		dir := target.InstanceDir(inst.Namespace(), inst.Release()) + "/"
		if len(out) == len(files) {
			out = maps.Clone(files)
		}
		for p := range files {
			if strings.HasPrefix(p, dir) {
				delete(out, p)
			}
		}
		kustomization := path.Join(target.ProjectDir(k.Namespace), "kustomization.yaml")
		if b, ok := out[kustomization]; ok {
			out[kustomization] = dropResource(b, "instances/"+inst.Release())
		}
		problems = append(problems, fmt.Sprintf("%s/%s: its output withdrawn: what it deployed no longer passes the platform's policy: %v", inst.Namespace(), inst.Name, why))
	}
	return out, problems
}

// dropResource: the kustomization b without the resource r, as Project
// writes it, one line each.
func dropResource(b []byte, r string) []byte {
	lines := strings.Split(string(b), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "- "+r })
	return []byte(strings.Join(lines, "\n"))
}

// violation: why the platform's policy of today refuses what the output
// holds of ti, a refused instance of a project that is not privileged,
// files the project's: checked as a compile checks them: of the upstream
// values it gives, at their paths in what is deployed, the keys and images
// (policy.UpstreamCheck) and the rule set's finalValues hook, the ingress
// hosts (compiler.CheckFinalValues), and what the charts of the keys it
// gives values to render, with the rule set's objects hook
// (compiler.CheckObjects). nil when it passes,
// or when nothing can tell: no output, no chart at hand (charts nil, or
// a chart it lacks), a chart that does not render (the instance's refusal
// says why it compiled no further).
func violation(ti *tree.Instance, files map[string][]byte, charts fs.FS) error {
	ctx := context.Background() // the output of a tree is written without one
	in := ti.Inputs
	settings, err := compiler.SettingsOf(ctx, in)
	if err != nil || compiler.Privileged(settings, in.Namespace) {
		return nil //nolint:nilerr // no platform settings: the instance's refusal says why
	}
	dir := target.InstanceDir(in.Namespace, ti.Release())
	raw, ok := files[path.Join(dir, helm.ValuesFile)]
	if !ok {
		return nil
	}
	var deployed map[string]map[string]any
	if yaml.Unmarshal(raw, &deployed) != nil || deployed == nil {
		return nil //nolint:nilerr // values that do not parse: nothing can tell
	}
	upstream := mergedUpstream(in)
	check := policy.UpstreamCheck(ctx, in.Rules, settings, in.Namespace, false)
	restricted := map[string]map[string]any{}
	for _, key := range slices.Sorted(maps.Keys(upstream)) {
		if deployed[key] == nil {
			continue
		}
		r, _ := restrict(deployed[key], upstream[key]).(value.M)
		if len(r) == 0 {
			continue
		}
		restricted[key] = r
		if check != nil {
			if err := check(r, deployed[key], func(p string) string { return "upstream." + key + "." + p }); err != nil {
				return err
			}
		}
	}
	if err := compiler.CheckFinalValues(ctx, in.Rules, service(ti), in.Namespace, settings, restricted); err != nil {
		return err
	}
	if charts == nil {
		return nil
	}
	refs := dependencyRefs(files[path.Join(dir, "Chart.yaml")])
	for _, key := range slices.Sorted(maps.Keys(restricted)) {
		ref, ok := refs[key]
		if !ok {
			continue
		}
		objects, err := render.Objects(render.Request{Charts: charts, Ref: ref, Key: key, Values: deployed[key], Release: ti.Release(), Namespace: in.Namespace})
		if err != nil {
			continue
		}
		svc := "upstream." + key + " (rendered)"
		if err := compiler.CheckObjects(ctx, in.Rules, svc, in.Namespace, settings, objects); err != nil {
			return err
		}
	}
	return nil
}

// service: the service of ti, the name of its package's chart, as a
// compile names it to the rule set's hooks; "" without a package.
func service(ti *tree.Instance) string {
	if ti.Definition == nil {
		return ""
	}
	return ti.Definition.Chart.Name
}

// mergedUpstream: the upstream values of the instance of in, by key, its
// values layers merged.
func mergedUpstream(in compiler.Inputs) value.M {
	merged := value.M{}
	for _, layer := range in.Values {
		value.Merge(merged, value.AsMap(value.Copy(layer["upstream"])))
	}
	return merged
}

// restrict: what deployed holds at the paths of given: of maps the keys
// given has, recursively, of anything else deployed whole.
func restrict(deployed, given any) any {
	d, ok := deployed.(value.M)
	g, ok2 := given.(value.M)
	if !ok || !ok2 {
		return deployed
	}
	out := value.M{}
	for k, v := range g {
		if dv, ok := d[k]; ok {
			out[k] = restrict(dv, v)
		}
	}
	return out
}

// dependencyRefs: the chart reference of each dependency of the umbrella
// Chart.yaml b, by its key (its alias, else its name).
func dependencyRefs(b []byte) map[string]chart.Ref {
	var c struct {
		Dependencies []struct {
			Name, Alias, Repository, Version string
		} `yaml:"dependencies"`
	}
	refs := map[string]chart.Ref{}
	if yaml.Unmarshal(b, &c) != nil {
		return refs
	}
	for _, d := range c.Dependencies {
		key := d.Alias
		if key == "" {
			key = d.Name
		}
		refs[key] = chart.Ref{Repository: d.Repository, Chart: d.Name, Version: d.Version}
	}
	return refs
}
