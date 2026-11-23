package tree

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler-security/policy"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-gitops/rules"
)

// levelsPlatform: a platform's file: a label of every chart, a cluster
// domain, a null setting, the default class of server connections and
// its class, a connection.
const levelsPlatform = "global: {labels: {okdp.io/tier: gold}, okdp: {cluster: {domain: example.org}, registry: null, provisioning: {server: {default: standard, classes: {standard: {provider: data/pg}}}}}}\nconnections: {shared-db: {contract: database, host: db.example.org}}\n"

// levelsTree: a tree of the instance demo/web of the package app, whose
// parameter schema declares replicas and global.imageRegistry, with these
// files: platform.yaml, demo's project.yaml, web's connections.yaml and
// config.yaml; "" leaves the file out.
func levelsTree(platform, project, connections, config string) fstest.MapFS {
	tree := fstest.MapFS{
		"contracts/database.schema.json":            file(`{"properties": {"host": {"type": "string"}, "port": {"type": "integer"}, "secretRef": {"type": "object"}, "password": {"type": "string", "x-okdp-secret": true}}}`),
		"catalog/app/1.0.0/Chart.yaml":              file("apiVersion: v2\nname: app\nversion: 1.0.0\n"),
		"catalog/app/1.0.0/config.schema.json":      file(`{"properties": {"replicas": {"type": "integer"}, "global": {"properties": {"imageRegistry": {"type": "string"}}}}}`),
		"projects/demo/instances/web/instance.yaml": file("service: app\nversion: 1.0.0\n"),
	}
	for name, s := range map[string]string{PlatformFile: platform, "projects/demo/project.yaml": project, "projects/demo/instances/web/connections.yaml": connections, "projects/demo/instances/web/config.yaml": config} {
		if s != "" {
			tree[name] = file(s)
		}
	}
	return tree
}

// loadOne: the instance of tree and the error of Load.
func loadOne(t *testing.T, tree fstest.MapFS) (compiler.Instance, error) {
	t.Helper()
	instances, err := Load(context.Background(), []Repository{{"db", tree, RoleTree}}, nil)
	if err != nil {
		return compiler.Instance{}, err
	}
	if len(instances) != 1 {
		t.Fatalf("%d instances", len(instances))
	}
	return instances[0], nil
}

// wantErr: err holds want, or is nil when want is "".
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("got %v, want no error", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Fatalf("got %v\nwant an error containing %q", err, want)
	}
}

