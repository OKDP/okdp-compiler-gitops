package policy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"
	"github.com/okdp/okdp-gitops/rules"
)

// layer: a values layer read from b as a values file is, or false when
// it is not one.
func layer(b []byte) (M, bool) {
	v, err := chart.ParseValues(b)
	return v, err == nil
}

// errorString: err's message, "" for none.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// FuzzUpstreamCheck: the platform's check of an instance's upstream values,
// whatever they hold: none for a privileged project; the same answer twice;
// and for any other, a key of the denied ones refused wherever it is added:
// beside the values, nested in them, or in a string a chart renders as YAML.
// Seeds: the upstream values of TestUpstreamPolicy's cases and the denied
// fixtures.
func FuzzUpstreamCheck(f *testing.F) {
	for _, s := range []string{
		"", "{}", "image: {repository: reg.example.org/okdp/app, tag: v1}", "image: docker.io/library/nginx", "registry: evil.example.org\nrepository: app",
		"securityContext: {runAsUser: 0}", "podSecurityContext: {runAsNonRoot: false}", "extraContainers: [{name: x}]",
		"initImage: evil.example.org/x", "config: \"hostPath: /x\\n\"", "a: {b: [{c: {privileged: true}}]}",
		"tpl: \"---\\nsidecars: []\\n---\\nimage: x\"", "image: null", "image: {name: reg.example.org/okdp/x}",
	} {
		f.Add([]byte(s))
	}
	okdp := M{"policy": M{"upstream": M{"deny": []any{"extraVolumes"}, "registries": []any{"reg.example.org/okdp"}}}}
	f.Fuzz(func(t *testing.T, b []byte) {
		user, ok := layer(b)
		if !ok {
			return
		}
		at := func(p string) string { return "upstream." + p }
		if UpstreamCheck(context.Background(), rules.Standard(), okdp, "demo", true) != nil {
			t.Fatal("a check for a privileged project")
		}
		check := UpstreamCheck(context.Background(), rules.Standard(), okdp, "demo", false)
		got, again := check(user, nil, at), check(user, nil, at)
		if errorString(got) != errorString(again) {
			t.Fatalf("%q: %v then %v", b, got, again)
		}
		for name, with := range map[string]func(M){
			"beside":    func(m M) { m["hostPath"] = "/x" },
			"deny list": func(m M) { m["extraVolumes"] = []any{} },
			"nested":    func(m M) { m["wrap"] = M{"list": []any{M{"sidecars": []any{}}}} },
			"text":      func(m M) { m["rendered"] = "name: x\nextraContainers: []\n" },
		} {
			m := value.AsMap(value.Copy(user))
			with(m)
			if err := check(m, nil, at); err == nil {
				t.Fatalf("%q: %s: %#v accepted", b, name, m)
			}
		}
	})
}

