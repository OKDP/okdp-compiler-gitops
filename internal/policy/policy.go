// Package policy is what the platform's policy refuses in the values of a
// compile, over what its rule set says: the upstream values of an instance
// of a project that is not privileged, the keys of global a project sets,
// and the hosts of the charts' ingresses in the final values. It runs with
// Helm's merge, which makes those values.
package policy

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-gitops/internal/yamldoc"
)

// M: a values map.
type M = value.M

// CheckGlobals: the upstream policy on the global of every layer above level
// 0: the keys of global a project sets, as the upstream values of its
// charts, which every chart receives. Level 0's is the platform's, reviewed
// with it; a privileged project's are not checked. The first refused, in
// layer order, fails, at its path.
func CheckGlobals(ctx context.Context, in compiler.GlobalsInput) error {
	check := UpstreamCheck(ctx, in.Rules, in.Settings, in.Namespace, in.Privileged)
	if check == nil {
		return nil
	}
	for _, l := range in.Layers {
		var path string
		at := func(p string) string { path = p; return l.Name + ": " + p }
		if err := check(M{"global": l.Global}, nil, at); err != nil {
			return value.AtField(path, err)
		}
	}
	return nil
}

// upstreamRules: what the upstream values of an instance of a project
// that is not privileged may not set (upstreamCheck), for the messages its
// namespace.
type upstreamRules struct {
	ctx        context.Context
	rules      *compiler.RuleSet
	namespace  string
	deny       map[string]bool // the platform's keys, global.okdp.policy.upstream.deny, beside the rule set's
	registries []string        // global.okdp.policy.upstream.registries; nil: the images are not checked
}

// UpstreamCheck: the platform's check of the upstream values of an
// instance of namespace (chart.UpstreamCheck), of global.okdp.policy of
// okdp: none for a privileged project, which sets any upstream value; for
// the others, no key of the rule set's deniedUpstreamKeys, no
// runAsUser 0 nor runAsNonRoot false (upstreamRules.key), and, with
// policy.upstream.registries, every image from one of them
// (upstreamRules.image). The package's values are not checked: they are
// reviewed with it.
func UpstreamCheck(ctx context.Context, rules *compiler.RuleSet, okdp M, namespace string, privileged bool) chart.UpstreamCheck {
	if privileged {
		return nil
	}
	r := &upstreamRules{ctx: ctx, rules: rules, namespace: namespace, deny: rules.Added(compiler.TableDeniedUpstreamKeys, okdp)}
	if v := value.Dig(okdp, "policy.upstream.registries"); v != nil {
		r.registries = []string{}
		for _, x := range value.AsList(v) {
			r.registries = append(r.registries, strings.TrimSuffix(value.Str(x), "/"))
		}
	}
	return func(user, values M, at func(string) string) error {
		return r.check(user, values, "", "", at)
	}
}

// yamlDocuments: the separators of the documents of a YAML text.
var yamlDocuments = regexp.MustCompile(`(?m)^---`)