// TestLevels: a level adds to the lower levels' values, it changes none:
// not a value they set (the same one included, a null too, a parent of
// it), not a connection they give, even by a field they do not set, nor a
// class they define or the default class they set: it adds classes beside
// theirs, and the classes of a contract they leave out; nothing else of
// global.okdp, the platform's settings, whether the platform sets the key
// or not, nor global.okdp itself; no template code. platform.yaml is level
// 0, project.yaml level 1, connections.yaml level 2.
func TestLevels(t *testing.T) {
	const added = " is set at level 0, by db/platform.yaml: a level adds values, it does not change a lower level's"
	const only = ": a platform setting, of level 0 only (db/platform.yaml): a project sets global.okdp.provisioning, nothing else of global.okdp"
	for name, tc := range map[string]struct {
		project, connections string
		want                 string // the error, none when empty
	}{
		"added":                             {"global: {storageClass: fast, project: {name: demo}}\nconnections: {team-db: {contract: database, host: a}}\n", "connections: {own: {contract: database, host: b}}\n", ""},
		"a platform value":                  {"global: {okdp: {cluster: {domain: other.example.org}}}\n", "", "db/projects/demo/project.yaml: global.okdp.cluster.domain" + added},
		"the same value":                    {"global: {okdp: {cluster: {domain: example.org}}}\n", "", "db/projects/demo/project.yaml: global.okdp.cluster.domain" + added},
		"a platform null":                   {"global: {okdp: {registry: {host: registry.example.org}}}\n", "", "db/projects/demo/project.yaml: global.okdp.registry" + added},
		"a parent":                          {"global: {okdp: null}\n", "", "db/projects/demo/project.yaml: global.okdp" + added},
		"a key with dots":                   {"global: {labels: {okdp.io/tier: silver}}\n", "", `db/projects/demo/project.yaml: global.labels["okdp.io/tier"]` + added},
		"a platform connection":             {"connections: {shared-db: {port: 5433}}\n", "", "db/projects/demo/project.yaml: connections.shared-db" + added},
		"an instance's platform connection": {"", "connections: {shared-db: {port: 5433}}\n", "db/projects/demo/instances/web/connections.yaml: connections.shared-db" + added},
		"a project's connection":            {"connections: {team-db: {contract: database, host: a}}\n", "connections: {team-db: {host: b}}\n", "db/projects/demo/instances/web/connections.yaml: connections.team-db is set at level 1, by db/projects/demo/project.yaml: a level adds values"},
		"a template":                        {"connections: {team-db: {contract: database, host: '{{ .Release.Name }}'}}\n", "", `db/projects/demo/project.yaml: connections.team-db.host: the value contains "{{"`},
		"an instance's template":            {"", "connections: {own: {contract: database, host: '{{ .Release.Name }}'}}\n", `db/projects/demo/instances/web/connections.yaml: connections.own.host: the value contains "{{"`},
		"a platform class":                  {"global: {okdp: {provisioning: {server: {classes: {standard: {request: {size: 1Gi}}}}}}}\n", "", "db/projects/demo/project.yaml: global.okdp.provisioning.server.classes.standard" + added},
		"the default class":                 {"global: {okdp: {provisioning: {server: {default: fast}}}}\n", "", "db/projects/demo/project.yaml: global.okdp.provisioning.server.default" + added},
		"a class added":                     {"global: {okdp: {provisioning: {server: {classes: {fast: {provider: data/pg2}}}, s3: {default: standard, classes: {standard: {provider: data/minio}}}}}}\n", "", ""},
		"a value of no layer":               {"replicas: 3\n", "", ""},
		"a value of no platform":            {"global: {okdp: {monitoring: {enabled: true}}}\n", "", "db/projects/demo/project.yaml: global.okdp.monitoring" + only},
		"a platform chart":                  {"global: {okdp: {provisioning: {s3: {classes: {}}}, charts: {objects: {repository: 'oci://attacker.example/x', chart: okdp-objects, version: 0.6.0}}}}\n", "", "db/projects/demo/project.yaml: global.okdp.charts" + only},
		"the privileged projects":           {"global: {okdp: {policy: {privileged: [demo]}}}\n", "", "db/projects/demo/project.yaml: global.okdp.policy" + only},
		"a key the compiler does not read":  {"global: {okdp: {project: {name: demo}}}\n", "", "db/projects/demo/project.yaml: global.okdp.project" + only},
		"the platform's ingress":            {"global: {okdp: {ingress: {annotations: {nginx.ingress.kubernetes.io/auth-url: https://attacker.example/auth}}}}\n", "", "db/projects/demo/project.yaml: global.okdp.ingress" + only},
		"the platform's oidc":               {"global: {okdp: {oidc: {scope: openid profile email groups admin}}}\n", "", "db/projects/demo/project.yaml: global.okdp.oidc" + only},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadOne(t, levelsTree(levelsPlatform, tc.project, tc.connections, ""))
			wantErr(t, err, tc.want)
		})
	}
	// global.okdp not a map above level 0, where level 0 sets none.
	_, err := loadOne(t, levelsTree("", "global: {okdp: null}\n", "", ""))
	wantErr(t, err, "db/projects/demo/project.yaml: global.okdp: a platform setting, of level 0 only (platform.yaml)")
	// The layers in order, the platform's, the project's, the instance's
	// connections and its values: a project's global reaches every chart,
	// the instance's values win.
	inst, err := loadOne(t, levelsTree(levelsPlatform, "global: {storageClass: fast}\nreplicas: 3\n", "connections: {own: {contract: database, host: b}}\n", "replicas: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	merged := compiler.MergeValues(inst.Inputs.Values...)
	if len(inst.Inputs.Values) != 4 || value.Dig(merged, "global.storageClass") != "fast" || value.Str(merged["replicas"]) != "2" || value.Dig(merged, "connections.own.host") != "b" || value.Dig(merged, "global.okdp.cluster.domain") != "example.org" {
		t.Errorf("values: %#v", inst.Inputs.Values)
	}
}

// TestLevelsAdd: what a tree cannot give, its connections.yaml holding
// connections only: layers of one level change each other, a level above
// changes theirs not; a level's claims are taken whole; levels above 1
// set no global.okdp but provisioning and no key of global the platform
// does not list either; access is level 1's alone, claims never level
// 0's; the layers given are not changed.
func TestLevelsAdd(t *testing.T) {
	at := func(level uint, file string, v M) layer { return layer{level: level, file: file, values: v} }
	for name, tc := range map[string]struct {
		layers []layer
		want   string
	}{
		"a level changes itself": {[]layer{at(1, "p1", M{"global": M{"storageClass": "fast"}}), at(1, "p2", M{"global": M{"storageClass": "slow"}})}, ""},
		"a third level":          {[]layer{at(1, "p", M{"global": M{"storageClass": "fast"}}), at(2, "c", M{"global": M{"storageClass": "slow"}})}, "c: global.storageClass is set at level 1, by p: a level adds values"},
		"a project's claim":      {[]layer{at(1, "p", M{"claims": M{"reports": M{"contract": "database"}}}), at(2, "c", M{"claims": M{"reports": M{"provider": "data/pg"}}})}, "c: claims.reports is set at level 1, by p"},
		"a higher setting":       {[]layer{at(2, "c", M{"global": M{"okdp": M{"proxy": M{"noProxy": ".svc"}}}})}, "c: global.okdp.proxy: a platform setting, of level 0 only (pl)"},
		"the platform's claims":  {[]layer{at(0, "pl2", M{"claims": M{"x": M{}}})}, "pl2: claims: a project's (project.yaml)"},
		"access of level 2":      {[]layer{at(2, "c", M{"access": M{}})}, "c: access: the roles of a project's groups, its project.yaml's only"},
		"access of level 0":      {[]layer{at(0, "pl2", M{"access": M{"a": "viewer"}})}, "pl2: access: the roles of a project's groups"},
		"every problem":          {[]layer{at(1, "p", M{"global": M{"okdp": M{"tls": M{}, "proxy": M{}}, "zone": "a", "app": "b"}})}, "p: global.okdp.proxy: a platform setting"},
	} {
		t.Run(name, func(t *testing.T) {
			ls, err := levels{namespace: "demo"}.add(at(0, "pl", M{"global": M{"labels": M{"a": "b"}}}))
			if err != nil {
				t.Fatal(err)
			}
			var errs []error
			for _, l := range tc.layers {
				ls, err = ls.add(l)
				errs = append(errs, err)
			}
			wantErr(t, errors.Join(errs...), tc.want)
		})
	}
	// Every problem of a layer at once.
	ls, _ := levels{namespace: "demo"}.add(at(0, "pl", M{}))
	_, err := ls.add(at(1, "p", M{"global": M{"okdp": M{"tls": M{}, "proxy": M{}}, "zone": "a", "app": "b"}}))
	for _, w := range []string{"p: global.okdp.proxy:", "p: global.okdp.tls:", "p: global.app:", "p: global.zone:"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("got %v, want %s", err, w)
		}
	}
	// access is no value of the compile; the layers given are unchanged,
	// those of the levels shared by their instances too.
	project := M{"access": M{"a": "viewer"}, "global": M{"storageClass": "fast"}}
	before := value.Copy(project)
	base, _ := levels{namespace: "demo"}.add(at(0, "pl", M{"global": M{"labels": M{"a": "b"}}}))
	with, err := base.add(at(1, "p", project))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := with.layers[1]["access"]; set || !reflect.DeepEqual(project, before) || len(base.layers) != 1 || len(base.byLevel) != 1 {
		t.Errorf("layers %#v, given %#v, base %#v", with.layers, project, base)
	}
	other, _ := base.add(at(1, "q", M{"global": M{"storageClass": "slow"}}))
	if value.Dig(with.byLevel[1].values, "global.storageClass") != "fast" || value.Dig(other.byLevel[1].values, "global.storageClass") != "slow" {
		t.Error("two projects share their level")
	}
}