// TestUpstreamPolicy: the upstream values of an instance of a project that
// is not privileged set no denied key, at any depth, in a list or in a
// string read as YAML, nor runAsUser 0 nor runAsNonRoot false, nor a key of
// the platform's deny list; with the platform's registries, every image
// comes from one of them, a registry and a repository making one, over the
// computed ones. A privileged project's are not checked.
func TestUpstreamPolicy(t *testing.T) {
	registries := M{"registries": []any{"registry.example.org", "mirror.example.org/okdp"}}
	computed := M{"image": M{"registry": "registry.example.org", "repository": "okdp/app"}}
	for name, tc := range map[string]struct {
		policy M // global.okdp.policy.upstream
		user   M // upstream.app
		want   string
	}{
		"free keys":           {nil, M{"nodeSelector": M{"a": "b"}, "tolerations": []any{M{"key": "k"}}, "affinity": M{}, "pod": M{"runAsUser": 1000.0, "runAsNonRoot": true}, "image": "docker.io/x:1"}, ""},
		"a denied key":        {nil, M{"securityContext": M{}}, "app: upstream.app.securityContext: securityContext is denied to the instance's upstream values (a pod's privileges, identity, containers or objects): the platform's projects set it (global.okdp.policy.privileged), demo is not one of them"},
		"in a list":           {nil, M{"volumes": []any{M{"name": "h", "hostPath": M{"path": "/"}}}}, "app: upstream.app.volumes[0].hostPath: hostPath is denied"},
		"runAsUser 0":         {nil, M{"pod": M{"runAsUser": 0.0}}, "app: upstream.app.pod.runAsUser: runAsUser 0, root, is denied to the instance's upstream values: the platform's projects set it (global.okdp.policy.privileged), demo is not one of them"},
		"runAsNonRoot false":  {nil, M{"pod": M{"runAsNonRoot": false}}, "app: upstream.app.pod.runAsNonRoot: runAsNonRoot false is denied"},
		"the platform's deny": {M{"deny": []any{"rbac"}}, M{"rbac": M{"create": true}}, "app: upstream.app.rbac: rbac is denied to the instance's upstream values (global.okdp.policy.upstream.deny)"},
		"YAML text":           {nil, M{"extraVolumes": "- name: h\n  hostPath: {path: /}\n"}, "app: upstream.app.extraVolumes, read as YAML: [0].hostPath: hostPath is denied"},
		"YAML documents":      {nil, M{"extra": "a: b\n---\nkind: X\nprivileged: true\n"}, "app: upstream.app.extra, read as YAML: privileged: privileged is denied"},
		"JSON text":           {nil, M{"config": M{"json": `{"hostNetwork": true}`}}, "app: upstream.app.config.json, read as YAML: hostNetwork: hostNetwork is denied"},
		"plain text":          {nil, M{"url": "https://x.example.org:8443/a", "note": "a: b", "list": "- a: 1"}, ""},
		"an image":            {registries, M{"image": "registry.example.org/app:1", "side": M{"image": "mirror.example.org/okdp/tool@sha256:0"}}, ""},
		"an image of another": {registries, M{"side": M{"image": "docker.io/x"}}, "app: upstream.app.side.image: image docker.io/x is not from a registry of global.okdp.policy.upstream.registries (registry.example.org, mirror.example.org/okdp): the platform's projects pull from any (global.okdp.policy.privileged), demo is not one of them"},
		"past a path":         {registries, M{"side": M{"image": "mirror.example.org/okdp-evil/x"}}, "app: upstream.app.side.image: image mirror.example.org/okdp-evil/x is not"},
		"another port":        {registries, M{"side": M{"image": "registry.example.org:5000/x"}}, "app: upstream.app.side.image: image registry.example.org:5000/x is not"},
		"a registry":          {registries, M{"image": M{"registry": "mirror.example.org", "repository": "okdp/app"}}, ""},
		"the computed one":    {registries, M{"image": M{"repository": "okdp/other"}}, ""},
		"another repository":  {registries, M{"image": M{"registry": "mirror.example.org", "repository": "other/app"}}, "app: upstream.app.image.registry: image mirror.example.org/other/app is not"},
		"another registry":    {registries, M{"image": M{"registry": "docker.io"}}, "app: upstream.app.image.registry: image docker.io/okdp/app is not"},
		"a null registry":     {registries, M{"image": M{"registry": nil}}, "app: upstream.app.image.registry: image okdp/app is not"},
		"an image null":       {registries, M{"image": nil}, "app: upstream.app.image: image null, the chart's default, is not"},
		"an image's name":     {registries, M{"singleuser": M{"image": M{"name": "docker.io/jupyter/x", "tag": "1"}}}, "app: upstream.app.singleuser.image.name: image docker.io/jupyter/x is not"},
		"no image's registry": {registries, M{"registry": M{"replicas": 1.0}}, ""},
		"no registry":         {M{"registries": []any{}}, M{"image": "registry.example.org/x"}, "app: upstream.app.image: image registry.example.org/x is not from a registry of global.okdp.policy.upstream.registries (none)"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := M{"policy": M{"upstream": tc.policy}}
			check := UpstreamCheck(context.Background(), rules.Standard(), settings, "demo", false)
			err := check(tc.user, computed, func(p string) string { return "app: upstream.app." + p })
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
			if UpstreamCheck(context.Background(), rules.Standard(), settings, "demo", true) != nil {
				t.Error("a check for a privileged project")
			}
		})
	}
}

// TestRunAsRootStrings: a chart renders a string as the number it holds:
// runAsUser "0" and runAsNonRoot "false" are denied as 0 and false are.
func TestRunAsRootStrings(t *testing.T) {
	for name, tc := range map[string]struct {
		key  string
		v    any
		deny bool
	}{
		"0":         {"runAsUser", 0.0, true},
		"\"0\"":     {"runAsUser", "0", true},
		"1000":      {"runAsUser", 1000.0, false},
		"\"1000\"":  {"runAsUser", "1000", false},
		"false":     {"runAsNonRoot", false, true},
		"\"false\"": {"runAsNonRoot", "false", true},
		"true":      {"runAsNonRoot", true, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := UpstreamCheck(context.Background(), rules.Standard(), M{}, "demo", false)(M{tc.key: tc.v}, nil, func(p string) string { return p })
			if (err != nil) != tc.deny {
				t.Errorf("got %v, denied %v", err, tc.deny)
			}
		})
	}
}