// check: v, the instance's upstream values at the path p (at names it),
// the value of the key k of its parent map ("" at the top and under a
// list), over base, the computed values there (nil under a list, or in a
// string read as YAML: the instance's replace them whole). A map's images
// first (image), then its keys in order, a list's items in order: the
// first refused, in path order, fails. A string that holds a ":" and reads
// as YAML values, a map or a list, each of its documents, is checked as
// values too: charts render some string values as YAML (tpl, Bitnami's
// tplvalues.render), the volumes or containers of a list written as text.
func (r *upstreamRules) check(v, base any, k, p string, at func(string) string) error {
	switch t := v.(type) {
	case M:
		b, _ := base.(M)
		if err := r.image(t, b, k, p, at); err != nil {
			return err
		}
		for _, key := range slices.Sorted(maps.Keys(t)) {
			kp := value.Key(p, key)
			if err := r.key(key, t[key], kp, at); err != nil {
				return err
			}
			if err := r.check(t[key], b[key], key, kp, at); err != nil {
				return err
			}
		}
	case []any:
		for i, x := range t {
			if err := r.check(x, nil, "", value.Index(p, i), at); err != nil {
				return err
			}
		}
	case string:
		if !strings.Contains(t, ":") {
			return nil
		}
		text := func(q string) string { return at(p) + ", read as YAML: " + q }
		for _, doc := range yamlDocuments.Split(t, -1) {
			y, err := yamldoc.Decode([]byte(doc))
			if err != nil {
				continue
			}
			switch y.(type) {
			case M, []any:
				if err := r.check(y, nil, "", "", text); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// key: the error of the key k of the instance's upstream values, at the
// path p, set to v: a key of the rule set's table or of the platform's deny list, to
// any value; runAsUser 0, root; runAsNonRoot false.
func (r *upstreamRules) key(k string, v any, p string, at func(string) string) error {
	return r.rules.Refuse(r.ctx, compiler.CheckUpstreamKey, M{"key": k, "value": v, "at": at(p), "ns": r.namespace, "added": r.deny})
}

// image: the error of the images the instance sets in the map m of its
// upstream values, at the path p, the value of the key k, over base, the
// computed values there; none without registries. An image string is a
// reference; a registry or a repository string (an image map's name,
// Zero to JupyterHub's, where there is no repository), or a null, the
// instance sets makes one with the other, its own or the computed one:
// <registry>/<repository>, the repository alone without registry. A null
// leaves the chart's default, which the compile does not read: "", as an
// image null. Each reference starts with an entry of registries, then
// ends, or goes on with "/" or, after an entry with a path, ":" or "@" (a
// tag, a digest). The keys whose name says they hold an image, a registry
// or a repository of images (namesImage) are references as well.
func (r *upstreamRules) image(m, base M, k, p string, at func(string) string) error {
	if r.registries == nil {
		return nil
	}
	// A key that names an image or a registry by its name, whatever the
	// chart calls it: initImage, imageName, imageRegistry,
	// defaultImageRegistry, sidecarImageRepository; its string, when not
	// empty, is a reference as the image's.
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if ref, ok := m[key].(string); ok && ref != "" && r.namesImage(key) && !r.allowed(ref) {
			return r.refused(ref, value.Key(p, key), at)
		}
	}
	// The instance's string, or its null as "", set; else the computed
	// string. Another value is no image's (Harbor's registry section).
	str := func(key string) (string, bool) {
		if v, ok := m[key]; ok && (v == nil || isString(v)) {
			return value.Str(v), true
		}
		s, _ := base[key].(string)
		return s, false
	}
	if ref, set := str("image"); set && !r.allowed(ref) {
		return r.refused(ref, value.Key(p, "image"), at)
	}
	repo := "repository"
	if _, ok := m[repo]; !ok && base[repo] == nil && k == "image" {
		repo = "name"
	}
	registry, regSet := str("registry")
	repository, repoSet := str(repo)
	if !regSet && !repoSet {
		return nil
	}
	ref := repository
	if registry != "" {
		ref = registry + "/" + repository
	}
	if r.allowed(ref) {
		return nil
	}
	if regSet {
		return r.refused(ref, value.Key(p, "registry"), at)
	}
	return r.refused(ref, value.Key(p, repo), at)
}

// namesImage: whether the key k names an image, a registry or a
// repository of images by its own name, as the charts spell them: it ends
// with one of the rule set's imageKeySuffixes, whatever its case, but image
// itself, which image reads with its sibling keys.
func (r *upstreamRules) namesImage(k string) bool {
	l := strings.ToLower(k)
	return l != "image" && slices.ContainsFunc(r.rules.List(compiler.TableImageKeySuffixes), func(suffix string) bool { return strings.HasSuffix(l, suffix) })
}

// isString: whether v is a string.
func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// allowed: whether the image reference ref starts with an entry of
// registries (image).
func (r *upstreamRules) allowed(ref string) bool {
	return slices.ContainsFunc(r.registries, func(e string) bool {
		rest, ok := strings.CutPrefix(ref, e)
		return e != "" && ok && (rest == "" || rest[0] == '/' || strings.Contains(e, "/") && (rest[0] == ':' || rest[0] == '@'))
	})
}

// refused: the error of the image reference ref, at the path p, from none
// of the registries.
func (r *upstreamRules) refused(ref, p string, at func(string) string) error {
	image, registries := "image "+ref, strings.Join(r.registries, ", ")
	if ref == "" {
		image = "image null, the chart's default,"
	}
	if registries == "" {
		registries = "none"
	}
	return fmt.Errorf("%s: %s is not from a registry of global.okdp.policy.upstream.registries (%s): the platform's projects pull from any (global.okdp.policy.privileged), %s is not one of them", at(p), image, registries, r.namespace)
}

// PlainUpstream: a warning per literal (credentialLiteral) of the
// instance's upstream values, upstream (upstream.<key>...), at a
// sensitive key (value.SensitivePath, but credentialReference): most
// likely a credential, which the instance's values file and the output
// hold in clear. A warning, not a refusal: the key's name is a guess. In
// the order of the paths; the value is never printed.
func PlainUpstream(upstream M) []string {
	var out []string
	var walk func(p, key string, v any)
	walk = func(p, key string, v any) {
		switch v := v.(type) {
		case M:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				walk(value.Key(p, k), k, v[k])
			}
		case []any:
			for i, x := range v {
				walk(value.Index(p, i), key, x)
			}
		default:
			if key != "" && value.Sensitive(key) && !value.CredentialReference(key) && value.CredentialLiteral(v) {
				out = append(out, fmt.Sprintf("%s: a literal at a sensitive key, a credential in clear in the values and the deployments repository: generate it (okdpGeneratedSecret) or read it from the platform's secret store, and give the chart the Secret's name (its existingSecret)", p))
			}
		}
	}
	walk("upstream", "", upstream)
	return out
}

// hostRules: the hosts a project may serve (checkIngressHosts).
type hostRules struct {
	ctx       context.Context
	rules     *compiler.RuleSet
	namespace string
	suffix    string // global.okdp.ingress.suffix, "" when unset
	listed    M      // global.okdp.policy.ingressHosts of namespace, lower case, as a set
}

// newHostRules: the rules of the project of namespace, of okdp, the
// platform's settings.
func newHostRules(ctx context.Context, rules *compiler.RuleSet, okdp M, namespace string) *hostRules {
	r := &hostRules{ctx: ctx, rules: rules, namespace: namespace, suffix: strings.ToLower(strings.Trim(value.Str(value.Dig(okdp, "ingress.suffix")), ".")), listed: M{}}
	for _, h := range value.AsList(value.AsMap(value.Dig(okdp, "policy.ingressHosts"))[namespace]) {
		r.listed[strings.ToLower(value.Str(h))] = true
	}
	return r
}

// check: the error of the host, found at where, when the project may not
// serve it (the rule set's host check). The case and a final dot do not
// count.
func (r *hostRules) check(where, host string) error {
	return r.rules.Refuse(r.ctx, compiler.CheckHost, M{"at": where, "raw": host, "host": strings.ToLower(strings.TrimSuffix(host, ".")), "ns": r.namespace, "suffix": r.suffix, "listed": r.listed})
}

// annotationHosts: the first host of the annotations a (a map of
// annotations) that the rules refuse, by annotation key order; nil when
// they all hold.
func (r *hostRules) annotationHosts(p string, a M) error {
	for _, k := range slices.Sorted(maps.Keys(a)) {
		if !r.rules.Has(compiler.TableHostAnnotations, k) {
			continue
		}
		for _, h := range strings.FieldsFunc(value.Str(a[k]), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\t' }) {
			if err := r.check(value.Key(p, k), h); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckIngressValues: the final values of the charts of an instance of
// svc in namespace, one that is not privileged (Result.Values, by chart),
// name no host the platform keeps from the project (hostRules) under a key
// ingress (ingresses) at any depth: a host, hostname or commonName, the
// strings of hosts, hostnames, dnsNames and extraHosts, the name of an
// extraHosts item, and the annotations that name hosts (the rule set's hostAnnotations).
// The values are the package's computed ones and the instance's upstream
// values over them: the upstream chart's own defaults are not seen. The
// first refused, charts in name order, keys sorted, fails, named
// <chart>: <path>.
func CheckIngressValues(ctx context.Context, rules *compiler.RuleSet, namespace string, okdp M, values map[string]map[string]any) error {
	r := newHostRules(ctx, rules, okdp, namespace)
	for _, chart := range slices.Sorted(maps.Keys(values)) {
		var walk func(v any, p, key string, in bool) error
		check := func(p string, h any) error {
			if s, ok := h.(string); ok && s != "" {
				return r.check(chart+": "+p, s)
			}
			return nil
		}
		walk = func(v any, p, key string, in bool) error {
			switch t := v.(type) {
			case M:
				if in {
					if err := r.annotationHosts(value.Key(p, "annotations"), value.AsMap(t["annotations"])); err != nil {
						return fmt.Errorf("%s: %w", chart, err)
					}
				}
				for _, k := range slices.Sorted(maps.Keys(t)) {
					kp := value.Key(p, k)
					if in {
						switch {
						case rules.Has(compiler.TableIngressHostKeys, k):
							if err := check(kp, t[k]); err != nil {
								return err
							}
						case k == "name":
							if key == "extraHosts" {
								if err := check(kp, t[k]); err != nil {
									return err
								}
							}
						case rules.Has(compiler.TableIngressHostListKeys, k):
							for i, h := range value.AsList(t[k]) {
								if err := check(value.Index(kp, i), h); err != nil {
									return err
								}
							}
							if err := check(kp, t[k]); err != nil {
								return err
							}
						}
					}
					if err := walk(t[k], kp, k, in || rules.Has(compiler.TableIngressValueKeys, k)); err != nil {
						return err
					}
				}
			case []any:
				for i, x := range t {
					if err := walk(x, value.Index(p, i), key, in); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(values[chart], "", "", false); err != nil {
			return err
		}
	}
	return nil
}