// TestConfigValues: an instance's config.yaml sets only the keys of
// global its package's parameter schema declares, never okdp, no
// connections, claims or access, and no "{{" under any key; the
// platform's file gives template code.
func TestConfigValues(t *testing.T) {
	const at = "db/projects/demo/instances/web/config.yaml: "
	for name, tc := range map[string]struct {
		config, want string
	}{
		"a declared key": {"global: {imageRegistry: instance.example.org}\n", ""},
		"okdp":           {"global: {okdp: {cluster: {domain: other.example.org}}}\n", at + "global.okdp: the platform's settings, of platform.yaml, and the classes a project adds, of its project.yaml"},
		"another key":    {"replicas: 2\nglobal: {imageRegistry: r, proxy: p}\n", at + "global.proxy: a key of global the package's parameter schema does not declare (properties.global.properties): the rest of global is the platform's (platform.yaml) or the project's (project.yaml)"},
		"not a map":      {"global: null\n", at + "global: the keys of global the package's parameter schema declares (properties.global.properties), a map"},
		"connections":    {"connections: {}\n", at + "connections: the instance's own are in its connections.yaml, the others the platform's or the project's"},
		"a null":         {"connections: null\n", at + "connections: the instance's own"},
		"claims":         {"claims: {reports: {contract: database}}\n", at + "claims: a project's (project.yaml)"},
		"access":         {"access: {analysts: admin}\n", at + "access: the roles of a project's groups, its project.yaml's only"},
		"a template":     {"replicas: 2\nupstream: {app: {tags: ['{{ .Release.Name }}']}}\n", at + `upstream.app.tags[0]: the value contains "{{"`},
		"a template key": {"upstream: {app: {podAnnotations: {'{{ x }}': y}}}\n", at + `upstream.app.podAnnotations.{{ x }}: the key contains "{{"`},
	} {
		t.Run(name, func(t *testing.T) {
			inst, err := loadOne(t, levelsTree("global: {imageRegistry: platform.example.org}\n", "", "", tc.config))
			wantErr(t, err, tc.want)
			if err == nil && compiler.MergeValues(inst.Inputs.Values...)["global"].(M)["imageRegistry"] != "instance.example.org" {
				t.Errorf("values: %#v", inst.Inputs.Values)
			}
		})
	}
	if _, err := loadOne(t, levelsTree("global: {imageRegistry: '{{ .Values.registry }}'}\n", "", "", "")); err != nil {
		t.Errorf("a template at level 0: %v", err)
	}
	// A schema that cannot be read: the compile's to refuse, no key of
	// global refused for it.
	tree := levelsTree("", "", "", "global: {imageRegistry: r}\n")
	tree["catalog/app/1.0.0/config.schema.json"] = file("{")
	if _, err := loadOne(t, tree); err != nil {
		t.Errorf("an unreadable schema: %v", err)
	}
}

