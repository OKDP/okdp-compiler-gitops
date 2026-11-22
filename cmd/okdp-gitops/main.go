// Command okdp-gitops compiles the tree of a platform's deployments, in one
// or several git repositories, into its output, compiled/: the catalogs of
// catalogs.yaml fetched, the parts of the tree read, given to okdp-compiler
// (deploy.Build), the files of its Flux and Argo CD targets written as
// they are (deploy.TreeFiles, gitops.Deployments.WriteCompiled).
//
//	okdp-gitops compile (--config FILE | --tree DIR --output DIR)
//	                    [--catalog NAME=DIR]... [--charts DIR] [--check]
//	                    [--path-prefix PREFIX] [--timeout DURATION]
//	okdp-gitops workflow --for output --config FILE
//
// It rewrites compiled/ and removes the files no instance produces any
// more. It writes only the files whose bytes change, and prints them. A
// project with an instance that does not compile, or that depends on such
// a project, keeps its output as it was, the others' written, and the run
// fails, every problem printed, and for each kept project the revision of
// the sources its output is the compile of (gitops.SourcesFile) and the
// count of its files re-derived all the same; a problem of the whole tree
// (a file that does not load, a menu of catalogs.yaml naming a service
// of no catalog, a release in two projects) writes nothing. With --check
// it writes nothing and fails when a compiled file is not up to date
// (CI).
//
// --config FILE is a topology file (the server's GITOPS_CONFIG): the
// platform, the projects and the output, each a git url (cloned, and the
// changes committed and pushed, with the credentials of the directory
// $GITOPS_CREDENTIALS_DIR when it is set, else the bearer token
// $GITOPS_TOKEN: gitops.CredentialsFromEnv, sent over http:// to the hosts
// of $GITOPS_INSECURE_HTTP_HOSTS only) or a local directory (an absolute
// path, or one relative to the file: read and written in place); the
// output's never one of the sources'. --tree DIR --output DIR is the io
// topology of two local directories: the platform and the projects in the
// first, the output in the other. The packages come from the catalogs of
// catalogs.yaml; --catalog NAME=DIR reads the catalog NAME
// from a local checkout instead of its url, the only way to a local
// catalog (catalogs.yaml names Git repositories); a catalog on the host of the
// platform's repository is read with its credentials, as the control plane
// reads it. --charts DIR is a chart cache (okdp-compiler's chartcache):
// the upstream charts of the compiled instances are pulled there, kept
// whole, their values checked against them (their defaults and schemas),
// as the control plane checks them, the charts a project gives upstream
// values to rendered and their manifests checked, and each chart the
// compile checked vendored in the umbrella (charts/*.tgz): a chart that
// cannot be pulled leaves the instances naming it unchecked, with a
// warning, and one of a project that is not privileged that gives it
// upstream values refused. The
// puller trusts the system's certificates and those of the credentials
// (gitops.NewChartPuller), reaches a chart host at a private address only
// when $GITOPS_PRIVATE_CHART_HOSTS names it, and over plain HTTP only when
// $GITOPS_INSECURE_CHART_HOSTS names it (gitops.ChartHosts): a chart host
// is reached over TLS otherwise, a registry or chart repository http://
// refused.
//
// Of each repository, the compile reads the paths of the layout only
// (gitops.Topology.LayoutPaths: platform.yaml, catalogs.yaml,
// projects.yaml, the projects, compiled/ and argocd/), each file of
// gitops.MaxTreeFileSize at most, gitops.MaxTreeSize together; nothing
// beside them, their size read before they are (a git blob from its object
// header). A git repository is fetched at the last commit of its branch
// only (a depth of 1), into a clone on disk in a directory of the run's
// own, removed when it ends; a catalog at its ref only. --timeout (10m)
// bounds the whole run: the fetches, the pushes, the catalogs, the charts.
//
// The changes of a git repository, the output's, are committed on the
// revision the compile read, never replayed on a newer one: an output that
// moved meanwhile (another run) is read and compiled again, up to 5
// times, so that concurrent runs end on the compile of the latest sources.
// Beside compiled/, the output records the sources it is the compile of,
// gitops.SourcesFile: a run of git repositories whose sources, catalogs,
// okdp-gitops and output did not move since stops after reading it.
//
// workflow prints the workflow of the CI, the output repository's
// (gitops.OutputWorkflow): every 2 minutes, and on demand, a job of the
// runner's okdp-gitops label, in a container of the okdp-gitops image,
// compiles the topology of FILE, which the workflow holds, with the token
// of the output repository's secret OKDP_GITOPS_TOKEN; a topology over
// http:// needs its hosts in $GITOPS_INSECURE_HTTP_HOSTS, which the job's
// environment then carries, as it carries $GITOPS_PRIVATE_CHART_HOSTS and
// $GITOPS_INSECURE_CHART_HOSTS. The repositories of the sources hold no
// workflow and no token: --for platform and --for project are refused.
//
// Exit status: 0 up to date (or written), 1 problems (a project's output
// kept, or nothing written) or stale files, 2 usage.
package main

