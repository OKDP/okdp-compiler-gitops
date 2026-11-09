package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/okdp/okdp-gitops/gitops"
)

// The test tree and its catalog (testdata/).
const (
	treeFixture    = "../../testdata/tree"
	catalogFixture = "../../testdata/catalog"
)

// readTree reads the regular files under root, by slash path.
func readTree(root string) (map[string]string, error) {
	files := map[string]string{}
	err := readPaths(gitops.NewDirReader(root), []string{"."}, files, map[string]bool{})
	return files, err
}

// copyIO copies the regular files of the test tree src into a directory
// of the sources, tree, and one of the output, out: argocd/ and compiled/.
func copyIO(t *testing.T, src, tree, out string) {
	t.Helper()
	files, err := readTree(src)
	require.NoError(t, err)
	for p, content := range files {
		dst := tree
		if strings.HasPrefix(p, "argocd/") || strings.HasPrefix(p, "compiled/") {
			dst = out
		}
		write(t, filepath.Join(dst, filepath.FromSlash(p)), content)
	}
}

func write(t *testing.T, file, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
}

// The test tree compiled into a local directory of the output: the
// targets' files under compiled/, the engines reading the output under the
// path prefix; a second run changes nothing; --check catches a stale file.
func TestCompileATreeIntoADirectoryOfTheOutput(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	args := []string{"compile", "--tree", root, "--output", out, "--path-prefix", "gitops", "--catalog", "test-charts=" + catalogFixture}

	require.Equal(t, 1, run(append(args, "--check")), "nothing compiled yet")
	require.Equal(t, 0, run(args))
	sources, err := readTree(root)
	require.NoError(t, err)
	for p := range sources {
		require.False(t, strings.HasPrefix(p, "compiled/"), "the tree is not written: %s", p)
	}
	got, err := readTree(out)
	require.NoError(t, err)
	app := "compiled/projects/demo/instances/demo-app/"
	for _, f := range []string{"Chart.yaml", "instance.values.yaml", "helmrelease.yaml", "kustomization.yaml", "application.yaml"} {
		require.Contains(t, got, app+f)
	}
	// demo-app's database is claimed of data-postgresql: its release waits
	// for it (dependsOn), after it (sync wave).
	require.Contains(t, got[app+"helmrelease.yaml"], "      chart: \"./gitops/compiled/projects/demo/instances/demo-app\"\n")
	require.Contains(t, got[app+"helmrelease.yaml"], "  dependsOn:\n    - name: \"data-postgresql\"\n")
	require.Contains(t, got[app+"application.yaml"], "    argocd.argoproj.io/sync-wave: \"1\"\n")
	require.Contains(t, got["compiled/projects/data/instances/data-postgresql/application.yaml"], "    argocd.argoproj.io/sync-wave: \"0\"\n")
	require.Contains(t, got["compiled/projects/demo/kustomization.yaml"], "  - instances/demo-app\n")
	require.Contains(t, got, "compiled/projects/data/instances/data-postgresql/okdp.info.yaml")
	require.Equal(t, 0, run(append(args, "--check")))

	config := filepath.Join(root, "projects/demo/instances/legacy/config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("database: {}\n"), 0o644))
	require.Equal(t, 1, run(append(args, "--check")))

	// An instance that does not compile: its project keeps its output, the
	// others' is written; the run fails, naming it.
	require.NoError(t, os.WriteFile(config, []byte("database: nowhere\n"), 0o644))
	write(t, filepath.Join(root, "projects/solo/project.yaml"), "connections:\n  ext-db: {contract: database-server, dbName: ext, engine: postgresql, host: db.example.org, secretRef: {name: ext-db-credentials}}\n")
	write(t, filepath.Join(root, "projects/solo/instances/web/instance.yaml"), "service: app\nversion: 1.0.0\n")
	write(t, filepath.Join(root, "projects/solo/instances/web/config.yaml"), "database: ext-db\n")
	before, err := readTree(out)
	require.NoError(t, err)
	var code int
	msg := stderrOf(t, func() { code = run(args) })
	require.Equal(t, 1, code)
	require.Equal(t, "okdp-gitops: 1 problem(s), the output of demo kept as it was:\n"+
		"demo/legacy: database: \"nowhere\" is not a connection, nor an output of an instance of the namespace\n"+
		"okdp-gitops: demo: kept, no revision recorded in sources.yaml, 11 file(s) re-derived\n", msg)
	after, err := readTree(out)
	require.NoError(t, err)
	require.Contains(t, after, "compiled/projects/solo/instances/solo-web/helmrelease.yaml")
	require.Contains(t, after["compiled/projects/kustomization.yaml"], "  - data\n  - demo\n  - solo\n")
	for p, content := range before {
		if !strings.HasPrefix(p, "compiled/projects/solo/") && p != "compiled/projects/kustomization.yaml" {
			require.Equal(t, content, after[p], p)
		}
	}
	require.Equal(t, 1, run(append(args, "--check")), "up to date, but a project kept")

	require.Equal(t, 2, run([]string{"render"}))
	require.Equal(t, 2, run([]string{"workflow", "extra"}))
	require.Equal(t, 2, run([]string{"workflow", "--for", "output"}), "no topology")
	require.Equal(t, 2, run([]string{"compile", "--tree", root, "--output", out, "--catalog", "nodir"}))

	// The sources and the output are never one directory.
	msg = stderrOf(t, func() { code = run([]string{"compile", "--root", root}) })
	require.Equal(t, 2, code)
	require.Contains(t, msg, "okdp-gitops: --root: the sources and the output are never one directory: --tree DIR --output DIR, or --config FILE")
	require.Equal(t, 2, run([]string{"compile", "-root=" + root}))
	require.Equal(t, 2, run([]string{"compile"}), "no topology")
	require.Equal(t, 2, run([]string{"compile", "--tree", root}), "no output")
	require.Equal(t, 2, run([]string{"compile", "--tree", root, "--output", out, "--config", "x.yaml"}))
	msg = stderrOf(t, func() { code = run([]string{"compile", "--tree", root, "--output", root + "/."}) })
	require.Equal(t, 1, code)
	require.Contains(t, msg, "--tree and --output are one directory")
	// A directory of the output in the tree's: beside the tree, not in it.
	inside := filepath.Join(root, "out")
	_ = stderrOf(t, func() {
		run([]string{"compile", "--tree", root, "--output", inside, "--catalog", "test-charts=" + catalogFixture})
	})
	require.FileExists(t, filepath.Join(inside, "compiled/projects/kustomization.yaml"))
}

// The catalog of a local directory url is refused, catalogs.yaml being
// the platform repository's content: --catalog reads a local directory;
// without argocd/root.yaml, no Applications.
func TestCompileRefusesALocalCatalogURL(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	require.NoError(t, os.Remove(filepath.Join(out, "argocd/root.yaml")))
	catalogs := filepath.Join(root, "catalogs.yaml")
	raw, err := os.ReadFile(catalogs)
	require.NoError(t, err)
	abs, err := filepath.Abs(catalogFixture)
	require.NoError(t, err)
	write(t, catalogs, strings.Replace(string(raw), "https://git.example.org/okdp/test-charts.git", abs, 1))

	msg := stderrOf(t, func() {
		require.Equal(t, 1, run([]string{"compile", "--tree", root, "--output", out, "--path-prefix", ""}))
	})
	require.Contains(t, msg, `reads the local file system: a catalog of catalogs.yaml is a Git repository, a local checkout the operator's (okdp-gitops compile --catalog test-charts=DIR)`)

	require.Equal(t, 0, run([]string{"compile", "--tree", root, "--output", out, "--path-prefix", "", "--catalog", "test-charts=" + abs}))
	got, err := readTree(out)
	require.NoError(t, err)
	require.Contains(t, got, "compiled/projects/demo/instances/demo-app/helmrelease.yaml")
	require.NotContains(t, got, "compiled/projects/demo/instances/demo-app/application.yaml")
}

// A menu of catalogs.yaml that names a service of no catalog is refused by
// the compile: nothing written, the problem printed; --check too.
func TestCompileRefusesAMenuOfAServiceInNoCatalog(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	copyIO(t, treeFixture, root, out)
	catalogs := filepath.Join(root, "catalogs.yaml")
	raw, err := os.ReadFile(catalogs)
	require.NoError(t, err)
	write(t, catalogs, string(raw)+"  - title: Caches\n    services:\n      - {name: redis}\n")
	before, err := readTree(out)
	require.NoError(t, err)
	args := []string{"compile", "--tree", root, "--output", out, "--catalog", "test-charts=" + catalogFixture}
	for _, a := range [][]string{args, append(args, "--check")} {
		var code int
		msg := stderrOf(t, func() { code = run(a) })
		require.Equal(t, 1, code, a)
		require.Equal(t, "okdp-gitops: 1 problem(s):\n"+
			`catalogs.yaml: categories[2].services[0]: service "redis" is in no catalog (catalog/redis/ of no repository)`+"\n", msg, a)
	}
	after, err := readTree(out)
	require.NoError(t, err)
	require.Equal(t, before, after, "nothing written")
}

// An entry of projects.yaml whose path goes up out of its repository is
// refused, the run failing with the error: nothing outside read, no panic.
func TestCompileRefusesAProjectsEntryOutOfItsRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "outside", "project.yaml"), "description: outside\n")
	write(t, filepath.Join(dir, "platform", "catalogs.yaml"), "repositories: []\n")
	write(t, filepath.Join(dir, "platform", "projects.yaml"), "projects:\n  - name: demo\n    url: "+filepath.Join(dir, "projrepo")+"\n    path: ../outside\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projrepo"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "out"), 0o755))
	config := filepath.Join(dir, "topology.yaml")
	write(t, config, "platform: {url: ./platform}\nprojects: {url: './proj-{project}'}\noutput: {url: ./out}\n")
	msg := stderrOf(t, func() {
		require.Equal(t, 1, run([]string{"compile", "--config", config}))
	})
	require.Contains(t, msg, `project demo: projects.yaml: path "../outside" goes up (..): a path in the project's repository`)
}