// TestProjectGlobals: a project's file sets the keys of global the
// platform lists (global.okdp.policy.projectGlobals, project and
// storageClass by default), the same for a privileged project; the
// refusals are field problems at global.<key>. The platform's file sets
// any.
func TestProjectGlobals(t *testing.T) {
	policyOf := func(p string) string { return "global: {okdp: {policy: " + p + "}}\n" }
	for name, tc := range map[string]struct {
		platform, global string
		want             string // the error, none when empty
		field            string // the field it is tagged with
	}{
		"the defaults":                   {policyOf("{}"), "{project: {name: demo}, storageClass: fast}", "", ""},
		"imageRegistry":                  {policyOf("{}"), "{imageRegistry: evil.example.com}", "db/projects/demo/project.yaml: global.imageRegistry: a key of global the platform does not let a project set: global.okdp.policy.projectGlobals of db/platform.yaml lists project, storageClass; every chart of the instances receives global, past their package's protect and the upstream policy", "global.imageRegistry"},
		"security":                       {policyOf("{}"), "{security: {allowInsecureImages: true}}", "global.security: a key of global the platform does not let a project set", "global.security"},
		"podSecurity":                    {policyOf("{}"), "{podSecurity: {privileged: true}}", "global.podSecurity: a key of global the platform does not let a project set", "global.podSecurity"},
		"a key with dots":                {policyOf("{}"), "{okdp.io/tier: gold}", `global["okdp.io/tier"]: a key of global`, `global["okdp.io/tier"]`},
		"the platform's list":            {policyOf("{projectGlobals: [imageRegistry]}"), "{imageRegistry: registry.example.org}", "", ""},
		"the list replaces the defaults": {policyOf("{projectGlobals: [imageRegistry]}"), "{storageClass: fast}", "global.storageClass: a key of global the platform does not let a project set: global.okdp.policy.projectGlobals of db/platform.yaml lists imageRegistry", "global.storageClass"},
		"none":                           {policyOf("{projectGlobals: []}"), "{project: {}}", "projectGlobals of db/platform.yaml lists none", "global.project"},
		"a listed map":                   {policyOf("{projectGlobals: [image]}"), "{image: {registry: registry.example.org, repository: app}}", "", ""},
		"a privileged project":           {policyOf("{privileged: [demo]}"), "{project: {hostNetwork: true}}", "", ""},
		"a privileged project's keys":    {policyOf("{privileged: [demo]}"), "{imageRegistry: evil.example.com}", "global.imageRegistry: a key of global the platform does not let a project set", "global.imageRegistry"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadOne(t, levelsTree(tc.platform, "global: "+tc.global+"\n", "", ""))
			wantErr(t, err, tc.want)
			if tc.want == "" {
				return
			}
			if fe := compiler.FieldErrors(err); len(fe) != 1 || fe[0].Path != tc.field || !strings.HasPrefix(fe[0].Message, tc.field+": ") {
				t.Errorf("got %#v, want a problem at %q", fe, tc.field)
			}
		})
	}
	// Every refused key a problem of its own, in key order.
	_, err := loadOne(t, levelsTree("", "global: {zone: a, app: b}\n", "", ""))
	if fe := compiler.FieldErrors(err); len(fe) != 2 || fe[0].Path != "global.app" || fe[1].Path != "global.zone" {
		t.Errorf("two keys: %#v", fe)
	}
	// The platform's global is its own, whatever the list.
	if _, err := loadOne(t, levelsTree("global: {imageRegistry: docker.io, okdp: {policy: {projectGlobals: [project]}}}\n", "", "", "global: {imageRegistry: r}\n")); err != nil {
		t.Errorf("the platform's global: %v", err)
	}
}

