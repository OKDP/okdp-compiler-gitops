# okdp-gitops

The deployment side of an OKDP platform: the tree of its deployments, in
one or several Git repositories, compiled with
[okdp-compiler](https://github.com/OKDP/okdp-compiler) into what a GitOps
engine deploys, Flux or Argo CD. A commit of the tree deploys: the CI of
the output repository runs `okdp-gitops compile` every 2 minutes and
commits the output, which the engine reconciles.

okdp-compiler compiles the tree and holds the mandatory rule set of an
OKDP platform. okdp-gitops does everything around the compile: it adds
its policy (upstream values, ingress hosts), checks the final values
against the upstream charts, writes the targets (`helm`, `umbrella`,
`flux`, `argocd`), reads and commits the Git repositories, and holds the
edits of the tree the control plane commits.

Three ways to use it:

- `okdp-gitops`, the binary of the output's CI: it compiles the Git
  repositories of a topology and commits the output;
- `okdp-compile`, the binary that compiles local directories for one
  target and touches no Git repository: for a catalog's CI, a person
  writing a package, or a plain `helm install`;
- the Go library (`github.com/okdp/okdp-gitops`), for
  [okdp-control-plane-server](https://github.com/OKDP/okdp-control-plane-server),
  which edits the tree and reads its compile through it.

## The tree

```text
platform.yaml                          the platform: global.okdp, provisioning classes, platform connections
catalogs.yaml                          the catalog repositories to fetch (and the console's menu)
projects.yaml                          split topology only: the projects
projects/<p>/project.yaml              a project: its globals, classes, connections, named claims
projects/<p>/instances/<i>/
  instance.yaml                        {service, version}: the package; claimers, its consent as a provider
  config.yaml                          its values: parameters and upstream values
  connections.yaml                     rare: its own connections
compiled/                              the output: the files of the Flux and Argo CD targets
argocd/root.yaml                       the output, optional: where Argo CD reads the output from
sources.yaml                           the output: the revisions of the sources and catalogs it is the compile of
```

The release of an instance is `<project>-<instance>`, in the project's
namespace. The packages and the contracts are in the catalog
repositories `catalogs.yaml` lists. The platform's own services are
instances of projects too.

A topology file says where each part is. The sources are in one
repository (io) or in a platform repository and one per project (split);
the output is always in a repository of its own, which the CI alone
writes ([the tree](docs/specs/01_layout.md),
[topologies](docs/specs/04_topology.md)).

## Command line

```text
okdp-gitops compile (--config FILE | --tree DIR --output DIR)
                    [--catalog NAME=DIR]... [--charts DIR] [--check]
                    [--path-prefix PREFIX] [--timeout DURATION]
okdp-gitops workflow --for output --config FILE
```

`compile` reads the tree and the catalogs, compiles the tree and
rewrites `compiled/`. A project with a problem keeps its last output,
and so do the projects that depend on it; a problem of the whole tree
writes nothing. The test tree of the repository:

```console
$ make build
$ testdata=$PWD/testdata okdp=$PWD/bin/okdp-gitops
$ cd "$(mktemp -d)" && cp -R "$testdata/tree" tree && mkdir output && mv tree/argocd output
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog"
compiled output/compiled/projects/data/instances/data-postgresql/Chart.yaml
...
compiled output/compiled/projects/kustomization.yaml
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog" --check
okdp-gitops: up to date
```

| Flag | |
|---|---|
| `--config FILE` | a topology file: the platform, the projects and the output, each a Git repository (cloned, committed and pushed with `$GITOPS_TOKEN`) or a local directory |
| `--tree DIR --output DIR` | two local directories instead: the sources, read; the output, written in place |
| `--catalog NAME=DIR` | the catalog `NAME` read from a local checkout instead of its URL |
| `--charts DIR` | a chart cache: the upstream charts pulled there, vendored in the output, the values checked against them |
| `--check` | write nothing, and fail when a compiled file is not up to date |
| `--path-prefix PREFIX` | the path of the output in its Git repository |
| `--timeout DURATION` | the bound of the whole run (default `10m`) |

Exit status: 0 up to date or written, 1 problems or stale files, 2 usage
([usage](docs/01_usage.md#compile)).

`okdp-compile` compiles the same tree from local directories and writes
the files of one target:

```text
okdp-compile --repository <dir>... [--catalog <dir>]... -o <dir>
             [--target helm|umbrella|flux|argocd]
okdp-compile --check [--charts <dir>] --repository <dir>... [--catalog <dir>]...
```

| `--target` | Deployed as |
|---|---|
| `helm` (the default) | two `helm install`: the `okdp-objects` chart, then the catalog's chart as it is |
| `umbrella` | one release of the package's charts and `okdp-objects` |
| `flux` | the umbrella, with its HelmRelease and Kustomization |
| `argocd` | the umbrella, with its Application |

([okdp-compile](docs/01_usage.md#okdp-compile))

## The CI of the output

The output repository alone holds a workflow (Forgejo Actions), which
runs `okdp-gitops compile` every 2 minutes and on demand. The
repositories of the sources hold no workflow and no token: whoever
writes one of them could otherwise read the token and push an output
the engines deploy.

```bash
okdp-gitops workflow --for output --config topology.yaml
```

It runs on a runner dedicated to okdp-gitops, with the okdp-gitops image
(this repository's `Dockerfile`), a volume for the chart cache, and the
secret `OKDP_GITOPS_TOKEN`: the token of a bot with write access to the
output repository and read access to the sources
([the CI of the output](docs/01_usage.md#the-ci-of-the-output)).

## Go packages

| Package | |
|---|---|
| `gitops` | the stores and the topologies, the compile of the tree, the commits of the control plane's edits, the output written to Git, the workflow |
| `catalog` | the catalog repositories, fetched from Git |
| `deploy` | `Build`, the compile with the rules, the policy and the checks against the charts; the targets |
| `rules` | `Standard`, the compiler's mandatory rule set |
| `treeedit` | the tree read, and its edits, each with its check |
| `chartcache` | the chart cache: the upstream charts pulled from OCI registries and https repositories |

`examples/` holds six trees and what `okdp-compile` writes of them;
`testdata/`, a tree, its catalog and the fixtures
([Go library](docs/01_usage.md#go-library)).

## Documentation

[docs/README.md](docs/README.md) indexes the documentation: usage, setting
up a deployments repository, the tree, the compile, the output,
topologies, the policy, architecture. The tree, the packages and how the
instances compile are okdp-compiler's ([its
documentation](https://github.com/OKDP/okdp-compiler/blob/main/docs/README.md)).

## Build

Go 1.27 or later.

```bash
make build            # bin/okdp-gitops
make build-compile    # bin/okdp-compile
make check            # what CI runs: gofmt, vet, lint, tidy, build, race tests
make help             # every target
docker build -t okdp-gitops .
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
