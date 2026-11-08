package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/okdp/okdp-gitops/gitops"
)

// workflow prints the workflow of the output repository
// (gitops.OutputWorkflow), for its .forgejo/workflows/: the topology file
// --config names, in it. --for names the repository: output, the only one
// with a workflow; a repository of the sources (platform, project) is
// refused, as it holds no workflow and no token.
func workflow(args []string) int {
	flags := flag.NewFlagSet("workflow", flag.ContinueOnError)
	configFile := flags.String("config", "", "the topology file of the compile, which the workflow holds (required)")
	repository := flags.String("for", "", "the repository of the workflow: output (required); the sources, platform and project, hold none")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "okdp-gitops: unexpected argument %q\n%s\n", flags.Arg(0), usage)
		return 2
	}
	switch *repository {
	case "output":
	case "platform", "project":
		fmt.Fprintf(os.Stderr, "okdp-gitops: workflow --for %s: a repository of the sources holds no workflow and no token: the output repository compiles the sources on a schedule (workflow --for output), with the only token, its secret %s\n", *repository, gitops.TokenSecret)
		return 2
	case "":
		fmt.Fprintf(os.Stderr, "okdp-gitops: workflow: --for is required: output, the repository that compiles the sources\n%s\n", usage)
		return 2
	default:
		fmt.Fprintf(os.Stderr, "okdp-gitops: workflow --for %q: output, the only repository with a workflow\n%s\n", *repository, usage)
		return 2
	}
	if *configFile == "" {
		fmt.Fprintf(os.Stderr, "okdp-gitops: workflow: --config is required: the topology the job compiles\n%s\n", usage)
		return 2
	}
	topology, err := os.ReadFile(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
		return 1
	}
	private, err := gitops.ChartHosts(os.Getenv(gitops.PrivateChartHostsEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %v\n", gitops.PrivateChartHostsEnv, err)
		return 1
	}
	insecureCharts, err := gitops.ChartHosts(os.Getenv(gitops.InsecureChartHostsEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %v\n", gitops.InsecureChartHostsEnv, err)
		return 1
	}
	out, err := gitops.OutputWorkflowWith(topology, gitops.WorkflowOptions{
		InsecureHTTPHosts:  gitops.InsecureHTTPHosts(os.Getenv(gitops.InsecureHTTPHostsEnv)),
		PrivateChartHosts:  private,
		InsecureChartHosts: insecureCharts,
		CAFile:             os.Getenv(gitops.RunnerCAFileEnv),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %s: %v\n", *configFile, err)
		return 1
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "okdp-gitops: %v\n", err)
		return 1
	}
	return 0
}