// TestDeclarationsLevels: a project without instance has its file checked
// as the load of an instance checks it: what it may set, and its
// declarations (compiler.CheckDeclarations).
func TestDeclarationsLevels(t *testing.T) {
	for name, tc := range map[string]struct {
		project, want string
	}{
		"listed":         {"global: {storageClass: fast, project: {name: demo}}\n", ""},
		"unlisted":       {"global: {imageRegistry: evil.example.com}\n", "db/projects/demo/project.yaml: global.imageRegistry: a key of global the platform does not let a project set"},
		"a setting":      {"global: {okdp: {policy: {privileged: [demo]}}}\n", "db/projects/demo/project.yaml: global.okdp.policy: a platform setting"},
		"a template":     {"global: {project: {name: '{{ x }}'}}\n", `db/projects/demo/project.yaml: global.project.name: the value contains "{{"`},
		"a declaration":  {"connections: {x: {contract: nope}}\n", "db/projects/demo/project.yaml: connections.x: "},
		"a platform key": {"connections: {shared-db: {port: 1}}\n", "db/projects/demo/project.yaml: connections.shared-db is set at level 0"},
	} {
		t.Run(name, func(t *testing.T) {
			tree := levelsTree(levelsPlatform, tc.project, "", "")
			delete(tree, "projects/demo/instances/web/instance.yaml")
			l, err := LoadProjects(context.Background(), []Repository{{"db", tree, RoleTree}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got error
			if len(l.Refused) > 0 {
				got = errors.Join(l.Refused[0].Errs...)
			}
			wantErr(t, got, tc.want)
		})
	}
}

// TestRemoteKeys: a connection above level 0 whose credentials are in the
// platform's secret store names a key of its namespace, <namespace>/...,
// a field problem at its connection; the platform's connections read
// any; a remoteKey that is no string is the compile's.
func TestRemoteKeys(t *testing.T) {
	platform := "global: {okdp: {secrets: {store: okdp-secrets}}}\nconnections: {lake: {contract: database, host: h, secretRef: {remoteKey: platform/lake}}}\n"
	conn := func(key string) string {
		return "connections: {own: {contract: database, host: h, secretRef: {remoteKey: '" + key + "'}}}\n"
	}
	scoped := func(file, key string) string {
		return file + `: connections.own: secretRef.remoteKey "` + key + `": a key of the platform's secret store a project may not read: a connection above level 0 names a key under "demo/", its namespace; the platform's keys are read through the platform's connections (db/platform.yaml)`
	}
	for name, tc := range map[string]struct {
		project, connections, want string
	}{
		"its namespace's":   {"", conn("demo/lake"), ""},
		"a project's":       {conn("demo/x/y"), "", ""},
		"another namespace": {"", conn("data/demo.app.db"), scoped("db/projects/demo/instances/web/connections.yaml", "data/demo.app.db")},
		"the platform's":    {conn("platform/lake"), "", scoped("db/projects/demo/project.yaml", "platform/lake")},
		"the namespace":     {"", conn("demo"), scoped("db/projects/demo/instances/web/connections.yaml", "demo")},
		"a prefix of it":    {"", conn("demo-x/lake"), scoped("db/projects/demo/instances/web/connections.yaml", "demo-x/lake")},
		"not a string":      {"", "connections: {own: {contract: database, host: h, secretRef: {remoteKey: 5}}}\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadOne(t, levelsTree(platform, tc.project, tc.connections, ""))
			wantErr(t, err, tc.want)
			if tc.want == "" {
				return
			}
			if fe := compiler.FieldErrors(err); len(fe) != 1 || fe[0].Path != "connections.own" {
				t.Errorf("got %#v, want a problem at connections.own", fe)
			}
		})
	}
}

// TestAccess: the roles of a project's groups are its file's access,
// given every instance of it in its settings (GroupsSettings,
// Inputs.Settings) and none of their values: the default group an editor
// without one, none with {}; refused, the project does not load; access
// in another file, global.okdp.access in a project's, or the groups'
// setting in the platform's, refused.
func TestAccess(t *testing.T) {
	platform := "global: {okdp: {access: {admins: [platform_admin], groupPrefix: 'k8s:'}}}\n"
	for name, tc := range map[string]struct {
		project string
		want    map[string]compiler.AccessRole
	}{
		"given":   {"access: {analysts: viewer, okdp-project-demo: admin}\n", map[string]compiler.AccessRole{"analysts": compiler.AccessViewer, "okdp-project-demo": compiler.AccessAdmin}},
		"absent":  {"", map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessEditor}},
		"null":    {"access: null\n", map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessEditor}},
		"no file": {"-", map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessEditor}},
		"none":    {"access: {}\n", map[string]compiler.AccessRole{}},
	} {
		t.Run(name, func(t *testing.T) {
			project := tc.project
			if project == "-" {
				project = ""
			} else {
				project = "description: d\n" + project
			}
			inst, err := loadOne(t, levelsTree(platform, project, "", ""))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(Groups(inst.Inputs), tc.want) || !reflect.DeepEqual(inst.Inputs.Settings, GroupsSettings(tc.want)) {
				t.Errorf("groups %v, want %v", inst.Inputs.Settings, tc.want)
			}
			for _, v := range inst.Inputs.Values {
				if _, set := v[AccessKey]; set {
					t.Errorf("a values layer holds access: %v", v)
				}
			}
		})
	}
	for name, tc := range map[string]struct {
		platform, project, config, want string
	}{
		"refused":               {platform, "access: {analysts: owner}\n", "", `db/projects/demo/project.yaml: access.analysts: a role, viewer, editor or admin, got "owner"`},
		"the platform's":        {platform + "access: {analysts: viewer}\n", "", "", "db/platform.yaml: access: the roles of a project's groups, its project.yaml's only"},
		"the values":            {platform, "", "access: {analysts: admin}\n", "db/projects/demo/instances/web/config.yaml: access: the roles of a project's groups"},
		"a project's okdp":      {platform, "global: {okdp: {access: {admins: [demo]}}}\n", "", "db/projects/demo/project.yaml: global.okdp.access: a platform setting, of level 0 only"},
		"the platform's groups": {"global: {okdp: {access: {groups: {analysts: admin}}}}\n", "", "", "db/platform.yaml: global.okdp.access.groups: the roles of a project's groups, of its project.yaml (access), which the tree gives its instances"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadOne(t, levelsTree(tc.platform, tc.project, "", tc.config))
			wantErr(t, err, tc.want)
		})
	}
	// A project whose access is refused keeps the platform's values
	// alone, and no group.
	l, err := LoadProjects(context.Background(), []Repository{{"db", levelsTree(platform, "access: {a/b: viewer}\n", "", ""), RoleTree}}, nil)
	if err != nil || len(l.Refused) != 1 || l.Refused[0].Own || len(l.Refused[0].Inputs.Values) != 1 || l.Refused[0].Inputs.Settings != nil {
		t.Errorf("refused: %v %+v", err, l)
	}
}

