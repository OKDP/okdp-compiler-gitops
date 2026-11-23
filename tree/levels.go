// Levels: what each values file of the tree may set. The compiler takes
// an instance's values as flat layers (Inputs.Values), any of them
// setting anything; the tree knows where each comes from, and enforces
// what each origin may set before it flattens them.
//
// The files of the tree's values are levels: PlatformFile is level 0,
// the platform's, ProjectFile level 1, a project's, an instance's
// ConnectionsFile level 2. Each level adds to the levels below and
// changes nothing they set: no value, no connection, no claim, no class,
// no default class. Of global.okdp, the platform's settings, a level
// above 0 sets provisioning only (its classes); of the rest of global,
// the keys the platform lists (global.okdp.policy.projectGlobals). Only
// level 0 declares no claims, only level 1 gives access, and only level
// 0 gives template code: the platform's file is reviewed with the
// platform, the higher levels are a project's input.
//
// A project's access reaches its instances as the settings the security
// set reads, global.okdp.access.groups, in the host's settings the tree
// gives beside the files' layers (GroupsSettings, Inputs.Settings): no
// chart receives them, no file sets that key, the platform's neither, so
// that the groups of a namespace are its project's alone. The compiler
// refuses a layer that gives them too; the tree refuses the platform's
// file first, naming the file.
//
// An instance's ConfigFile sets only the keys of global its package's
// parameter schema declares, never okdp, and no connections (its own are
// its ConnectionsFile), no claims (they are its project's) and no access.

package tree

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler-security/access"
	"github.com/okdp/okdp-compiler-security/policy"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"
)

// M is a map of values.
type M = map[string]any

// AccessKey is the key of ProjectFile that gives the roles of the
// project's groups (GroupsSettings). No instance receives it as a value.
const AccessKey = "access"

// groupsSetting is the path of the groups of a namespace in the values,
// the setting of the security set (access.Setting under global.okdp).
const groupsSetting = "global.okdp." + access.Setting

// DefaultProjectGroup returns okdp-project-<namespace>, the group that is
// an editor of a project whose ProjectFile gives no access.
func DefaultProjectGroup(namespace string) string { return "okdp-project-" + namespace }

// defaultProjectGlobals are the keys of global a level above 0 sets when
// the platform lists none (global.okdp.policy.projectGlobals).
var defaultProjectGlobals = []string{"project", "storageClass"}

// projectSettings are the keys of global.okdp a level above 0 sets: the
// classes a project adds.
var projectSettings = []string{"provisioning"}

// wholePaths are the paths a level gives whole, or not at all: * stands
// for one key. A level may not change them piecemeal.
var wholePaths = [][]string{
	{"connections", "*"},
	{"claims", "*"},
	{"global", "okdp", "provisioning", "*", "classes", "*"},
	{"global", "okdp", "provisioning", "*", "default"},
}

// wholePath reports whether the map at keys counts as one value (wholePaths).
func wholePath(keys []string) bool {
	return slices.ContainsFunc(wholePaths, func(p []string) bool {
		if len(p) != len(keys) {
			return false
		}
		for i, k := range p {
			if k != "*" && k != keys[i] {
				return false
			}
		}
		return true
	})
}

// overlap returns the first path, in path order, where layer sets a value
// that base already sets, or "" when there is none. A key overlaps when
// its two values are not both maps, or when the map at its path counts as
// one value (wholePath). The path is formatted with chart.FormatPath.
func overlap(base, layer M, at []string) string {
	for _, k := range slices.Sorted(maps.Keys(layer)) {
		b, ok := base[k]
		if !ok {
			continue
		}
		p := append(slices.Clip(at), k)
		bm, bok := b.(M)
		lm, lok := layer[k].(M)
		if !bok || !lok || wholePath(p) {
			return chart.FormatPath(p)
		}
		if s := overlap(bm, lm, p); s != "" {
			return s
		}
	}
	return ""
}

// layer is a values file of the tree at its level.
type layer struct {
	level  uint
	file   string // the file, its repository and path, as messages name it
	values M
}

