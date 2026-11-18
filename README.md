# okdp-gitops

The deployment side of an OKDP platform: the tree of its deployments, in
one or several Git repositories, compiled with
[okdp-compiler](https://github.com/OKDP/okdp-compiler) into what a GitOps
engine deploys, Flux or Argo CD. A commit of the tree deploys: the CI of
the output repository runs `okdp-gitops compile` every 2 minutes and
commits the output, which the engine reconciles.

- okdp-compiler does the compiling: it reads the tree and compiles every
  instance together, each claim bound to its provider, each instance's
  final values merged as Helm merges them, and orders the releases
  (`compiler.Build`). It holds the rule set of an OKDP platform, its
  mandatory set, which every compile applies, its checks among them. It
  reads no upstream chart.
- okdp-gitops adds its policy to that rule set (the upstream and
  ingress policy of `internal/policy`, wired by `deploy.Build`), and
  does everything around the compile. It checks the final values
  against the upstream charts (`internal/chartcheck`: their schemas over
  their defaults, the lists an instance appends to), which the compiler
  does not read, with the chart cache of the checks (`chartcache`). It
  writes what the engines deploy (`deploy`: the targets `helm`,
  `umbrella`, `flux` and `argocd`), holds the edits of the tree and
  their checks (`treeedit`).
  It keeps the projects with a problem as their last output. On the Git
  side, it checks out the parts of the tree (io and split topologies;
  the repository of a new project, on Forgejo, named
  after the topology's projects url), fetches the catalog repositories,
  and commits the targets' files under `compiled/`, on the revision it
  read. It keeps the workflow of the CI, and the Git transactions of the
  control plane: each applies a `treeedit` edit to the tree at the
  latest revisions, runs its check with the compile of the tree before
  and after it, and commits the change to the repositories of its paths.

Three ways to use it:

- **as a binary**, `okdp-gitops`, for the CI of the output and for a
  person;
- **as a binary**, `okdp-compile`, to compile a tree from local
  directories and write it for one deployer, or for a plain
  `helm install`: what a catalog's CI and a person writing a package run;
- **as a Go library** (`github.com/okdp/okdp-gitops`), for the control
  plane ([okdp-control-plane-server](https://github.com/OKDP/okdp-control-plane-server)),
  which commits the `treeedit` edits of the tree through its
  `gitops` package (`Deployments`: projects, instances, connections,
  named claims, the catalog list), reads the tree with `treeedit`'s
  readers, the compile of the tree (each instance's outcome, the claims
  bound to each provider) and the output back; it does not write the
  output.

## The tree

```text
platform.yaml                          the platform: global.okdp, provisioning classes, platform connections
catalogs.yaml                          okdp-gitops': the catalog repositories to fetch (and the console's menu)
projects.yaml                          okdp-gitops', split topology only: the projects
projects/<p>/project.yaml              a project: its globals, classes, connections, named claims
projects/<p>/instances/<i>/
  instance.yaml                        {service, version}: the package; claimers, its consent as a provider
  config.yaml                          its values: parameters and upstream values
  connections.yaml                     rare: its own connections
compiled/                              the output: the files of the Flux and Argo CD targets
argocd/root.yaml                       the output, optional: where Argo CD reads the output from
sources.yaml                           the output: the revisions of the sources and catalogs it is the compile of
```

The tree is okdp-compiler's
([repository layout](https://github.com/OKDP/okdp-compiler/blob/main/docs/01_usage.md#repository-layout)):
the release of an instance is `<project>-<instance>`
(`compiler.ReleaseName`), in the project's namespace. The packages and the
contracts are in the catalog repositories `catalogs.yaml` lists
(`catalog/<service>/<version>/`, `contracts/`). There are no platform
components: the platform's own services are instances of projects, and
the deployment graph (connection references, claims) is the only order.
The sources are in one input repository (io: for a small team, each of
its writers trusted with every project; the platform's part best in a
repository of its own, for the platform admins), or in a platform
repository and one repository per project (split: each team writes its
own); the output, `compiled/` and `argocd/`, is always in a repository
of its own, which the CI alone writes. A topology file says where each
part is ([the tree](docs/specs/01_layout.md),
[topologies](docs/specs/04_topology.md)).

## Command line

```text
okdp-gitops compile (--config FILE | --tree DIR --output DIR)
                    [--catalog NAME=DIR]... [--charts DIR] [--check]
                    [--path-prefix PREFIX] [--timeout DURATION]
okdp-gitops workflow --for output --config FILE
```

`compile` reads every part of the tree and the catalogs, compiles the
tree, rewrites `compiled/`, writes the files whose bytes change and
removes those no instance produces any more, printing each. Every
problem is reported at once: a project with an instance that does not
compile keeps its output as it was, and so does a project that depends
on it, the others' written; a problem of the whole tree (a file that
does not load, a menu of `catalogs.yaml` that names a service of no
catalog) writes nothing. The test tree of the repository, with
its catalog, its `argocd/` moved to a directory of the output:

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

- `--config FILE`: a topology file: the platform, the projects and the
  output, each a Git repository (cloned, the changes committed and pushed
  as `okdp: compile`, with the credentials of the directory
  `$GITOPS_CREDENTIALS_DIR` when it is set, else the bearer token
  `$GITOPS_TOKEN`, sent over `http://` to the hosts of
  `$GITOPS_INSECURE_HTTP_HOSTS` only, a catalog's and the control
  plane's too, `gitops.Credentials`) or a local directory (written in
  place); the output's repository never one of the sources'.
- `--tree DIR --output DIR`: the io topology of two local directories,
  without a file: the platform and the projects in the first, read; the
  output in the other, written in place. They replace `--root DIR`, the
  tree and the output in one directory, which is gone: a script that
  ran `--root .` runs `--tree . --output ../okdp-output`, the output
  moved to a directory, and a repository, of its own.
- `--catalog NAME=DIR`: the catalog `NAME` of `catalogs.yaml` read from a
  local checkout instead of its URL, the only way to a local catalog:
  `catalogs.yaml` names Git repositories.
- `--charts DIR`: a chart cache: the upstream charts pulled there, kept
  whole and vendored in the output (`charts/*.tgz`), and the
  values checked against them, as the control plane checks them.
- `--check`: write nothing, and fail when a compiled file is not up to
  date.
- `--path-prefix PREFIX`: the path of the output in its Git repository,
  for the paths the engines read.
- `--timeout DURATION`: the bound of the whole run, the fetches, the
  pushes, the catalogs and the charts (default `10m`).

Of each repository, a compile reads the paths of the layout only, at the
last commit of its branch, each file of 4 MiB at most, the files together
64 MiB at most; of each catalog, its ref only. The size of a file is read
from its object header before the file is: a file over the bound, or
beside the layout, is never read into memory, and the clone lives on disk
in a directory of the run's own, its packs compressed.

Exit status: 0 up to date (or written), 1 problems (a project's output
kept, or nothing written) or stale files, 2 usage. A repository's
changes are committed on the revision the compile read, never replayed
on a newer one: when a repository moved meanwhile, the tree is read and
compiled again, up to 5 times ([usage](docs/01_usage.md#compile)).

`okdp-compile` compiles the same tree from local directories, writes
nothing but the files of one `--target` under `-o`, and touches no Git
repository:

```text
okdp-compile --repository <dir>... [--catalog <dir>]... -o <dir>
             [--target helm|umbrella|flux|argocd]
okdp-compile --check [--charts <dir>] --repository <dir>... [--catalog <dir>]...
```

| `--target` | Each instance, in `projects/<namespace>/instances/<release>/` | Deployed as |
|---|---|---|
| `helm` (the default) | `values.yaml`, `okdp-objects.values.yaml`, `okdp.info.yaml`, no chart | two `helm install`: the `okdp-objects` chart, then the catalog's chart as it is |
| `umbrella` | `Chart.yaml`, `instance.values.yaml`, `okdp.info.yaml` | one release of the package's charts and `okdp-objects` |
| `flux` | the umbrella, with its HelmRelease and Kustomization | one release per instance |
| `argocd` | the umbrella, with its Application | one release per instance |

([okdp-compile](docs/01_usage.md#okdp-compile))

## The CI of the output

The output repository alone holds a workflow,
`.forgejo/workflows/okdp-gitops.yaml` (Forgejo Actions), that runs
`okdp-gitops compile` every 2 minutes (`schedule:`), and on demand
(`workflow_dispatch:`), in a job of its own, one run waiting for the one
before it (`concurrency:`, nothing cancelled): the topology is in the
workflow, the token in the output repository's secret. The repositories
of the sources, the platform's and the projects', hold no workflow and
no token: whoever writes one of them could otherwise read the token
from a workflow of their own, and push an output the engines deploy
with every right.

```bash
okdp-gitops workflow --for output --config topology.yaml    # the workflow of the output repository
```

`--for platform` and `--for project` are refused: a repository of the
sources needs nothing but read access for the token's user. A commit of
the sources is compiled by the next run, within 2 minutes; a run whose
sources did not move since stops early, the output's `sources.yaml`
recording the revisions it is the compile of. It runs on a runner
dedicated to okdp-gitops, registered for the output repository only:

| | |
|---|---|
| label `okdp-gitops` | the runner's docker executor, the label mapped to the okdp-gitops image (`okdp-gitops:docker://<image>`, this repository's `Dockerfile`): each job runs in a new container of it, with nothing of the runner's |
| the volume `okdp-charts` | a Docker volume the jobs may mount (`container.valid_volumes`), writable by the user 65534: the chart cache of the checks, `/var/cache/okdp/charts` in the job |
| the CA bundle's directory | optional (`GITOPS_RUNNER_CA_FILE` of `okdp-gitops workflow`): a directory of the runner's Docker daemon that the jobs may mount read-only, a forge served over https with a CA of its own; the job trusts it (`SSL_CERT_FILE`) |
| the secret `OKDP_GITOPS_TOKEN` | of the output repository only: a Forgejo token of scope `write:repository`, its user, a bot, the only one with write access to the output repository, read access to the sources; the job's `$GITOPS_TOKEN` |

A run that fails on an instance (one that does not compile, a claim no
provider binds, a value an upstream chart refuses, a chart it renders
to objects the project may not deploy, values over a ConfigMap) keeps the output of
its project, and of the projects that depend on it, as it was, and
writes the others', but an instance's output that today's platform
policy refuses on its own, which is withdrawn; one that fails on the
whole tree writes nothing: the engines keep deploying the last output of
what is not written
([the CI of the output](docs/01_usage.md#the-ci-of-the-output)). The
run's log names, for each kept project, the revision of the sources its
output is the compile of (`sources.yaml`) and the count of its files
re-derived all the same.

## Go packages

| Package | |
|---|---|
| `gitops` | the stores (`GitStore`, `MemoryStore`, a topology's `Repositories`), each a `treeedit.Reader`, its reads snapshots that run at once, no write waiting on them nor they on a write, its transactions a `treeedit.Writer`; the compile of the tree a store holds (`Compiler`); the commits of the `treeedit` edits (`Deployments.Apply`, and a method per edit), the reads of the tree through `treeedit`; the repository of a new project (`ForgejoClient`); the output written to Git (`WriteCompiled`), the workflow of the output (`OutputWorkflow`) |
| `catalog` | the catalog repositories of `catalogs.yaml` (`treeedit.CatalogRepository`), fetched from Git (`Loader`), indexed by okdp-compiler (`Index`) |
| `deploy` | `Build`, `compiler.Build` with the compiler's mandatory rule set, Helm's merge and the policy, the final values checked against the upstream charts (`BuildOptions.Charts`: `NewCharts` of a `ChartSource`, a `chartcache.Cache` or `StaticCharts`); the targets (`Helm`, `Umbrella`, `Flux`, `ArgoCD`), `TreeFiles`, the files of a tree, and what reads them back (`ReadCompiled`, `ReadPublished`, `ReadArgoRoot`) |
| `rules` | `Standard`, the compiler's mandatory rule set ([the policy](docs/specs/05_rules.md)) |
| `treeedit` | the tree read (`ReadSources`, its readers) and its edits, each with its check (`Apply`, `Check.Verify`): an instance, a connection, a named claim, a project, `catalogs.yaml`, `projects.yaml` |
| `chartcache` | the chart cache of the checks, a `deploy.ChartSource`: the upstream charts pulled from OCI registries and https repositories |

([Go library](docs/01_usage.md#go-library))

## Repository layout

| Path | Description |
|---|---|
| `cmd/okdp-gitops/` | the binary: the flags, the repositories read, the compile, the writes and their commits |
| `cmd/okdp-compile/` | the binary that compiles local directories for one target |
| `deploy/` | the compile with the platform's rules, and what writes it |
| `rules/` | `Standard`, the compiler's mandatory rule set |
| `treeedit/`, `chartcache/` | the edits of the tree and their checks; the chart cache |
| `gitops/` | the stores and the topologies, the compile of the tree, the Git transactions of the control plane's edits, the output, the workflow |
| `catalog/` | the catalog repositories |
| `internal/target/`, `internal/output/` | the targets (`helm`, `flux`, `argocd`), the files of a tree, the projects kept, what is withdrawn of them |
| `internal/render/` | a chart rendered with its final values (Helm SDK), for the policy on what it deploys |
| `internal/helmvalues/`, `internal/policy/` | the compiler's merge with the checks against the charts and the platform's policy: the upstream values and the ingress hosts |
| `internal/chartcheck/`, `internal/chartdir/` | the final values checked against the upstream charts, which the compiler does not read; the layout of a chart cache |
| `internal/outdir/`, `internal/dirfs/` | the output directory of `okdp-compile`; the directories it reads, bounded |
| `internal/buildinfo/` | the program's name and version |
| `examples/` | six trees and what `okdp-compile` writes of them, byte for byte: `input/db/` is the real tree, whose `projects/<namespace>/instances/<instance>/` gives each instance its namespace and release |
| `testdata/` | a tree (`tree/`): a provider of databases and the instances that claim them, and its catalog (`catalog/`); the fixtures of the CLI (`cli/`), of Helm (`helm/`) and of the targets (`targets/`) |
| `Dockerfile`, `.goreleaser.yaml`, `.github/workflows/` | the image of `okdp-gitops`; the release archives of `okdp-compile`; CI (`ci.yml`), the releases (`release-please.yml`), the image's build (`build-image.yml`), the Helm fixtures (`helm-fixtures.yml`) |
| `docs/` | the documentation |

## Documentation

[docs/README.md](docs/README.md) indexes the documentation: usage, setting
up a deployments repository, the tree, the compile, the output,
topologies, the policy, architecture. The tree, the packages and how the
instances compile are okdp-compiler's ([its
documentation](https://github.com/OKDP/okdp-compiler/blob/main/docs/README.md)).

## Build

okdp-gitops builds with Go 1.27 or later. `go.mod` requires a release of
okdp-compiler ([its
releases](https://github.com/OKDP/okdp-compiler/releases)), v0.8.1, and
through it one of cel2yaml, downloaded as any other module.

```bash
make build            # bin/okdp-gitops
make build-compile    # bin/okdp-compile
make check            # what CI runs: gofmt, vet, lint, tidy, build, race tests
make help             # every target
docker build -t okdp-gitops .
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