// TestProjectAccess: a project's access maps group names to a role, every
// problem at once; absent or null, the default group an editor; {} no
// group.
func TestProjectAccess(t *testing.T) {
	for name, tc := range map[string]struct {
		access any
		want   map[string]compiler.AccessRole
		err    string // the error, none when empty
	}{
		"absent":       {nil, map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessEditor}, ""},
		"none":         {M{}, map[string]compiler.AccessRole{}, ""},
		"every role":   {M{"okdp-project-demo": "admin", "analysts": "viewer", "data.eng_team@corp:x": "editor"}, map[string]compiler.AccessRole{"okdp-project-demo": compiler.AccessAdmin, "analysts": compiler.AccessViewer, "data.eng_team@corp:x": compiler.AccessEditor}, ""},
		"not a map":    {[]any{"okdp-project-demo"}, nil, "access: the roles of groups, a map of group names to viewer, editor or admin"},
		"a path":       {M{"/okdp/demo": "viewer"}, nil, `access./okdp/demo: a group name`},
		"a space":      {M{"demo team": "viewer"}, nil, `access.demo team: a group name`},
		"a comma":      {M{"a,b": "viewer"}, nil, `a group name`},
		"too long":     {M{strings.Repeat("a", 256): "viewer"}, nil, `a group name`},
		"no role":      {M{"analysts": "owner"}, nil, `access.analysts: a role, viewer, editor or admin, got "owner"`},
		"not a string": {M{"analysts": true}, nil, `access.analysts: a role, viewer, editor or admin, got true`},
		"every error":  {M{"a b": "viewer", "analysts": nil}, nil, "access.a b: a group name"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ProjectAccess(M{AccessKey: tc.access}, "demo")
			if tc.err != "" {
				wantErr(t, err, tc.err)
				if name == "every error" && !strings.Contains(err.Error(), "access.analysts: a role") {
					t.Errorf("not every problem: %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, %v, want %v", got, err, tc.want)
			}
		})
	}
	if DefaultProjectGroup("demo") != "okdp-project-demo" {
		t.Error(DefaultProjectGroup("demo"))
	}
}