import (
	"github.com/okdp/okdp-gitops/tree"

	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/okdp/okdp-gitops/chartcache"

	"github.com/okdp/okdp-gitops/gitops"
)

const usage = "usage: okdp-gitops compile (--config FILE | --tree DIR --output DIR) [--catalog NAME=DIR]... [--charts DIR] [--check] [--path-prefix PREFIX] [--timeout DURATION]\n       okdp-gitops workflow --for output --config FILE"

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	// The one log handler of the command: what the libraries log, from
	// Info up, on the standard error.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "workflow" {
		return workflow(args[1:])
	}
	if len(args) == 0 || args[0] != "compile" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	for _, a := range args[1:] {
		if name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "="); strings.HasPrefix(a, "-") && name == "root" {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %s: the sources and the output are never one directory: --tree DIR --output DIR, or --config FILE\n%s\n", a, usage)
			return 2
		}
	}
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	configFile := flags.String("config", "", "a topology file: the platform, the projects and the output (git urls or local directories)")
	treeDir := flags.String("tree", "", "a local directory of the sources: platform.yaml, catalogs.yaml, projects/ (with --output)")
	outputDir := flags.String("output", "", "a local directory of the output: compiled/, argocd/ (with --tree)")
	prefix := flags.String("path-prefix", "", "path of the output in its Git repository, for the engine files (default: the topology's output path; with --output, from git, \"\" at the repository root)")
	check := flags.Bool("check", false, "write nothing; fail when a compiled file is not up to date")
	chartCache := flags.String("charts", "", "a chart cache directory: check the values against the upstream charts, pulled there")
	timeout := flags.Duration("timeout", defaultTimeout, "bound of the whole run: fetches, pushes, catalogs, charts")
	var catalogs list
	flags.Var(&catalogs, "catalog", "NAME=DIR: read the catalog NAME of catalogs.yaml from a local directory (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "okdp-gitops: unexpected argument %q\n%s\n", flags.Arg(0), usage)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintf(os.Stderr, "okdp-gitops: --timeout %s: a duration above 0\n", *timeout)
		return 2
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case set["config"] && (set["tree"] || set["output"]):
		fmt.Fprintf(os.Stderr, "okdp-gitops: --config is exclusive with --tree and --output: a topology names its own directories\n%s\n", usage)
		return 2
	case !set["config"] && (*treeDir == "" || *outputDir == ""):
		fmt.Fprintf(os.Stderr, "okdp-gitops: compile: --config FILE, or --tree DIR and --output DIR: where the sources and the output are\n%s\n", usage)
		return 2
	}
	dirs := map[string]string{}
	for _, c := range catalogs {
		name, dir, ok := strings.Cut(c, "=")
		if !ok || name == "" || dir == "" {
			fmt.Fprintf(os.Stderr, "okdp-gitops: --catalog %q: NAME=DIR\n", c)
			return 2
		}
		dirs[name] = dir
	}

	var topology *gitops.Topology
	if *configFile != "" {
		var err error
		if topology, err = gitops.LoadTopology(*configFile); err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
			return 1
		}
		if !set["path-prefix"] {
			*prefix = topology.Output.Path
		}
	} else {
		var err error
		if topology, err = localTopology(*treeDir, *outputDir); err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
			return 1
		}
		if !set["path-prefix"] {
			*prefix = gitPrefix(*outputDir)
		}
	}

	creds, err := gitops.CredentialsFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
		return 1
	}
	c := compilation{topology: topology, prefix: strings.Trim(*prefix, "/"), catalogs: dirs, creds: creds, check: *check}
	if *chartCache != "" {
		private, err := gitops.ChartHosts(os.Getenv(gitops.PrivateChartHostsEnv))
		if err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %v\n", gitops.PrivateChartHostsEnv, err)
			return 1
		}
		insecure, err := gitops.ChartHosts(os.Getenv(gitops.InsecureChartHostsEnv))
		if err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %v\n", gitops.InsecureChartHostsEnv, err)
			return 1
		}
		puller, err := gitops.NewChartPuller(creds, insecure, private)
		if err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
			return 1
		}
		c.charts = chartcache.NewCache(*chartCache, puller)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		code, err := c.run(ctx)
		if err != nil && ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: timed out after %s (--timeout): %v\n", *timeout, err)
			return 1
		}
		if errors.Is(err, gitops.ErrMoved) && attempt < attempts {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %v: compiling again from the new revision\n", err)
			continue
		}
		if layout, ok := errors.AsType[*tree.LoadError](err); ok {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %d problem(s):\n%s\n", len(layout.Problems), strings.Join(layout.Problems, "\n"))
			return 1
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
			return 1
		}
		return code
	}
}

// defaultTimeout bounds a run of okdp-gitops compile (--timeout).
const defaultTimeout = 10 * time.Minute

// attempts bounds the compiles of one run: a repository that moves while it
// compiles (another commit of the sources, another run) is compiled again
// from its new revision, the compile being of the sources it read.
const attempts = 5