// levels holds the layers of an instance's values checked so far, by
// level, lowest first. add never changes a levels: it returns a new one,
// so that the platform's and a project's are shared by their instances.
type levels struct {
	namespace string     // the project, which its connections' remoteKey names
	byLevel   []levelSet // what each level sets, its layers merged
	layers    []M        // every layer as the compiler receives it, in order
}

// levelSet is what the layers of one level set, merged.
type levelSet struct {
	level  uint
	files  []string
	values M
}

// add checks l over the layers of ls and returns ls with it. It returns
// ls with l even when l is refused: the problems of the levels above are
// still reported. Layers are added lowest level first; those of one
// level may change each other. access, the project's file's, is left
// out of the layer the compiler receives.
func (ls levels) add(l layer) (levels, error) {
	v, err := value.Normalize(l.values)
	if err != nil {
		return ls, fmt.Errorf("%s: %w", l.file, err)
	}
	values := value.AsMap(v)
	out := levels{namespace: ls.namespace, byLevel: slices.Clone(ls.byLevel), layers: slices.Clone(ls.layers)}
	if n := len(out.byLevel); n == 0 || out.byLevel[n-1].level != l.level {
		out.byLevel = append(out.byLevel, levelSet{level: l.level, values: M{}})
	}
	var errs []error
	for _, lower := range out.byLevel[:len(out.byLevel)-1] {
		if p := overlap(lower.values, values, nil); p != "" {
			errs = append(errs, fmt.Errorf("%s: %s is set at level %d, by %s: a level adds values, it does not change a lower level's", l.file, p, lower.level, strings.Join(lower.files, ", ")))
		}
	}
	errs = append(errs, ls.checkKeys(l, values)...)
	if l.level > 0 {
		errs = append(errs, chart.NoTemplate(values, func(p string) string { return l.file + ": " + p }))
	}
	delete(values, AccessKey)
	cur := &out.byLevel[len(out.byLevel)-1]
	cur.files = append(slices.Clone(cur.files), l.file)
	cur.values = value.Merge(value.Merge(M{}, cur.values), values)
	if l.level > 0 {
		errs = append(errs, out.checkRemoteKeys(l.file, values, cur.values)...)
	}
	out.layers = append(out.layers, values)
	return out, errors.Join(errs...)
}

// checkKeys refuses what the level of l may not set: of global.okdp
// above level 0, anything but projectSettings; of the rest of global
// above level 0, a key the platform does not list; claims at level 0;
// access but at level 1.
func (ls levels) checkKeys(l layer, values M) []error {
	var errs []error
	global := value.AsMap(values["global"])
	if okdp, set := global["okdp"]; set && l.level > 0 {
		m, isMap := okdp.(M)
		if !isMap {
			errs = append(errs, ls.platformOnly(l.file, "global.okdp"))
		}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if !slices.Contains(projectSettings, k) {
				errs = append(errs, ls.platformOnly(l.file, value.Key("global.okdp", k)))
			}
		}
	}
	if l.level > 0 {
		allowed := ls.projectGlobals()
		shown := "none"
		if len(allowed) > 0 {
			shown = strings.Join(allowed, ", ")
		}
		for _, k := range slices.Sorted(maps.Keys(global)) {
			if k == "okdp" || slices.Contains(allowed, k) {
				continue
			}
			field := value.Key("global", k)
			errs = append(errs, fmt.Errorf("%s: %w", l.file, value.AtField(field, fmt.Errorf("%s: a key of global the platform does not let a project set: global.okdp.policy.projectGlobals of %s lists %s; every chart of the instances receives global, past their package's protect and the upstream policy", field, ls.platformFiles(), shown))))
		}
	}
	if _, set := values["claims"]; set && l.level == 0 {
		errs = append(errs, fmt.Errorf("%s: claims: a project's (%s)", l.file, ProjectFile))
	}
	if value.Dig(values, groupsSetting) != nil && l.level == 0 {
		errs = append(errs, fmt.Errorf("%s: %s: the roles of a project's groups, of its %s (%s), which the tree gives its instances", l.file, groupsSetting, ProjectFile, AccessKey))
	}
	if _, set := values[AccessKey]; set && l.level != 1 {
		errs = append(errs, fmt.Errorf("%s: %s: the roles of a project's groups, its %s's only", l.file, AccessKey, ProjectFile))
	}
	return errs
}