// buildLevels: Build of tree with the standard rule set and check as
// BuildOptions.CheckGlobals.
func buildLevels(t *testing.T, tree fstest.MapFS, check func(context.Context, policy.GlobalsInput) error) *Tree {
	t.Helper()
	tr, err := Build(context.Background(), []Repository{{"db", tree, RoleTree}}, BuildOptions{Rules: rules.Standard(), CheckGlobals: check})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// TestCheckGlobalsHook: the host's policy (BuildOptions.CheckGlobals) sees
// the global of the project's file and of the instance's values, by file,
// with the settings, the namespace and the rule set; never the
// platform's; what it refuses refuses the project; a project without
// instances is checked too. Load calls none.
func TestCheckGlobalsHook(t *testing.T) {
	tree := levelsTree("global: {imageRegistry: p, okdp: {policy: {privileged: [demo]}}}\n", "global: {project: {a: b}}\n", "", "global: {imageRegistry: i}\n")
	var got []string
	tr := buildLevels(t, tree, func(_ context.Context, g policy.GlobalsInput) error {
		if g.Namespace != "demo" || g.Rules != rules.Standard() || !g.Privileged || value.Dig(g.Settings, "policy.privileged") == nil {
			t.Errorf("the input: %+v", g)
		}
		for _, l := range g.Layers {
			got = append(got, l.Name+" "+strings.Join(slices.Sorted(maps.Keys(l.Global)), ","))
		}
		return nil
	})
	if want := []string{"db/projects/demo/project.yaml project", "db/projects/demo/instances/web/config.yaml imageRegistry"}; !reflect.DeepEqual(got, want) || len(tr.Refused) != 0 {
		t.Errorf("the layers: %q, refused %+v", got, tr.Refused)
	}
	tr = buildLevels(t, tree, func(context.Context, policy.GlobalsInput) error { return errors.New("refused") })
	if len(tr.Refused) != 1 || tr.Refused[0].Problems[0] != "refused" || len(tr.Instances) != 0 {
		t.Errorf("refused: %+v", tr.Refused)
	}
	delete(tree, "projects/demo/instances/web/instance.yaml")
	delete(tree, "projects/demo/instances/web/config.yaml")
	got = nil
	tr = buildLevels(t, tree, func(_ context.Context, g policy.GlobalsInput) error {
		got = append(got, g.Layers[0].Name)
		return errors.New("refused")
	})
	if !reflect.DeepEqual(got, []string{"db/projects/demo/project.yaml"}) || len(tr.Refused) != 1 {
		t.Errorf("no instance: %q %+v", got, tr.Refused)
	}
}

// TestUpstreamGlobals: the security set's policy on the globals
// (policy.CheckGlobals): in a key of global the platform lets a project
// set, an image from a registry the platform does not list, and what the
// upstream policy denies an instance, at any depth, refused at its field;
// a privileged project sets it.
func TestUpstreamGlobals(t *testing.T) {
	const denied = " is denied to the instance's upstream values (a pod's privileges, identity, containers or objects): the platform's projects set it (global.okdp.policy.privileged), demo is not one of them"
	for name, tc := range map[string]struct {
		policy, global string
		field, want    string
	}{
		"a listed registry": {"{projectGlobals: [image], upstream: {registries: [registry.example.org]}}", "{image: {registry: registry.example.org, repository: app}}", "", ""},
		"another image":     {"{projectGlobals: [image], upstream: {registries: [registry.example.org]}}", "{image: {registry: docker.io, repository: evil/app}}", "global.image", "docker.io"},
		"securityContext":   {"{projectGlobals: [project]}", "{project: {securityContext: {runAsUser: 0}}}", "global.project.securityContext", "db/projects/demo/project.yaml: global.project.securityContext: securityContext" + denied},
		"hostNetwork":       {"{projectGlobals: [project]}", "{project: {pod: {hostNetwork: true}}}", "global.project.pod.hostNetwork", "global.project.pod.hostNetwork: hostNetwork" + denied},
		"runAsUser 0":       {"{projectGlobals: [project]}", "{project: {runAsUser: 0}}", "global.project.runAsUser", "global.project.runAsUser: runAsUser 0, root, is denied to the instance's upstream values"},
		"free keys":         {"{projectGlobals: [project]}", "{project: {nodeSelector: {a: b}, runAsUser: 1000}}", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			build := func(privileged string) *Tree {
				p := strings.TrimSuffix(tc.policy, "}") + privileged + "}"
				return buildLevels(t, levelsTree("global: {okdp: {policy: "+p+"}}\n", "global: "+tc.global+"\n", "", ""), policy.CheckGlobals)
			}
			tr := build("")
			err := LoadErrorOf(tr)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantErr(t, err, tc.want)
			if fe := compiler.FieldErrors(err); len(fe) != 1 || !strings.HasPrefix(fe[0].Path, tc.field) {
				t.Errorf("got %#v, want a problem at %q", fe, tc.field)
			}
			if err := LoadErrorOf(build(", privileged: [demo]")); err != nil {
				t.Errorf("a privileged project: %v", err)
			}
		})
	}
	// Load runs no policy.
	if _, err := loadOne(t, levelsTree("global: {okdp: {policy: {projectGlobals: [project]}}}\n", "global: {project: {runAsUser: 0}}\n", "", "")); err != nil {
		t.Errorf("Load: %v", err)
	}
}