// Each part of the tree in its own directory, as a topology file names
// them: the tree is read, the output written, in place.
func TestCompileATopologyOfLocalDirectories(t *testing.T) {
	dir := t.TempDir()
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	for p, content := range tree {
		switch {
		case strings.HasPrefix(p, "projects/"):
			project, rest, _ := strings.Cut(strings.TrimPrefix(p, "projects/"), "/")
			write(t, filepath.Join(dir, "okdp-project-"+project, rest), content)
		case strings.HasPrefix(p, "argocd/"):
			write(t, filepath.Join(dir, "okdp-output", p), content)
		default:
			write(t, filepath.Join(dir, "okdp-platform", p), content)
		}
	}
	write(t, filepath.Join(dir, "okdp-platform", "projects.yaml"), "projects:\n  - name: data\n  - name: demo\n")
	config := filepath.Join(dir, "topology.yaml")
	write(t, config, "platform: {url: ./okdp-platform}\nprojects: {url: './okdp-project-{project}'}\noutput: {url: ./okdp-output}\n")
	sources := func() map[string]string {
		files := map[string]string{}
		for _, repo := range []string{"okdp-platform", "okdp-project-data", "okdp-project-demo"} {
			f, err := readTree(filepath.Join(dir, repo))
			require.NoError(t, err)
			for p, c := range f {
				files[repo+"/"+p] = c
			}
		}
		return files
	}
	before := sources()

	args := []string{"compile", "--config", config, "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 1, run(append(args, "--check")))
	require.Equal(t, 0, run(args))
	out, err := readTree(filepath.Join(dir, "okdp-output"))
	require.NoError(t, err)
	require.Contains(t, out, "compiled/projects/demo/instances/demo-reports/instance.values.yaml")
	require.Contains(t, out, "compiled/projects/demo/instances/demo-reports/application.yaml")
	require.Contains(t, out["compiled/projects/demo/instances/demo-reports/helmrelease.yaml"], "      chart: \"./compiled/projects/demo/instances/demo-reports\"\n")
	require.Equal(t, before, sources(), "the tree is not written")
	require.Equal(t, 0, run(append(args, "--check")))
}

// io, the platform apart: platform.yaml and catalogs.yaml in a directory
// of their own (the platform admins'), the projects in another (the
// team's), the output in a third; the compile reads both, writes the
// output only.
func TestCompileAnIOTopologyThePlatformApart(t *testing.T) {
	dir := t.TempDir()
	tree, err := readTree(treeFixture)
	require.NoError(t, err)
	for p, content := range tree {
		switch {
		case strings.HasPrefix(p, "projects/"):
			write(t, filepath.Join(dir, "okdp-projects", p), content)
		case strings.HasPrefix(p, "argocd/"):
			write(t, filepath.Join(dir, "okdp-output", p), content)
		default:
			write(t, filepath.Join(dir, "okdp-platform", p), content)
		}
	}
	config := filepath.Join(dir, "topology.yaml")
	write(t, config, "platform: {url: ./okdp-platform}\nprojects: {url: ./okdp-projects, path: 'projects/{project}'}\noutput: {url: ./okdp-output}\n")
	sources := func() map[string]string {
		files := map[string]string{}
		for _, repo := range []string{"okdp-platform", "okdp-projects"} {
			f, err := readTree(filepath.Join(dir, repo))
			require.NoError(t, err)
			for p, c := range f {
				files[repo+"/"+p] = c
			}
		}
		return files
	}
	before := sources()
	args := []string{"compile", "--config", config, "--catalog", "test-charts=" + catalogFixture}
	require.Equal(t, 0, run(args))
	out, err := readTree(filepath.Join(dir, "okdp-output"))
	require.NoError(t, err)
	require.Contains(t, out, "compiled/projects/demo/instances/demo-app/helmrelease.yaml")
	require.Contains(t, out, "compiled/projects/data/instances/data-postgresql/application.yaml")
	require.Equal(t, before, sources(), "the sources are not written")
	require.Equal(t, 0, run(append(args, "--check")))
}

// okdp-gitops workflow --for output: the workflow of the output, the
// topology of --config in it; a topology the job cannot use is refused; a
// repository of the sources gets none.
func TestWorkflowHoldsTheTopology(t *testing.T) {
	dir := t.TempDir()
	topology := filepath.Join(dir, "topology.yaml")
	write(t, topology, "platform: {url: https://git.example.org/okdp/okdp-sources.git}\n"+
		"projects: {url: https://git.example.org/okdp/okdp-sources.git, path: 'projects/{project}'}\n"+
		"output: {url: https://git.example.org/okdp/okdp-output.git}\n")
	stdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	code := run([]string{"workflow", "--for", "output", "--config", topology})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	raw, err := os.ReadFile(topology)
	require.NoError(t, err)
	want, err := gitops.OutputWorkflow(raw, nil)
	require.NoError(t, err)
	require.Equal(t, string(want), string(out))

	// The sources hold no workflow, and no token.
	for _, repository := range []string{"platform", "project"} {
		var code int
		msg := stderrOf(t, func() { code = run([]string{"workflow", "--for", repository, "--config", topology}) })
		require.Equal(t, 2, code)
		require.Contains(t, msg, "okdp-gitops: workflow --for "+repository+": a repository of the sources holds no workflow and no token")
	}
	var code2 int
	msg := stderrOf(t, func() { code2 = run([]string{"workflow", "--config", topology}) })
	require.Equal(t, 2, code2)
	require.Contains(t, msg, "--for is required")
	require.Equal(t, 2, run([]string{"workflow", "--for", "sources", "--config", topology}))
	require.Equal(t, 2, run([]string{"workflow", "--for", "output", "--config", topology, "--branch", "main"}), "no --branch: the workflow runs on a schedule")

	write(t, topology, "platform: {url: ./platform}\nprojects: {url: './p-{project}'}\noutput: {url: ./out}\n")
	msg = stderrOf(t, func() { code2 = run([]string{"workflow", "--for", "output", "--config", topology}) })
	require.Equal(t, 1, code2)
	require.Contains(t, msg, "the job reaches its repositories over HTTP(S)")

	// Over http://, the hosts of its GITOPS_INSECURE_HTTP_HOSTS only, which
	// the job's environment carries.
	base := "http://forgejo-http.forgejo.svc.cluster.local:3000/okdp/"
	write(t, topology, "platform: {url: '"+base+"okdp-sources.git'}\nprojects: {url: '"+base+"okdp-sources.git', path: 'projects/{project}'}\noutput: {url: '"+base+"okdp-output.git'}\n")
	t.Setenv(gitops.InsecureHTTPHostsEnv, "")
	msg = stderrOf(t, func() { code2 = run([]string{"workflow", "--for", "output", "--config", topology}) })
	require.Equal(t, 1, code2)
	require.Contains(t, msg, "would go over http://, in clear, to forgejo-http.forgejo.svc.cluster.local: allow the host with GITOPS_INSECURE_HTTP_HOSTS")
	t.Setenv(gitops.InsecureHTTPHostsEnv, "*.svc.cluster.local")
	r, w, err = os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	code = run([]string{"workflow", "--for", "output", "--config", topology})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	out, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Contains(t, string(out), "\n          GITOPS_INSECURE_HTTP_HOSTS: \"*.svc.cluster.local\"\n")

	// The CA bundle of its GITOPS_RUNNER_CA_FILE: its directory mounted
	// read-only, SSL_CERT_FILE; a file it cannot hold refused.
	t.Setenv(gitops.InsecureHTTPHostsEnv, "")
	write(t, topology, "platform: {url: 'https://git.example.org/okdp/okdp-sources.git'}\nprojects: {url: 'https://git.example.org/okdp/okdp-sources.git', path: 'projects/{project}'}\noutput: {url: 'https://git.example.org/okdp/okdp-output.git'}\n")
	t.Setenv(gitops.RunnerCAFileEnv, "/etc/okdp/ca/ca.crt")
	r, w, err = os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	code = run([]string{"workflow", "--for", "output", "--config", topology})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	out, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Contains(t, string(out), `volumes: ["okdp-charts:/var/cache/okdp/charts", "/etc/okdp/ca:/etc/okdp/ca:ro"]`)
	require.Contains(t, string(out), "\n          SSL_CERT_FILE: \"/etc/okdp/ca/ca.crt\"\n")
	t.Setenv(gitops.RunnerCAFileEnv, "/ca.crt")
	msg = stderrOf(t, func() { code2 = run([]string{"workflow", "--for", "output", "--config", topology}) })
	require.Equal(t, 1, code2)
	require.Contains(t, msg, "GITOPS_RUNNER_CA_FILE")
	t.Setenv(gitops.RunnerCAFileEnv, "")
	write(t, topology, "platform: {url: '"+base+"okdp-sources.git'}\nprojects: {url: '"+base+"okdp-sources.git', path: 'projects/{project}'}\noutput: {url: '"+base+"okdp-output.git'}\n")
	t.Setenv(gitops.InsecureHTTPHostsEnv, "*.svc.cluster.local")

	// The chart hosts of its GITOPS_PRIVATE_CHART_HOSTS, carried as they
	// are; one that is not a host refused.
	t.Setenv(gitops.PrivateChartHostsEnv, "*.svc.cluster.local")
	r, w, err = os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	code = run([]string{"workflow", "--for", "output", "--config", topology})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	out, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Contains(t, string(out), "\n          GITOPS_PRIVATE_CHART_HOSTS: \"*.svc.cluster.local\"\n")
	t.Setenv(gitops.PrivateChartHostsEnv, "https://registry")
	msg = stderrOf(t, func() { code2 = run([]string{"workflow", "--for", "output", "--config", topology}) })
	require.Equal(t, 1, code2)
	require.Contains(t, msg, `GITOPS_PRIVATE_CHART_HOSTS: chart host "https://registry": a host, host:port or *.<domain>`)
	msg = stderrOf(t, func() {
		code2 = run([]string{"compile", "--tree", dir, "--output", t.TempDir(), "--charts", t.TempDir()})
	})
	require.Equal(t, 1, code2)
	require.Contains(t, msg, `GITOPS_PRIVATE_CHART_HOSTS: chart host "https://registry"`)

	// The plain-http chart hosts of GITOPS_INSECURE_CHART_HOSTS, the same way.
	t.Setenv(gitops.PrivateChartHostsEnv, "")
	t.Setenv(gitops.InsecureChartHostsEnv, "okdp-registry:5000")
	r, w, err = os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	code = run([]string{"workflow", "--for", "output", "--config", topology})
	os.Stdout = stdout
	require.NoError(t, w.Close())
	out, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Contains(t, string(out), "\n          GITOPS_INSECURE_CHART_HOSTS: \"okdp-registry:5000\"\n")
	t.Setenv(gitops.InsecureChartHostsEnv, "https://registry")
	msg = stderrOf(t, func() { code2 = run([]string{"workflow", "--for", "output", "--config", topology}) })
	require.Equal(t, 1, code2)
	require.Contains(t, msg, `GITOPS_INSECURE_CHART_HOSTS: chart host "https://registry"`)
	msg = stderrOf(t, func() {
		code2 = run([]string{"compile", "--tree", dir, "--output", t.TempDir(), "--charts", t.TempDir()})
	})
	require.Equal(t, 1, code2)
	require.Contains(t, msg, `GITOPS_INSECURE_CHART_HOSTS: chart host "https://registry"`)
}