func (ls levels) platformOnly(file, key string) error {
	var keys []string
	for _, k := range projectSettings {
		keys = append(keys, "global.okdp."+k)
	}
	return fmt.Errorf("%s: %s: a platform setting, of level 0 only (%s): a project sets %s, nothing else of global.okdp", file, key, ls.platformFiles(), strings.Join(keys, ", "))
}

// platformFiles names the files of level 0, PlatformFile when there are
// none.
func (ls levels) platformFiles() string {
	if len(ls.byLevel) > 0 && ls.byLevel[0].level == 0 && len(ls.byLevel[0].files) > 0 {
		return strings.Join(ls.byLevel[0].files, ", ")
	}
	return PlatformFile
}

// projectGlobals returns the keys of global the levels above 0 set:
// global.okdp.policy.projectGlobals of level 0, defaultProjectGlobals
// when level 0 lists none.
func (ls levels) projectGlobals() []string {
	var platform M
	if len(ls.byLevel) > 0 && ls.byLevel[0].level == 0 {
		platform = ls.byLevel[0].values
	}
	listed := value.Dig(platform, "global.okdp.policy.projectGlobals")
	if listed == nil {
		return defaultProjectGlobals
	}
	var out []string
	for _, k := range value.AsList(listed) {
		out = append(out, value.Str(k))
	}
	return out
}

// checkRemoteKeys refuses a connection that values, a layer above level
// 0, declares whose credentials are a key of the platform's secret store
// (secretRef.remoteKey) outside the project's: a project reads the keys
// under <namespace>/ alone, the platform's are read through the
// platform's connections. merged is what the layer's level sets: a
// connection is given whole by its level, so what its level sets is
// what the compile reads. A remoteKey that is no string is the
// compile's to refuse.
func (ls levels) checkRemoteKeys(file string, values, merged M) []error {
	var errs []error
	connections := value.AsMap(merged["connections"])
	for _, name := range slices.Sorted(maps.Keys(value.AsMap(values["connections"]))) {
		key, ok := value.AsMap(value.AsMap(connections[name])["secretRef"])["remoteKey"].(string)
		if !ok || key == "" || strings.HasPrefix(key, ls.namespace+"/") {
			continue
		}
		field := value.Key("connections", name)
		errs = append(errs, fmt.Errorf("%s: %w", file, value.AtField(field, fmt.Errorf("%s: secretRef.remoteKey %q: a key of the platform's secret store a project may not read: a connection above level 0 names a key under %q, its namespace; the platform's keys are read through the platform's connections (%s)", field, key, ls.namespace+"/", ls.platformFiles()))))
	}
	return errs
}