// TestIngressValues: the hosts of the final values under a key ingress, at
// any depth, are the project's or listed for it.
func TestIngressValues(t *testing.T) {
	const prefix = `app: `
	for name, tc := range map[string]struct {
		user M // upstream.app
		want string
	}{
		"the project's":      {M{"ingress": M{"hostname": "app.demo.example.org", "hosts": []any{M{"host": "app.demo.example.org"}, "b.demo.example.org"}, "tls": []any{M{"hosts": []any{"app.demo.example.org"}}}}}, ""},
		"the old dash form":  {M{"ingress": M{"hostname": "app-demo.example.org"}}, prefix + `ingress.hostname: host "app-demo.example.org" is not served by demo`},
		"a deeper name":      {M{"ingress": M{"hostname": "a.b.demo.example.org"}}, prefix + `ingress.hostname: host "a.b.demo.example.org" is not served by demo`},
		"another project's":  {M{"ingress": M{"hostname": "app.other.example.org"}}, prefix + `ingress.hostname: host "app.other.example.org" is not served by demo`},
		"outside an ingress": {M{"database": M{"host": "db.example.org", "hosts": []any{"x.example.org"}}, "name": "x"}, ""},
		"a host":             {M{"master": M{"ingress": M{"host": "sso.example.org"}}}, prefix + `master.ingress.host: host "sso.example.org" is not served by demo`},
		"a hostname":         {M{"ingress": M{"hostname": "sso.example.org"}}, prefix + `ingress.hostname: host "sso.example.org"`},
		"hosts":              {M{"ingress": M{"web": M{"hosts": []any{"app.demo.example.org", "sso.example.org"}}}}, prefix + `ingress.web.hosts[1]: host "sso.example.org"`},
		"an item's host":     {M{"ingress": M{"hosts": []any{M{"host": "sso.example.org", "paths": []any{"/"}}}}}, prefix + `ingress.hosts[0].host: host "sso.example.org"`},
		"extra hosts":        {M{"ingress": M{"extraHosts": []any{M{"name": "sso.example.org"}}}}, prefix + `ingress.extraHosts[0].name: host "sso.example.org"`},
		"extra tls":          {M{"ingress": M{"extraTls": []any{M{"hosts": []any{"sso.example.org"}}}}}, prefix + `ingress.extraTls[0].hosts[0]: host "sso.example.org"`},
		"ingresses":          {M{"ingresses": M{"http": M{"host": "sso.example.org"}}}, prefix + `ingresses.http.host: host "sso.example.org"`},
		"a name outside":     {M{"ingress": M{"name": "sso.example.org"}}, ""},
		"an annotation":      {M{"ingress": M{"annotations": M{"cert-manager.io/alt-names": "a.demo.example.org sso.example.org"}}}, prefix + `ingress.annotations["cert-manager.io/alt-names"]: host "sso.example.org"`},
		"a boolean":          {M{"ingress": M{"host": true, "hostname": ""}}, ""},
		"listed":             {M{"ingress": M{"host": "sso.example.org"}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			settings := M{"ingress": M{"suffix": "example.org"}}
			if name == "listed" {
				settings["policy"] = M{"ingressHosts": M{"demo": []any{"sso.example.org"}}}
			}
			err := CheckIngressValues(context.Background(), rules.Standard(), "demo", settings, map[string]map[string]any{"app": tc.user})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// TestPlainUpstream: a literal of the upstream values at a sensitive key is
// warned of, by its path, its value never printed.
func TestPlainUpstream(t *testing.T) {
	user := M{"auth": M{"password": "hunter2", "existingSecret": "s", "existingSecretPasswordKey": "password", "username": "u", "enabled": true}, "tokens": []any{"t1", "${TOKEN}"}, "secretKey": 4321.0, "env": []any{M{"name": "API_KEY", "value": "v"}}}
	var got []string
	for _, w := range PlainUpstream(M{"app": user}) {
		got = append(got, strings.SplitN(w, ":", 2)[0])
		if strings.Contains(w, "hunter2") || strings.Contains(w, "4321") {
			t.Errorf("a value printed: %s", w)
		}
	}
	if want := []string{"upstream.app.auth.password", "upstream.app.secretKey", "upstream.app.tokens[0]"}; !slices.Equal(got, want) {
		t.Errorf("warned at %q, want %q", got, want)
	}
}
