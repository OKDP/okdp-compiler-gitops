package tree

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"
)

// FuzzLoad: a tree whatever its files hold: Load and LoadProjects never
// panic and give the same answer twice; a tree Load accepts is accepted by
// LoadProjects with no project refused, and each of its instances has its
// project's namespace, its directory's name and the release that is the
// project's and its name (the compile checks they are labels); a tree it
// refuses is an *LoadError
// with a problem at least; and LoadProjects gives no instance to a project
// it refuses, which says why. Seeds: the files of TestLoad, TestLoadErrors
// and TestLoadProjects.
func FuzzLoad(f *testing.F) {
	platform := "global: {okdp: {cluster: {domain: example.org}}}\n"
	instance := "service: app\nversion: 1.0.0\n"
	for _, s := range [][5]string{
		{platform, "description: The demo\nglobal: {team: demo}\nclaims: {reports: {contract: database}}\n", instance, "replicas: 2\n", "connections: {ext: {contract: database, host: h}}\n"},
		{"{}\n", "{}\n", instance, "", ""},
		{"claims: {reports: {contract: database}}\n", "", instance + "replicas: 2\n", "[a\n", "connections: {}\nglobal: {okdp: {}}\n"},
		{platform, "[a\n", instance, "", ""},
		{platform, "description: {text: d}\n", "service: nope\nversion: 1.0.0\n", "{}\n", ""},
		{platform, "", "service: app\nversion: ../1.0.0\n", "", ""},
		{platform, "", instance + "claimers: [demo, '*']\n", "", ""},
		{platform, "", instance + "claimers: [demo, Demo]\n", "", ""},
		{platform, "access: {analysts: viewer}\n", instance, "global: {okdp: {}}\n", "connections: {a: {contract: x, b: c}}\n"},
		{"", "", "", "", ""},
	} {
		f.Add([]byte(s[0]), []byte(s[1]), []byte(s[2]), []byte(s[3]), []byte(s[4]), "a")
	}
	f.Add([]byte("{}\n"), []byte(""), []byte("service: app\nversion: 1.0.0\n"), []byte(""), []byte(""), "Not_A_Name")
	f.Add([]byte("{}\n"), []byte(""), []byte("service: app\nversion: 1.0.0\n"), []byte(""), []byte(""), "..")
	f.Add([]byte("{}\n"), []byte(""), []byte("service: app\nversion: 1.0.0\n"), []byte(""), []byte(""), "a/b")
	f.Fuzz(func(t *testing.T, platform, project, instance, config, connections []byte, dir string) {
		tree := fstest.MapFS{
			"catalog/app/1.0.0/Chart.yaml":                         {Data: []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n")},
			"contracts/database.schema.json":                       {Data: []byte(`{"properties": {"host": {"type": "string"}}}`)},
			"platform.yaml":                                        {Data: platform},
			"projects/demo/project.yaml":                           {Data: project},
			"projects/demo/instances/" + dir + "/instance.yaml":    {Data: instance},
			"projects/demo/instances/" + dir + "/config.yaml":      {Data: config},
			"projects/demo/instances/" + dir + "/connections.yaml": {Data: connections},
			"projects/other/instances/b/instance.yaml":             {Data: []byte("service: app\nversion: 1.0.0\n")},
		}
		repos := []Repository{{Name: "db", FS: tree, Role: RoleTree}}
		got, err := Load(context.Background(), repos, nil)
		again, err2 := Load(context.Background(), repos, nil)
		if errorString(err) != errorString(err2) || len(got) != len(again) {
			t.Fatalf("Load: not the same twice: %v / %v", err, err2)
		}
		l, lerr := LoadProjects(context.Background(), repos, nil)
		if lerr2 := func() error { _, e := LoadProjects(context.Background(), repos, nil); return e }(); errorString(lerr) != errorString(lerr2) {
			t.Fatalf("LoadProjects: not the same twice: %v / %v", lerr, lerr2)
		}
		if err != nil {
			if e, ok := errors.AsType[*LoadError](err); !ok || len(e.Problems) == 0 {
				t.Fatalf("Load: %v is no *LoadError with a problem", err)
			}
			if lerr == nil && len(l.Refused) == 0 {
				t.Fatalf("Load refuses the tree (%v), LoadProjects loads it all", err)
			}
		} else if lerr != nil || len(l.Refused) > 0 || len(l.Instances) != len(got) {
			t.Fatalf("Load accepts the tree, LoadProjects: %v, %d refused, %d instances of %d", lerr, len(l.Refused), len(l.Instances), len(got))
		}
		for _, inst := range got {
			in := inst.Inputs
			if inst.Definition == nil || in.Namespace == "" || inst.Name == "" || in.Release != ReleaseName(in.Namespace, inst.Name) {
				t.Fatalf("an instance %q %q of namespace %q", in.Release, inst.Name, in.Namespace)
			}
		}
		if lerr != nil {
			return
		}
		for _, r := range l.Refused {
			if len(r.Problems) == 0 || len(r.Problems) != len(r.Errs) || r.Inputs.Namespace != r.Namespace {
				t.Fatalf("project %s refused: %q, %v", r.Namespace, r.Problems, r.Errs)
			}
			if slices.ContainsFunc(l.Instances, func(i compiler.Instance) bool { return i.Inputs.Namespace == r.Namespace }) {
				t.Fatalf("project %s refused and loaded", r.Namespace)
			}
		}
	})
}

// FuzzLevels: three files of the levels 0, 1 and 2, whatever they hold:
// the same answer twice, the files unchanged and not shared with the
// layers given the compiler; when accepted, above level 0, of global.okdp
// provisioning alone and no template code. Seeds: the cases of TestLevels.
func FuzzLevels(f *testing.F) {
	f.Add([]byte(levelsPlatform), []byte(""), []byte(""))
	f.Add([]byte(levelsPlatform), []byte("global: {storageClass: fast, project: {name: demo}}\nconnections: {team-db: {contract: database, host: a}}\n"), []byte("connections: {mine: {contract: database, host: b}}\n"))
	f.Add([]byte(levelsPlatform), []byte("global: {okdp: {cluster: {domain: other.example.org}}}\n"), []byte(""))
	f.Add([]byte(levelsPlatform), []byte("global: {okdp: {charts: {objects: {repository: 'oci://x/y', chart: c, version: 1.0.0}}}}\n"), []byte(""))
	f.Add([]byte(levelsPlatform), []byte("global: {okdp: {provisioning: {s3: {classes: {fast: {provider: data/minio}}}}}}\n"), []byte("global: {okdp: {proxy: {noProxy: .svc}}}\n"))
	f.Add([]byte(levelsPlatform), []byte("claims: {reports: {contract: database}}\naccess: {analysts: viewer}\n"), []byte("claims: {reports: {provider: data/pg}}\n"))
	f.Add([]byte(levelsPlatform), []byte("connections: {t: {host: '{{ .Release.Name }}'}}\n"), []byte(""))
	f.Add([]byte("global: {okdp: {secrets: {store: s}}}\n"), []byte("connections: {t: {secretRef: {remoteKey: platform/x}}}\n"), []byte("connections: {u: {secretRef: {remoteKey: demo/x}}}\n"))
	f.Add([]byte(""), []byte("global: {okdp: null}\n"), []byte("global: {okdp: 1}\n"))
	f.Fuzz(func(t *testing.T, b0, b1, b2 []byte) {
		var given []layer
		var before []M
		for level, b := range [][]byte{b0, b1, b2} {
			v, err := chart.ParseValues(b)
			if err != nil {
				return
			}
			given = append(given, layer{level: uint(level), file: string(rune('a' + level)), values: v})
			before = append(before, value.AsMap(value.Copy(v)))
		}
		add := func() (levels, error) {
			ls := levels{namespace: "demo"}
			var errs []error
			for _, l := range given {
				var err error
				ls, err = ls.add(l)
				errs = append(errs, err)
			}
			return ls, errors.Join(errs...)
		}
		ls, err := add()
		ls2, err2 := add()
		if errorString(err) != errorString(err2) || !reflect.DeepEqual(ls.layers, ls2.layers) {
			t.Fatalf("%q: not the same twice: %v / %v", [][]byte{b0, b1, b2}, err, err2)
		}
		for _, l := range ls.layers {
			l["touched"] = true
		}
		for i, l := range given {
			if !reflect.DeepEqual(l.values, before[i]) {
				t.Fatalf("%q: the files changed, or are shared with the layers", [][]byte{b0, b1, b2})
			}
		}
		if err != nil {
			return
		}
		for _, l := range given[1:] {
			if okdp, set := value.AsMap(l.values["global"])["okdp"]; set {
				m, isMap := okdp.(M)
				if !isMap {
					t.Fatalf("%q: accepted global.okdp %#v at level %d", [][]byte{b0, b1, b2}, okdp, l.level)
				}
				for k := range m {
					if k != "provisioning" {
						t.Fatalf("%q: accepted global.okdp.%s at level %d", [][]byte{b0, b1, b2}, k, l.level)
					}
				}
			}
			if err := chart.NoTemplate(l.values, func(p string) string { return p }); err != nil {
				t.Fatalf("%q: accepted template code at level %d: %v", [][]byte{b0, b1, b2}, l.level, err)
			}
			if _, set := l.values["claims"]; set && l.level == 0 {
				t.Fatalf("%q: accepted claims at level 0", [][]byte{b0, b1, b2})
			}
		}
	})
}

// FuzzConfig: an instance's values file, whatever it holds: the same
// answer twice, the file unchanged; when accepted, no connections, claims
// or access, no template code, and of global only the keys the parameter
// schema declares, never okdp. Seeds: the cases of TestConfigValues.
func FuzzConfig(f *testing.F) {
	for _, s := range []string{
		"", "replicas: 2", "global: {storageClass: instance}", "global: {okdp: {cluster: {domain: x}}}", "global: {proxy: p}", "global: null",
		"connections: {}", "connections: null", "claims: {reports: {contract: database}}", "access: {a: viewer}", "upstream: {app: {tags: ['{{ .Release.Name }}']}}",
		"upstream: {app: {'{{ x }}': y}}", "global: {storageClass: '{{ x }}'}",
	} {
		f.Add([]byte(s))
	}
	declared := M{"storageClass": M{}, "okdp": M{}}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := chart.ParseValues(b)
		if err != nil {
			return
		}
		before := value.Copy(v)
		err = checkConfig("config.yaml", v, declared)
		if err2 := checkConfig("config.yaml", v, declared); errorString(err) != errorString(err2) {
			t.Fatalf("%q: not the same twice: %v / %v", b, err, err2)
		}
		if !reflect.DeepEqual(before, value.Copy(v)) {
			t.Fatalf("%q: the file changed", b)
		}
		if err != nil {
			return
		}
		for _, k := range []string{"connections", "claims", AccessKey} {
			if _, set := v[k]; set {
				t.Fatalf("%q: accepted %s", b, k)
			}
		}
		if g, set := v["global"]; set {
			m, isMap := g.(M)
			if !isMap {
				t.Fatalf("%q: accepted global %#v", b, g)
			}
			for k := range m {
				if k != "storageClass" {
					t.Fatalf("%q: accepted global.%s", b, k)
				}
			}
		}
		if err := chart.NoTemplate(v, func(p string) string { return p }); err != nil {
			t.Fatalf("%q: accepted template code: %v", b, err)
		}
	})
}

// errorString: err's message, "" for none.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