// checkConfig refuses what an instance's values file, file, may not set:
// of global, a key its package's parameter schema does not declare
// (declared, nil when it cannot be read), and okdp; connections, claims
// and access; template code, under any key, parameters or upstream
// values.
func checkConfig(file string, values M, declared M) error {
	v, err := value.Normalize(values)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	values = value.AsMap(v)
	var errs []error
	if g, set := values["global"]; set {
		if _, isMap := g.(M); !isMap {
			errs = append(errs, fmt.Errorf("%s: global: the keys of global the package's parameter schema declares (properties.global.properties), a map: the rest of global is the platform's (%s) or the project's (%s)", file, PlatformFile, ProjectFile))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(value.AsMap(values["global"]))) {
		switch _, ok := declared[k]; {
		case k == "okdp":
			errs = append(errs, fmt.Errorf("%s: global.okdp: the platform's settings, of %s, and the classes a project adds, of its %s", file, PlatformFile, ProjectFile))
		case !ok && declared != nil:
			errs = append(errs, fmt.Errorf("%s: %s: a key of global the package's parameter schema does not declare (properties.global.properties): the rest of global is the platform's (%s) or the project's (%s)", file, value.Key("global", k), PlatformFile, ProjectFile))
		}
	}
	if _, set := values["connections"]; set {
		errs = append(errs, fmt.Errorf("%s: connections: the instance's own are in its %s, the others the platform's or the project's", file, ConnectionsFile))
	}
	if _, set := values["claims"]; set {
		errs = append(errs, fmt.Errorf("%s: claims: a project's (%s)", file, ProjectFile))
	}
	if _, set := values[AccessKey]; set {
		errs = append(errs, fmt.Errorf("%s: %s: the roles of a project's groups, its %s's only", file, AccessKey, ProjectFile))
	}
	errs = append(errs, chart.NoTemplate(values, func(p string) string { return file + ": " + p }))
	return errors.Join(errs...)
}

// declaredGlobals returns the keys of global the parameter schema of the
// package def declares (properties.global.properties), nil when the
// schema cannot be read: the compile refuses it, and checkConfig checks
// no key of global.
func declaredGlobals(def *compiler.Definition) M {
	s, err := chart.ReadParameterSchema(def.Files)
	if err != nil {
		return nil
	}
	declared := M{}
	maps.Copy(declared, value.AsMap(value.Dig(s.Doc, "properties.global.properties")))
	return declared
}

// ProjectAccess reads the roles of the groups of the project of
// namespace from the values of its ProjectFile, as read: access, its
// default group an editor when absent or null (DefaultProjectGroup), {}
// no group. It reports every problem at once.
func ProjectAccess(values map[string]any, namespace string) (map[string]access.Role, error) {
	v := values[AccessKey]
	if v == nil {
		return map[string]access.Role{DefaultProjectGroup(namespace): access.Editor}, nil
	}
	return access.Parse(AccessKey, v)
}

// GroupsSettings returns the settings that give the instances of a
// project the roles of its groups, global.okdp.access.groups
// (access.Settings), which the security set reads: the tree gives them in
// Inputs.Settings, so that no chart receives them. nil for nil groups: no
// settings, no group.
func GroupsSettings(groups map[string]access.Role) map[string]any {
	if groups == nil {
		return nil
	}
	return access.Settings(groups)
}

// Groups returns the roles of the groups the tree gives in
// (GroupsSettings, in.Settings), what the targets write a project's
// access from, whatever rule set it compiles with; nil when it gives
// none.
func Groups(in compiler.Inputs) map[string]access.Role {
	if value.Dig(in.Settings, access.Setting) == nil {
		return nil
	}
	return access.Of(in.Settings)
}

// withGroups returns in with the settings of groups (GroupsSettings),
// none for nil groups.
func withGroups(in compiler.Inputs, groups map[string]access.Role) compiler.Inputs {
	in.Settings = GroupsSettings(groups)
	return in
}

// projectAccess is ProjectAccess of the project's file, file.
func projectAccess(file string, values M, namespace string) (map[string]access.Role, error) {
	groups, err := ProjectAccess(values, namespace)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return groups, nil
}

// globalsCheck runs the host's policy on the global of a file the
// platform does not review (BuildOptions.CheckGlobals), file, which holds
// values: in, an instance's or a project's inputs, gives its settings.
// Settings that do not hold check nothing: the compile refuses them.
func globalsCheck(ctx context.Context, check func(context.Context, policy.GlobalsInput) error, in compiler.Inputs, file string, values M) error {
	g := value.AsMap(values["global"])
	if check == nil || len(g) == 0 {
		return nil
	}
	settings, err := compiler.SettingsOf(ctx, in)
	if err != nil {
		return nil //nolint:nilerr // the compile refuses the settings
	}
	return check(ctx, policy.GlobalsInput{Rules: in.Rules, Settings: settings, Namespace: in.Namespace, Privileged: policy.Privileged(settings, in.Namespace), Layers: []policy.GlobalLayer{{Name: file, Global: g}}})
}
