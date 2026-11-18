# Usage

How to run okdp-gitops: the `okdp-gitops` binary, for a person and for
the CI of the output, the `okdp-compile` binary, for local directories,
the engines that deploy what they write, and the Go library, for the
control plane. What it reads is specified in
[the tree](specs/01_layout.md), how it compiles in
[the compile](specs/02_compile.md), what it writes in
[the output](specs/03_output.md), where the parts live in
[topologies](specs/04_topology.md); how it works inside is in
[architecture](00_architecture.md). How one instance compiles, its inputs
and its outputs, is okdp-compiler's:
[its documentation](https://github.com/OKDP/okdp-compiler/blob/main/docs/README.md).

The commands below run from the repository root, after `make build` and
`make build-compile`.

## Install

### From a checkout

okdp-gitops builds with Go 1.27 or later. `go.mod` requires a release of
okdp-compiler, v0.8.1, one of
[its releases](https://github.com/OKDP/okdp-compiler/releases), and
through it one of cel2yaml, both downloaded as any other module.

```bash
make build            # bin/okdp-gitops
make build-compile    # bin/okdp-compile
make check            # what CI runs: gofmt, vet, lint, tidy, build, race tests
make example          # rewrite the outputs of examples/ from the current compiler
make fixtures         # regenerate testdata/helm (helm, python3, UMBRELLA_HELMS)
make                  # every target
```

`make build` stamps the version into `internal/buildinfo`: `git describe
--tags --always --dirty` without the leading `v`, `VERSION=1.2.3`
overriding it; without it, the version Go records for the main module,
else `dev`.

A newer okdp-compiler is taken with `go get`, the change of `go.mod` and
`go.sum` committed, and a change of both modules builds against an
okdp-compiler checkout through a Go workspace, which `.gitignore` keeps
out of the commits:

```bash
go get github.com/okdp/okdp-compiler@vX.Y.Z && go mod tidy
go work init . ../okdp-compiler    # build against ../okdp-compiler; rm go.work* once it is released
```

### The image

`Dockerfile` builds the binary for the target platform, without
emulation, onto Alpine: `/usr/local/bin/okdp-gitops`, the entrypoint, run
as the user 65534, with `HOME=/tmp` and the CA certificates:

```bash
docker build --build-arg VERSION=1.2.3 -t okdp-gitops .
```

okdp-compiler and cel2yaml are downloaded at the releases `go.mod`
requires. The image holds `okdp-gitops` only:
`okdp-compile` is built from a checkout, or from the archives of
`.goreleaser.yaml` once a release publishes them.

Its base images are pinned by digest, the Go toolchain of `go.mod` and
a supported Alpine branch (`GO_IMAGE`, `ALPINE_IMAGE`, a tag and its
digest, bumped together by hand).

`.github/workflows/build-image.yml`, run by hand or called by another
workflow with a version, publishes it for `linux/amd64` and `linux/arm64`
as `quay.io/okdp/images/okdp-gitops:<version>`: a SemVer version,
`MAJOR.MINOR.PATCH[-PRERELEASE]`, its leading `v` dropped; another input
fails the job, no tag pushed. It is the image of the
jobs of the CI of the output: their runner maps its label `okdp-gitops`
to it ([the CI of the output](#the-ci-of-the-output)), and their
script runs with its shell, `sh`, as the user 65534.

The image holds no `git` binary: run with `--output` on a directory that
is not at the root of its repository, it needs `--path-prefix`
([`--path-prefix`](#--path-prefix)).

## Command line

```text
okdp-gitops compile (--config FILE | --tree DIR --output DIR)
                    [--catalog NAME=DIR]... [--charts DIR] [--check]
                    [--path-prefix PREFIX] [--timeout DURATION]
okdp-gitops workflow --for output --config FILE
```

`okdp-compile`, for local directories and one target, has a section of
its own ([okdp-compile](#okdp-compile)).

Flags take one or two dashes, and a value as the next argument or after
`=`; an argument that is not a flag is refused.

### compile

| Flag | Default | |
|---|---|---|
| `--config` | none | a topology file: the platform, the projects and the output, each a Git repository (cloned; the changes committed and pushed) or a local directory (read and written in place), the output's never one of the sources' ([topologies](specs/04_topology.md)). Exclusive with `--tree` and `--output` |
| `--tree` | none | with `--output`: a local directory of the sources, `platform.yaml`, `catalogs.yaml`, `projects/`, read ([local directories](specs/04_topology.md#local-directories)) |
| `--output` | none | with `--tree`: a local directory of the output, `compiled/`, `argocd/`, written in place; never the directory of `--tree` |
| `--catalog` | none | `NAME=DIR`: the catalog `NAME` of `catalogs.yaml` read from a local directory instead of its URL; repeatable ([`--catalog`](#--catalog)) |
| `--charts` | none | a chart cache directory: the upstream charts of the instances pulled there, and the values checked against them ([`--charts`](#--charts)) |
| `--check` | | write nothing; fail when a compiled file is not up to date ([`--check`](#--check)) |
| `--path-prefix` | the topology's output path; with `--output`, the directory's path in its Git repository | the path of the output in its Git repository, for the paths the engines read ([`--path-prefix`](#--path-prefix)) |
| `--timeout` | `10m` | the bound of the whole run (a Go duration, above 0): the fetches and pushes of every repository, the catalogs, the charts, the compiles again; past it, the run fails, `okdp-gitops: timed out after <duration> (--timeout)`, nothing pushed that was not |

A compile:

1. of Git repositories, reads the output first: when its
   `sources.yaml` names the revisions every repository and catalog are
   still at, stops, up to date
   ([sources.yaml](specs/03_output.md#sourcesyaml));
2. reads every repository of the topology, the paths of the layout only
   (`platform.yaml`, `catalogs.yaml`, `projects.yaml`, the projects,
   `compiled/`, `argocd/`, `sources.yaml`), each file of 4 MiB at most
   (a local one by its size, before it is read): nothing beside them,
   whatever the repository holds; a Git repository is cloned on disk, in
   a temporary directory of the run, at the last commit of its branch
   only (a depth of 1, but from a server that does not fetch shallow),
   the size of a file read from its object header before the file is
   (the files together 64 MiB at most), a local directory read in place;
3. fetches the catalogs of `catalogs.yaml`, each at its ref only, its
   last commit, and compiles the tree with
   okdp-compiler, its mandatory rule set given (`deploy.Build`):
   `compiler.LoadRepository` of its parts (`catalogs.yaml` among them,
   its menu checked against the catalogs) and the catalogs, with
   `--charts` the upstream charts fetched, `CompileTree`, the final
   values checked against the charts ([the compile](specs/02_compile.md));
4. writes `compiled/` again: the files of the Flux target and, with an
   `argocd/root.yaml`, of the Argo CD target
   ([the output](specs/03_output.md)); and `sources.yaml`, the revisions
   it compiled, when every part and catalog is a Git repository;
5. writes the files whose bytes change and removes those no instance
   produces any more, a commit per Git repository that changes; prints
   each.

A project with an instance that does not compile keeps its output as
it was, and so does a project that depends on it; the others' is
written, every problem printed, and the run fails. A problem of the
whole tree (a file that does not load, a menu of `catalogs.yaml` that
names a service of no catalog, a catalog that cannot be read, a release
of a kept output written again) writes nothing; two projects
that share a release name are kept
([problems](specs/02_compile.md#problems)): the engines keep deploying
the last output.

| Line | Means |
|---|---|
| `compiled <file>` | written: a new file, or one whose bytes changed |
| `removed <file>` | removed: no instance produces it any more |
| `stale <file>` | `--check`: would be written |
| `orphan <file>` | `--check`: would be removed |
| `okdp-gitops: up to date` | `--check`: nothing to change |
| `okdp-gitops: up to date: the sources did not move since the compile sources.yaml records` | stopped before the compile: the record of the output holds ([sources.yaml](specs/03_output.md#sourcesyaml)) |

`<file>` is, in a local directory, the directory as `--output` or the
topology names it, then the path in it; in a Git repository,
`<url>: <path>`. The lines are on stdout, by repository, the files
written then those removed, each sorted by path; the logs and the errors
on stderr.

The test tree of the repository, `testdata/tree`, with its catalog,
`testdata/catalog`: the project `data`, its instance `postgresql`, a
database server that provisions the databases claimed of it (the
provider of the platform's default class of `database-server`
connections, `platform.yaml`); the project `demo`, its instances `app` (a
database of its own, the default claim), `reports` (the named claim
`analytics` of `project.yaml`) and `legacy` (the connection `ext-db` of
`project.yaml`). Its `catalogs.yaml` names a catalog `test-charts`, read
from the local directory instead; its `argocd/` is the output's, moved
to a directory of the output:

```console
$ testdata=$PWD/testdata okdp=$PWD/bin/okdp-gitops
$ cd "$(mktemp -d)" && cp -R "$testdata/tree" tree && mkdir output && mv tree/argocd output
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog"
compiled output/compiled/projects/data/instances/data-postgresql/Chart.yaml
compiled output/compiled/projects/data/instances/data-postgresql/application.yaml
compiled output/compiled/projects/data/instances/data-postgresql/helmrelease.yaml
compiled output/compiled/projects/data/instances/data-postgresql/instance.values.yaml
compiled output/compiled/projects/data/instances/data-postgresql/kustomization.yaml
compiled output/compiled/projects/data/instances/data-postgresql/okdp.info.yaml
compiled output/compiled/projects/data/kustomization.yaml
compiled output/compiled/projects/demo/instances/demo-app/Chart.yaml
...
compiled output/compiled/projects/demo/kustomization.yaml
compiled output/compiled/projects/kustomization.yaml
```

`demo-app`'s HelmRelease waits for `data-postgresql`
(`dependsOn: [{name: "data-postgresql"}]`), the provider of its claim,
and its Application is in the sync wave after it. A change of the tree
changes what is compiled from it, and nothing else:

```console
$ printf 'database: {class: large}\n' > tree/projects/demo/instances/legacy/config.yaml
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog" --check
stale output/compiled/projects/data/instances/data-postgresql/instance.values.yaml
...
okdp-gitops: the compiled files are not up to date: run okdp-gitops compile and commit the result
```

An instance that does not compile keeps the output of its project as
it was, the others' written, and each problem is reported; a kept
instance whose deployed values today's platform policy refuses has its
output withdrawn, the engines pruning it
([what a compile rewrites](specs/03_output.md#what-a-compile-rewrites));
a file that does not load writes nothing:

```console
$ printf 'database: nope\n' > tree/projects/demo/instances/legacy/config.yaml
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog"
okdp-gitops: 1 problem(s), the output of demo kept as it was:
demo/legacy: database: "nope" is not a connection, nor an output of an instance of the namespace
okdp-gitops: demo: kept, no revision recorded in sources.yaml, 11 file(s) re-derived
$ printf 'global: [\n' > tree/platform.yaml
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog"
okdp-gitops: 1 problem(s):
platform.yaml: cannot unmarshal yaml document: error converting YAML to JSON: yaml: line 1: did not find expected node content
```

### --check

`--check` compiles as a compile does and writes nothing: each file a
compile would write is printed `stale`, each it would remove `orphan`,
and the command fails, status 1, as it does when a project's output is
kept ([problems](specs/02_compile.md#problems)). For a repository whose
output a person commits, a pre-commit hook or a CI job checks that it is
up to date; with `--config`, the Git repositories are read and nothing
is pushed.

### --catalog

The packages come from the catalogs `catalogs.yaml` names, each a Git
repository at a ref ([catalogs.yaml](specs/01_layout.md#catalogsyaml)).
`--catalog NAME=DIR` reads the catalog `NAME` from the directory `DIR`
instead, relative to the current directory: a checkout of a catalog
being written, a CI that has the catalog at hand. A catalog the file
does not name is not read. The directory is read as a repository is:
its regular files only, `.git` and the symbolic links left out, a file
of 4 MiB at most, the catalog 128 MiB.

`--catalog` is the only way to a local catalog: a URL of
`catalogs.yaml` that is a local directory (an absolute path, `./...`,
`../...`) is refused, a `file://` one too unless the platform's
repository is itself local; `catalogs.yaml` is the content of the
platform's repository, not the operator's.

A catalog on the host of the platform's repository is fetched with the
credentials of the deployments repositories (the directory
`$GITOPS_CREDENTIALS_DIR`, else the token `$GITOPS_TOKEN`), as the
control plane fetches it, over `http://` to the hosts of
`$GITOPS_INSECURE_HTTP_HOSTS` only (refused, not read anonymously, for
another); one of another host anonymously
([Git repositories](specs/04_topology.md#git-repositories)).

### --charts

`--charts DIR`, a chart cache: the upstream charts the instances name in
their `Chart.yaml` are pulled into it, anonymously, from their OCI
registries or https chart repositories, once the tree loads and before
any instance compiles, then the tree compiled once: the final values of
each instance, coalesced over each chart's defaults as Helm coalesces
them, must meet the chart's `values.schema.json`, and a list it appended
to that lacks items of the chart's default list is a warning
([the steps](specs/02_compile.md#the-steps)). These checks are
okdp-gitops', not okdp-compiler's, which reads no upstream chart; a
`$ref` of a schema to a URL or a file is never fetched, and a chart's
subcharts are not checked; a chart
an instance gives upstream values to is rendered and its manifests
checked ([the render](specs/05_rules.md#the-render)); each chart the
compile checked is vendored in the umbrella, `charts/<chart>-<version>.tgz`,
so that the engines deploy what was checked
([the output](specs/03_output.md)). A chart that cannot be pulled leaves
the instances naming it unchecked, with a warning, and an instance of a
project that is not privileged that gives it upstream values refused.
The directory is kept between runs, each chart whole with its archive:
a chart is pulled once, an entry of an older cache, without its archive,
pulled again.

The puller (`chartcache`) reaches the charts over https,
trusting the system's certificates (`$SSL_CERT_FILE`) and the CA bundle
of the credentials; it refuses a chart host at a private, loopback or
link-local address (a blind request to the platform's network), but for
the hosts of `$GITOPS_PRIVATE_CHART_HOSTS`: comma-separated hosts,
`host:port` or `*.<domain>` (a registry of the cluster,
`*.svc.cluster.local`), another entry refused, status 1.
`$GITOPS_INSECURE_CHART_HOSTS` lists, the same way, the chart hosts
reached over plain HTTP (a development registry without TLS,
`okdp-registry:5000`): none by default, a chart host is reached over TLS
only; such a host is also allowed at a private address.

```console
$ "$okdp" compile --tree tree --output output --catalog test-charts="$testdata/catalog" --charts "$HOME/.cache/okdp/charts"
```

The compiled files are the same with or without it, checked or not.

### --path-prefix

The engines read the compiled tree in the output repository, at the
output's path: the chart of a HelmRelease (`./<prefix>/compiled/...`),
the `path` of an Application. `--path-prefix` is that path, when the
default is not:

- with `--config`, the default is the topology's output path: give
  `--path-prefix` when the output is a local directory that is a
  subdirectory of a checkout;
- with `--output`, the default is the directory's path in its Git
  repository (`git rev-parse --show-prefix`), `""` outside one or
  without `git`: give it when the directory is not in a checkout of the
  output repository (a copy, the image).

### Exit status and errors

| Status | When |
|---|---|
| 0 | compiled, each file written or removed printed, or nothing to change; `--check`: up to date |
| 1 | a project whose output is kept, the others' written: every problem after `okdp-gitops: <n> problem(s), the output of <project>, ... kept as it was:`, one per line, then a line per kept project, `okdp-gitops: <project>: kept since <revision> of <url>#<branch> (sources.yaml), <n> file(s) re-derived` ([problems](specs/02_compile.md#problems)); a problem of the whole tree, nothing written: every one after `okdp-gitops: <n> problem(s):`; `--check`: a file not up to date, or a project kept; any other error, `okdp-gitops: <message>`: a repository or a catalog that cannot be read, a push that fails, a repository that kept moving |
| 2 | no command, or another than `compile` and `workflow`; an unknown flag, a flag without its value, an argument that is not a flag, neither `--config` nor `--tree` and `--output`, `--config` with either, `--root` (gone: `--tree DIR --output DIR`), a `--catalog` that is not `NAME=DIR`, `workflow` without `--for output` (a repository of the sources holds no workflow) or without `--config`, `--help`: the message and the usage on stderr |

Besides, stderr has the logs: the compiler's warnings, by instance
(`instance=demo/app`), the charts that could not be pulled, the commits
pushed.

### workflow

`okdp-gitops workflow` prints, on stdout, the workflow of the output
repository ([the CI of the output](#the-ci-of-the-output)), the
topology of the compile in it:

| Flag | Default | |
|---|---|---|
| `--for` | required | the repository of the workflow: `output`, the only one that has one. `platform` and `project`, the repositories of the sources, are refused, status 2: they hold no workflow and no token |
| `--config` | required | the topology file the job compiles, written in the workflow base64 encoded, so that nothing of it is read by the shell, by YAML or by Forgejo's expressions: Git repositories over HTTP(S) (`https://`, `http://`), the job's token their credentials; a local directory, an SSH URL, a password in a URL, a control character but a line feed and a tab (YAML reads a lone CR, NEL, LS or PS as a line break), or an `http://` URL of a host that `$GITOPS_INSECURE_HTTP_HOSTS` of the command's environment does not allow is refused |

```console
$ cat topology.yaml
platform: {url: https://git.example.org/okdp/okdp-sources.git}
projects: {url: https://git.example.org/okdp/okdp-sources.git, path: "projects/{project}"}
output:   {url: https://git.example.org/okdp/okdp-output.git}
$ bin/okdp-gitops workflow --for output --config topology.yaml
# Generated by okdp-gitops. Do not edit.
name: okdp-gitops
on:
  schedule:
    - cron: "*/2 * * * *"
  workflow_dispatch: {}
concurrency:
  group: okdp-gitops
  cancel-in-progress: false
jobs:
  compile:
    runs-on: okdp-gitops
    container:
      volumes: ["okdp-charts:/var/cache/okdp/charts"]
    steps:
      - name: okdp-gitops compile
        shell: sh
        env:
          GITOPS_TOKEN: ${{ secrets.OKDP_GITOPS_TOKEN }}
        run: |
          topology="$(mktemp)"
          printf %s 'cGxhdGZvcm06IHt1cmw6IGh0dHBzOi8vZ2l0LmV4YW1wbGUub3JnL29rZHAvb2tkcC1zb3VyY2VzLmdpdH0KcHJvamVjdHM6IHt1cmw6IGh0dHBzOi8vZ2l0LmV4YW1wbGUub3JnL29rZHAvb2tkcC1zb3VyY2VzLmdpdCwgcGF0aDogInByb2plY3RzL3twcm9qZWN0fSJ9Cm91dHB1dDogICB7dXJsOiBodHRwczovL2dpdC5leGFtcGxlLm9yZy9va2RwL29rZHAtb3V0cHV0LmdpdH0K' | base64 -d > "$topology"
          okdp-gitops compile --config "$topology" --charts /var/cache/okdp/charts
$ bin/okdp-gitops workflow --for project --config topology.yaml
okdp-gitops: workflow --for project: a repository of the sources holds no workflow and no token: the output repository compiles the sources on a schedule (workflow --for output), with the only token, its secret OKDP_GITOPS_TOKEN
```

The job runs every 2 minutes, and when it is dispatched (the Actions page
of the repository, or its API), never on a push: no push to a repository
of the sources starts it, no push of its own either. A run that starts
while another is under way waits for it (`concurrency:`, a group of the
workflow that cancels nothing: Forgejo documents it at the workflow level,
as a best-effort attempt to run one at a time; the output is written on
the revision the compile read all the same, so two runs at once stay
safe). It runs in a container of the okdp-gitops image, which the runner's
label names, the Docker volume `okdp-charts` mounted at
`/var/cache/okdp/charts`; its script, the image's `sh`, decodes the
topology (`base64 -d`) into a file of its own and compiles it, with the
token of the output repository's secret `OKDP_GITOPS_TOKEN`
(`$GITOPS_TOKEN`, which Forgejo masks in the logs).

The token goes over `http://`, in clear, to the hosts the command's
`$GITOPS_INSECURE_HTTP_HOSTS` allows only: comma-separated host names or
globs of them, a cluster's services in a sandbox. A topology with an
`http://` URL of another host is refused; with one of an allowed host,
the job's environment carries the hosts or globs its URLs need, which
`okdp-gitops compile` checks the same way
([credentials](specs/04_topology.md#git-repositories)):

```console
$ GITOPS_INSECURE_HTTP_HOSTS='*.svc.cluster.local' bin/okdp-gitops workflow --for output --config topology.yaml
...
        env:
          GITOPS_TOKEN: ${{ secrets.OKDP_GITOPS_TOKEN }}
          GITOPS_INSECURE_HTTP_HOSTS: "*.svc.cluster.local"
...
```

When the repositories are served over `https://` with a CA of the
platform's own (a sandbox's, the Forgejo behind its ingress), the job
trusts the CA bundle `$GITOPS_RUNNER_CA_FILE` of the command's
environment names, an absolute path on the runner's Docker daemon, in a
directory of its own, of letters, digits, `.`, `_`, `-` and `/`: the
workflow mounts the directory read-only at the same path in the job
(`container.volumes: ["okdp-charts:/var/cache/okdp/charts",
"/etc/okdp/ca:/etc/okdp/ca:ro"]`, which the runner allows, `valid_volumes`)
and sets `SSL_CERT_FILE` to the file, which Go reads instead of the
system's certificates: the bundle holds the public CAs too (cert-manager's
trust-manager `useDefaultCAs`). The token then crosses no network in
clear, and `$GITOPS_INSECURE_HTTP_HOSTS` stays empty:

```console
$ GITOPS_RUNNER_CA_FILE=/etc/okdp/ca/ca.crt bin/okdp-gitops workflow --for output --config topology.yaml
...
    container:
      volumes: ["okdp-charts:/var/cache/okdp/charts", "/etc/okdp/ca:/etc/okdp/ca:ro"]
...
        env:
          GITOPS_TOKEN: ${{ secrets.OKDP_GITOPS_TOKEN }}
          SSL_CERT_FILE: "/etc/okdp/ca/ca.crt"
...
```

The job's environment carries, as they are, the chart hosts the
command's `$GITOPS_PRIVATE_CHART_HOSTS` allows at a private address
([--charts](#--charts)), a registry of the cluster the checks pull
from; an entry that is not a host, `host:port` nor `*.<domain>` is
refused.

## okdp-compile

`okdp-compile` compiles a tree from local directories and writes the
files of one target. It reads no Git repository and commits nothing:
a catalog's CI, a person writing a package, a script. It compiles as
`okdp-gitops compile` does (`deploy.Build`: okdp-compiler's [rule
set](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md),
Helm's merge of the final values and the upstream and ingress
[policy](specs/05_rules.md)).

```text
okdp-compile --repository <dir> [--repository <dir>]...
             [--catalog <dir>]... -o <dir>
             [--target helm|umbrella|flux|argocd] [--path-prefix <path>]
             [--flux-source <namespace>/<name>]
             [--repo-url <url>] [--revision <revision>]
             [--cost-limit <n>] [--instance-timeout <duration>]
             [--timeout <duration>] [--force]
             [--report <file>|-] [--partial] [--allow-plain-secrets]
okdp-compile --check [--charts <dir>] [--report <file>|-]
             [--allow-plain-secrets]
             --repository <dir> [--repository <dir>]...
             [--catalog <dir>]...
okdp-compile --version
```

| Flag | Default | |
|---|---|---|
| `--repository` | required | a repository of the tree, its directory, repeatable: any part of the [tree](specs/01_layout.md), each part in one repository; every instance of the tree compiled together |
| `--catalog` | | a catalog of packages, its directory, repeatable: `catalog/` and `contracts/` only, each service in one repository; one that holds `platform.yaml` or `projects/` is refused |
| `--output`, `-o` | required, refused with `--check` | the output directory, under `projects/<namespace>/instances/<release>/` the files of `--target`; every symbolic link under its `projects/` removed first, nothing written through one |
| `--check` | | compile and check, write nothing: the errors and warnings, no `--output` |
| `--charts` | `$OKDP_CHARTS`, with `--check` | a chart cache directory, read by `--check` only: each upstream chart unpacked as `helm pull --untar -d <dir>/<repository>/<chart>-<version>` writes it, `<repository>` the URL without its scheme; the final values checked against each. Without `--check`, refused |
| `--target` | `helm` | what is written ([the targets](specs/03_output.md#the-targets)): `helm`, the values of each instance, no chart; `umbrella`, each instance's umbrella chart; `flux`, with its HelmRelease, the Kustomizations and each project's manifests; `argocd`, with its Application and each project's manifests |
| `--path-prefix` | the root | `--target flux` and `argocd`: the path of the output directory in the repository the deployer reads |
| `--flux-source` | `flux-system/okdp-gitops` | `--target flux`: the GitRepository of that repository, `<namespace>/<name>` |
| `--repo-url` | required by `--target argocd` | `--target argocd`: the repository Argo CD reads the output directory from |
| `--revision` | `HEAD` | `--target argocd`: its revision |
| `--cost-limit` | `1000000` | the CEL cost each instance's expressions may use together, past which the instance is refused; `0`, no limit |
| `--instance-timeout` | `1m` | how long one instance's compile may take, a Go duration: past it that instance is refused, the others compiled, and `--partial` keeps its project; `0`, no limit |
| `--timeout` | `10m` | how long the compile may take, every instance together: past it, nothing is written; `0`, no limit |
| `--report` | | a JSON report of the compile, to the file, or to stdout for `-`, even with `--check` and when the compile fails ([`--report`](#okdp-compile---report)); refused inside a `--repository` or `<output>/projects/` |
| `--partial` | | write the projects whose instances all compile, and keep what the output holds of the others', status 1 all the same ([`--partial`](#okdp-compile---partial)); refused with `--check` |
| `--force` | | write into an `<output>/projects/` that holds files but no `projects/.okdp-compile`, removing from it every file it does not write; refused with `--check` |
| `--allow-plain-secrets` | | accept credentials written in clear: a `provides` `secret` without `generate`, a Secret of the `objects` section with `data` or `stringData`; without it they fail the compile, the output being committed |
| `--version` | | print the version and exit |

Flags take one or two dashes, and every input is a flag: an argument
that is not one is refused, where it would end the flags and those
after it be ignored. The directories of `--repository`, `--catalog` and
`--charts` must exist. An option of another target is refused. Every
output is built before any is written: anything that fails writes
nothing; an error while writing may leave some of the files written.

What the command reads is bounded, and no symbolic link is followed. A
link under a directory fails, named, `<repository>/<path>: a symbolic
link, not followed`. So does a file that is neither regular nor a
directory, a file over 4 MiB, and a new file once those read of one
directory pass 64 MiB together. The chart cache is bounded by chart: the
files of each chart's directory 64 MiB together.

### okdp-compile --repository

Every instance of a tree compiled together, each read from
`projects/<namespace>/instances/<instance>/`, the release
`<namespace>-<instance>`. The tree says where each instance goes: its
project is the namespace the deployers install it to, which no flag
overrides. In order:

1. the tree: every problem at once; a project's own (its `project.yaml`,
   an instance's files, the version it names) refuses that project, not
   the tree ([`--partial`](#okdp-compile---partial));
2. the instances, each claim bound to its provider, checked against the
   chart cache with `--check --charts`: each binding on stderr,
   `<namespace>/<release>: <field>: claim <id> (<contract>) bound to
   <namespace>/<release>`, then each refusal, `error:
   <namespace>/<release>: <message>`, each warning, `warning:
   <namespace>/<release>: <message>`;
3. their deployment graph: each problem on stderr, `error:
   <namespace>/<release>: <message>`;
4. the files of the target, written under `-o`; then each dependency,
   `<namespace>/<release>: depends on <instance> (<contract> <output>,
   <field>)`.

Any problem exits with status 1, nothing written. `--check` stops before
writing, and prints no dependency.

```console
$ bin/okdp-compile --repository examples/03_full/input/db -o out/tree
demo/demo-workspace: catalogs[1].database: claim demo.marketing (database) bound to demo/demo-db, the default class standard
demo/demo-workspace: database: claim demo.workspace.database (database) bound to demo/demo-db, the default class standard
demo/demo-workspace: depends on demo-db (database demo.marketing, catalogs[1].database)
demo/demo-workspace: depends on demo-db (database demo.workspace.database, database)
demo/demo-workspace: depends on demo-alerts (http alerts, notifications.webhook)
```

`out/tree` then holds `examples/03_full/output/`, byte for byte. The
examples keep this tree on purpose, `input/db/` as the command reads it:
okdp-compiler's own examples show a package and its values, but a
deployer needs the namespace, which here is the directory of the
project.

### okdp-compile --output

The default target, `helm`, writes the values of each instance and no
chart: the package of the catalog is installed as it is.

| File | Content |
|---|---|
| `values.yaml` | the final values of each key of the package, under the key, after the line `# Code generated by okdp-compile. DO NOT EDIT.`; a dependency the mapping leaves out is switched off, `<key>: {enabled: false}`, for which its `Chart.yaml` must declare `condition: <key>.enabled` |
| `okdp-objects.values.yaml` | when the instance has objects, the values of the `okdp-objects` chart: `manifests`, its objects as one YAML stream, a string |
| `okdp.info.yaml` | when the instance publishes a URL, a usage or a connection: `url`, `usage` and `provides` |

The other targets write one chart per instance, an umbrella of the
package's charts and of `okdp-objects`:

| File | Content |
|---|---|
| `Chart.yaml` | the package's `Chart.yaml`, its metadata kept: the dependencies the mapping renders, in their order, as written, then `okdp-objects` when the instance has objects or OAuth clients |
| `instance.values.yaml` | the final values of each key under the key, and the `okdp-objects` chart's under `okdp-objects`. There is no `values.yaml`: given as user values, a null of the final values deletes an upstream default, where Helm drops the nulls of defaults |
| `okdp.info.yaml` | as above |

```console
$ bin/okdp-compile --repository examples/02_advanced/input/db -o out
demo/demo-webapp: database: claim demo.webapp.database (database) bound to demo/demo-db, the default class standard
demo/demo-webapp: depends on demo-db (database demo.webapp.database, database)
$ ls out/projects/demo/instances/demo-webapp
okdp.info.yaml  okdp-objects.values.yaml  values.yaml
$ bin/okdp-compile --repository examples/02_advanced/input/db -o umbrella --target umbrella 2>/dev/null
$ cat umbrella/projects/demo/instances/demo-webapp/Chart.yaml
apiVersion: v2
name: webapp
version: 1.0.0
dependencies:
  - name: app
    repository: https://charts.example.org
    version: 1.8.0
  - name: okdp-objects
    repository: oci://quay.io/okdp/charts
    version: 0.1.0
```

The chart's name will be `oci://quay.io/okdp/charts/okdp-objects`, but
it is not published there: only the sandbox's local registry holds it,
`127.0.0.1:5001/okdp/charts`, and `global.okdp.charts.objects` of
`platform.yaml` points a platform at it.

The directory is created if needed. The command owns
`<output>/projects/`: a file there it does not write is removed, and the
directories left empty, so `-o` and each `--repository` must not contain
one another, nor a `--repository` another. Nothing else of the directory
is read, removed or checked.

The command marks the directory it owns, `projects/.okdp-compile`, and
refuses a `projects/` that holds files without it, before anything is
compiled: `<output>/projects/ exists and was not written by okdp-compile
(no projects/.okdp-compile): choose an empty output, or remove it
yourself`. `--force` writes over it. A `projects/` that is a symbolic
link fails, even with `--force`.

### okdp-compile --check

`--check` compiles as `-o` does and writes nothing; it takes no `-o`.
With `--charts <dir>` (`$OKDP_CHARTS` by default), each upstream chart
the instances render is read from a chart cache, its `values.yaml` and
`values.schema.json`, nothing else of it, its subcharts' neither; the
cache is read as it is, nothing pulled (`deploy.StaticCharts`). These
checks are okdp-gitops' (`internal/chartcheck`): okdp-compiler reads no
upstream chart.

- the final values of each key, coalesced over the chart's defaults as
  Helm coalesces them (Helm's `chartutil.CoalesceValues`), an empty
  `global` added, must meet its `values.schema.json`: a violation is an
  error, those of every key reported together; a `$ref` of the schema
  to a URL or a file is never fetched;
- an instance list appended to where the chart's defaults hold a list
  some item of which the final list lacks is a warning: the package
  restates the items it keeps;
- a chart the cache lacks fails the instance, `<key>: chart
  <chart>-<version> is not in the chart cache: helm pull ...`, the
  command that unpacks it there, `<chart cache>` standing for the
  directory (the catalogs' `scripts/compile-charts.sh` runs it).

The final values are the same with or without the chart cache. The full
example's chart cache, `examples/03_full/.charts/`, holds the charts of
its instances. In `bad`, a copy of its tree whose workspace's
`config.yaml` adds `{name: bad-name, value: x}` to `upstream.api.env`:

```console
$ bin/okdp-compile --check --charts examples/03_full/.charts --repository bad
error: demo/demo-workspace: compiled values don't meet the specifications of the schema(s) in the following chart(s):
api (workspace-3.2.0):
- at '/env/26/name': 'bad-name' does not match pattern '^[A-Za-z_][A-Za-z0-9_]*$'
```

### okdp-compile --report

`--report <file>` writes what the compile gives as JSON, for a CI job or
a bot that comments a pull request: `-` writes it to stdout, which the
command prints nothing else on. It is written with `--check` too, and
when the compile fails (the exit status is the same), but not when
`--report` itself is refused.

| Key | |
|---|---|
| `instances` | every instance of the tree, in the order of its repositories' projects and instances |
| `instances[].namespace`, `instance`, `release` | the project, the instance's name, its release |
| `instances[].service`, `version` | its package |
| `instances[].privileged` | its project is one of the platform's own (`global.okdp.policy.privileged`); `false` when refused |
| `instances[].status` | `compiled`; `refused`, its compile failed, the graph has a problem at its release, or an instance of another project has its release name (`--target flux`, `argocd`); `skipped`, not compiled, the compile stopped (`--timeout`), or, with `--partial`, compiled but not written, another instance of its project refused |
| `instances[].error` | why it is refused, the message stderr prints after `error: <namespace>/<release>: `; `""` when it is not |
| `instances[].warnings` | its warnings |
| `instances[].dependencies` | the instances it depends on, `{instance, namespace, contract, field, output}`, `namespace` when another |
| `instances[].claims` | each claim of its references bound to its provider, `{field, id, contract, provider: {namespace, release}, class}` |
| `instances[].charts` | the upstream charts it renders, by key, `{key, repository, chart, version}` |
| `errors` | what fails that is no instance's: an option, a repository, a problem of a project that does not load, the target, the output, `--timeout` |
| `written` | the files were written under `-o`, those of some projects only with `--partial` |

The lists are empty, never `null`.

```console
$ bin/okdp-compile --check --report - --repository examples/02_advanced/input/db 2>/dev/null | jq -c '.instances[] | [.release, .status, .claims[].id]'
["demo-db","compiled"]
["demo-webapp","compiled","demo.webapp.database"]
```

### okdp-compile --partial

Without it, one refused instance writes nothing. With `--partial`, a
refusal fails its project, not the tree: the projects whose instances
all compile, and at whose releases the graph has no problem, are
written; the others are not, what the output holds of their instances
kept as it is, each printed after the refusals. The rule is that of
[problems](specs/02_compile.md#problems). What the platform decides of a
project kept is written all the same, of today's settings: its manifests
and, with `--target flux`, each kept instance's HelmRelease, so that a
privilege revoked or a role taken back is not kept by a project one of
whose instances is refused.

```text
error: demo/demo-web: values don't meet the specifications of the schema(s) in the following chart(s):
web:
- at '/replicas': minimum: got 0, want 1
error: demo: not written, an instance of it refused (--partial): out/projects/demo/ kept as it is, its namespace, identity and roles of today's settings
```

A project whose namespace is reserved since the output was written is
not kept: its instances are refused, and its output dropped, said so. A
project that does not load fails alike, as a whole, its output kept; the
other projects are written. Only the platform's parts fail the tree,
with or without `--partial`: `platform.yaml`, `catalogs.yaml`, a
repository and its role, a package that does not load, a contract that
differs between repositories. An instance of another project that
depends on a release not written fails its own project in turn, its
report status `skipped`. A release name two projects share fails both,
with `--target flux` and `argocd`.

The command exits with status 1 all the same. A failure of the tree
writes nothing, as without `--partial`: an option, a repository, the
target's files, a release a project kept holds and a project written has
too, `--timeout`.

### Exit status of okdp-compile

| Status | When |
|---|---|
| 0 | compiled, or checked (`--check`), each binding and warning on stderr, then, the files written, each dependency; `--version`, `--help` |
| 1 | any error: `error: <message>` on stderr, one per refused instance and per problem of the graph, `error: <namespace>/<release>: <message>`; one per problem of a project that does not load, `error: <repository>/<path>: <message>`; with `--partial`, one per project not written too, `error: <namespace>: not written, ...`, the others written |
| 2 | an unknown flag, a flag without its argument, an argument that is not a flag: the message and the usage on stderr |

A message does not print the value of a sensitive key: a schema
violation of a password, a token, a client secret, an API key, or a key
whose name ends with `Password`, `Secret`, `Token` or `Key`, prints
`<redacted>` in its place. A warning does not fail the compile: it is
printed before the files are written, and the command exits with status
0.

### Installing with Helm

The default target's output installs with `helm install`, without a
chart of its own: the `okdp-objects` chart first, whose Secrets the
instance's pods read, then the package of the catalog as it is, its
dependencies built. The `okdp-objects` chart is OKDP/okdp-objects-chart
0.1.0, installed from a checkout, as it is not published to a registry
yet. `<dir>` is `<output>/projects/<namespace>/instances/<release>`:

```bash
helm upgrade --install <release>-okdp ../okdp-objects-chart \
  --namespace <namespace> -f <dir>/okdp-objects.values.yaml
helm dependency build <package>
helm upgrade --install <release> <package> --namespace <namespace> -f <dir>/values.yaml
```

Skip the first line when the instance has no `okdp-objects.values.yaml`.
With the sandbox's registry, the chart is
`oci://127.0.0.1:5001/okdp/charts/okdp-objects`, with `--plain-http`.
The release of the objects adds `-okdp` to the instance's, which the
target refuses past 48 characters, Helm taking 53. Install under the
release and the namespace of the compile: the values are compiled for
them.

An umbrella, `--target umbrella`, is one release, its dependencies built
in a copy of the directory, `helm dependency build` adding `Chart.lock`
and `charts/`, which the next compile into it refuses:

```bash
chart=$(mktemp -d) && cp -R <dir>/. "$chart" && helm dependency build "$chart"
helm upgrade --install <release> "$chart" --namespace <namespace> -f <dir>/instance.values.yaml
```

Do not add OKDP's defaults or the values files of the compile: the final
values already merge them, and Helm applies the chart's own defaults.
The `okdp-objects` chart writes each document of its `manifests` value
as a manifest, as written: the value is one string, so that Helm changes
neither the `{{` of an ExternalSecret's template, nor a null, nor a
large integer.

In an umbrella, each upstream chart renders as a subchart, with the
final values of its key, as it renders alone but for what Helm does to
every subchart:

- an aliased subchart is named after its alias: a name the chart derives
  from `.Chart.Name` changes, unless the values set it;
- every subchart has a `global` map, empty when nothing sets one;
- a null default nested in a map of the chart's `values.yaml`, which the
  values leave unset, stays null, where the chart alone drops it. A
  template reads both the same, except `hasKey`, `toYaml`, and `range`
  over the map. With Helm 3.19, that null default is dropped.

### In a CI

The repositories of the parts checked out side by side, any error
failing the job, the output committed and checked against the commit:

```bash
bin/okdp-compile --repository platform --catalog catalog --repository projects \
  -o compiled --target flux || exit 1
test -z "$(git status --porcelain compiled/)" || { git status --short compiled/; exit 1; }
```

`git status --porcelain` lists the changed, removed and new files,
where `git diff --exit-code` misses a new, untracked file.
The mark of the output, `compiled/projects/.okdp-compile`, is committed
with them. A job that pulls the upstream charts into a chart cache checks
the final values against them too: the same flags, `--check --charts
charts` for `-o`.

## The CI of the output

A commit of the tree deploys: the output repository holds the workflow
`.forgejo/workflows/okdp-gitops.yaml` (Forgejo Actions), which runs
`okdp-gitops compile` every 2 minutes, and on demand, and commits the
output, which the engines reconcile. A commit of a person and one of the
control plane are compiled the same way, by the next run.

```bash
okdp-gitops workflow --for output --config topology.yaml > .forgejo/workflows/okdp-gitops.yaml   # in the output repository, on its default branch
```

The workflow goes on the default branch of the output repository, the
branch Forgejo runs a schedule of, by hand, once, and again when the
topology changes; the Forgejo instance must run the schedules of its
Actions. The repositories of the sources, the platform's
and every project's, hold no workflow and no secret: the control plane
creates the repository of a new project of a split topology empty of
both. Each run is a job of its own, on a runner dedicated to
okdp-gitops, which provides:

| | |
|---|---|
| label `okdp-gitops` | on the runner's docker executor, mapped to the okdp-gitops image ([above](#the-image)): `okdp-gitops:docker://<image>`. Each job runs in a new container of the image, removed after it, with nothing of the runner's: no file, no credentials, no Docker socket |
| the Docker volume `okdp-charts` | the only volume the jobs may mount (`container.valid_volumes: [okdp-charts]`), writable by the image's user, 65534: the chart cache of the checks, kept between the jobs |
| the CA bundle's directory | with `$GITOPS_RUNNER_CA_FILE`, a directory of the runner's Docker daemon the jobs may mount read-only (`container.valid_volumes`), the platform's CA bundle in it ([above](#workflow)) |
| registration | for the output repository only: a runner of an organization would run the jobs of every repository of it, those of the sources included |

The output repository has the secret `OKDP_GITOPS_TOKEN`, the only token
of the CI: a Forgejo access token of scope `write:repository`, of a
user, a bot, with write access to the output repository and read access
to the repositories of the sources and to the catalogs of its host, the
job's `$GITOPS_TOKEN`
([credentials](specs/04_topology.md#git-repositories)). Whoever can push
a workflow to a repository can read its secrets: the bot is the only
writer of the output repository (a protected branch whose push
whitelist is the bot), the people and the control plane write the
sources only, which hold none. A writer of the sources changes what is
compiled, through the compiler's checks, never the output itself.

The sources hold no workflow, for that reason: a source repository's
workflow that pushes needs a write token, which every writer of the
repository could then read, and push an output the engines deploy with
every right (an Application of the AppProject of the platform), past
every check of the compile. A schedule needs no event of the sources:
the delay of a commit is that of the next run, at most 2 minutes, small
next to the engines' own refresh (3 minutes for Argo CD).

A run whose sources did not move since the last compile stops after
reading the output and listing the refs of the sources and the
catalogs: the output's `sources.yaml` records the revisions it is the
compile of ([sources.yaml](specs/03_output.md#sourcesyaml)). Else it
compiles the whole tree from the latest revision of each repository,
and pushes what changed, the record at least. Runs that overlap end on
the compile of the latest sources
([the compile's commits](specs/04_topology.md#the-compiles-commits)). An
instance that does not compile (a claim no provider binds, a value an
upstream chart refuses) keeps the output of its project as it was, and
of the projects that depend on it, the others' written; the run fails,
and its log reports every problem. A problem of the whole tree writes
nothing ([problems](specs/02_compile.md#problems)): the engines keep
deploying the last output.

## The engines

The output is deployed by Flux or Argo CD, from the output repository
only. Their entry points are installed once, at the platform's bootstrap
([what the engines need](specs/03_output.md#what-the-engines-need)). A
deployer that renders an umbrella, one of these or another, respects
what it is compiled for:

**The release.** The values of every key are compiled for the instance's
release name and namespace, both given by the tree,
`projects/<namespace>/instances/<instance>/`: the umbrella renders under
these, one release per instance, one Flux HelmRelease or one Argo CD
Application.

**The values.** `instance.values.yaml` is given to Helm as user values:
Flux `valuesFrom` a ConfigMap of the file (a kustomize
`configMapGenerator`), Argo CD `helm.valueFiles`, `helm -f`. Never as
the chart's defaults, Flux `chart.spec.valuesFiles` or a `values.yaml`
of the chart: Helm drops the nulls of defaults, where a null of the
final values deletes an upstream default.

**The chart.** The umbrella's directory is committed with the instance's
compiled files, and the chart built from the output repository. Flux
needs `reconcileStrategy: Revision`, which rebuilds the chart at every
revision: its version, the package's, does not change when the platform
changes the version of a dependency. Both fetch the dependencies from
their repositories, `okdp-objects` among them: a private repository
needs its credentials there, and a platform names a registry of its own
with `global.okdp.charts.objects`. Each renders with the Helm it embeds:
Flux 2.9 Helm 4.2.4, Flux 2.8 Helm 4.2.0, Flux 2.7 Helm 3.19.2; Argo CD
3.5 Helm 4.2.1, Argo CD 3.4 and 3.3 Helm 3.19.4. The umbrella, the final
values and the schema reports are checked with each (`testdata/helm`,
`make fixtures`).

**The objects.** The credentials Secrets, the mapping's objects and the
ExternalSecrets of the credentials read from the platform's secret store
are the `manifests` value of the `okdp-objects` chart, in the instance's
release: deployed with the upstream charts, pruned with them, but those
Helm keeps (`helm.sh/resource-policy: keep`, a claim's database), which
carry Argo CD's equivalents too, `argocd.argoproj.io/sync-options:
Prune=false,Delete=false`: Argo CD renders with `helm template` and
reads no Helm annotation. Under Flux, they travel in the ConfigMap
`values-<release>` of `okdp-releases`: the `flux` target refuses an
`instance.values.yaml` over the 1 MiB of a ConfigMap (that instance
refused, its project kept, the others written), and, with
`--allow-plain-secrets`, the data of a Secret is in that ConfigMap.

**OAuth clients.** Each is a Job among the instance's objects, a Helm
pre-install/pre-upgrade hook (an Argo CD `PreSync` hook), which writes the
client's Secret; the pods that read it do not start before it exists. A
changed `redirectUris` reaches the provider at the next sync, and a
`pre-delete` hook (Argo CD `PostDelete`) removes the client and its Secret
at uninstall ([OAuth
clients](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md#oauth-clients)).

**What the instance publishes.** `okdp.info.yaml`, beside its chart: its
`url`, its `usage`, and `provides`, the connections it provides with
their `secretRef`. Nothing of it is deployed: a control plane or a
console reads it from the output, to list the instances of a namespace
and offer the connections they provide.

## Go library

`github.com/okdp/okdp-gitops`, the module of the binary, is the control
plane's library
([okdp-control-plane-server](https://github.com/OKDP/okdp-control-plane-server)):
it commits its edits of the tree through it, reads the tree, the compile
of the tree, and the output. Package `treeedit` holds the rules of the
edits of the tree: okdp-gitops reads the tree from its repositories,
applies an edit and its check, and commits the change.

| Package | |
|---|---|
| `gitops` | the stores: `Store` (`View`, a `treeedit.Reader`; `Update`, a `Tx`, a `treeedit.Writer`), `GitStore` (a Git remote, go-git), `MemoryStore`, `Repositories` (the parts of a `Topology`); `Credentials`: the credentials of the repositories, the catalogs and the Forgejo API, the `http://` hosts allowed; `Compiler`: the compile of the tree a store holds (`SkipRender`: no render of the charts, for a long-running process; [the render](specs/05_rules.md#the-render)); `Deployments`: the edits of `treeedit` committed (`Apply`, a method per edit), the reads of the tree and of the output through `treeedit`, the output (`WriteCompiled`); `ForgejoClient`; the workflow of the output (`OutputWorkflow`) |
| `catalog` | the catalogs of `catalogs.yaml` (each a `treeedit.CatalogRepository`): `Loader` (Git repositories at their ref, local directories), `Catalogs`, indexed by okdp-compiler (`Index`, a `compiler.Catalog`: services, versions, packages, contracts) |
| `deploy` | `Build(ctx, repos, BuildOptions)`: `compiler.Build` with the compiler's mandatory rule set, Helm's merge and the policy, each unless the caller gives another, the final values checked against the upstream charts of `BuildOptions.Charts` (`SkipRender`: the same `Finalize`, the charts not rendered); `ChartSource` (a `chartcache.Cache`, or `StaticCharts(fsys)`, a directory filled already), `NewCharts(src)`, the `Charts` of one build (`Failures`, `FS`); `CoalesceValues(values, defaults)`: values over a chart's defaults as Helm coalesces them; the targets (`Helm`, `Umbrella`, `Flux`, `ArgoCD`); `TreeFiles(t, targets, previous, charts)`: the files of a tree; `Write`: those of one compile for a plain `helm install`; `ReadCompiled`, `ReadPublished`, `ReadArgoRoot`, `ProjectAccess`, `CheckReleases` |
| `rules` | `Standard`: the compiler's mandatory rule set ([the policy](specs/05_rules.md)) |
| `treeedit` | the tree read, its edits and their checks: `Reader`, `Writer`, `Apply`, `Check.Verify`, `ReadSources`, `OutputTargets`, `OutputFS`, `WriteOutput` |
| `chartcache` | the chart cache of the checks, a `deploy.ChartSource` (`Get` pulls a chart it lacks, `FS` gives them): `NewCache`, `NewRepositoryPuller`, set as `Compiler.Charts` |

A program that calls `compiler.Build` itself, without `deploy.Build`,
compiles with okdp-compiler's mandatory rules alone: no namespace, object,
upstream or host policy, and no check against the upstream charts, which
okdp-compiler does not read.

`deploy.Build` with `BuildOptions.Charts` fetches the upstream charts of
every `Chart.yaml` of the tree once the tree loads, before any instance
compiles (`compiler.BuildOptions.Loaded`, which it chains to the
caller's): no fetch counts against `InstanceTimeout`. A `Charts`
(`deploy.NewCharts`) serves one build: each chart fetched once, read
once, its schema compiled once; a fetch the context ends is not recorded
as a failure. `Charts.Failures` says why each chart that could not be
fetched could not be: the keys it renders are unchecked. `deploy.Build`
replaces `compiler.BuildOptions` in its signature, and `SkipRender`
replaces `deploy.WithoutRender()`; okdp-compiler no longer has
`Inputs.Charts`, `CompileTree`'s chart source, `StaticCharts`,
`ChartFailures` nor `CoalesceValues`: they are `deploy`'s.

The test tree, compiled in memory, as `okdp-gitops compile` compiles it,
then changed as the control plane changes it (run from the repository
root):

```go
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"

	compiler "github.com/okdp/okdp-compiler"

	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/gitops"
	"github.com/okdp/okdp-gitops/treeedit"
)

func main() {
	ctx := context.Background()
	// The tree, by path.
	files := map[string]string{}
	tree := os.DirFS("testdata/tree")
	err := fs.WalkDir(tree, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := fs.ReadFile(tree, p)
		files[p] = string(data)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	store, err := gitops.NewMemoryStore(files)
	if err != nil {
		log.Fatal(err) // a path out of the store
	}
	d := gitops.NewDeployments(store)
	d.Compiler = gitops.NewCompiler(&catalog.Loader{Dirs: map[string]string{"test-charts": "testdata/catalog"}})

	// The compile of the tree: the claims bound to each provider.
	t, err := d.Tree(ctx)
	if err != nil {
		log.Fatal(err) // a *compiler.LoadError: every problem of the load
	}
	for _, c := range t.Instance("data", "postgresql").Provided {
		fmt.Printf("%s provisions %s for %s/%s\n", "data-postgresql", c.ID, c.Claimer.Namespace(), c.Claimer.Name)
	}

	// What the control plane does: a change of the tree, checked by a compile.
	actor := gitops.Actor{Username: "alice", Name: "Alice Martin", Email: "alice@example.com"}
	_, _, err = d.UpdateInstance(ctx, actor, "demo", "legacy", func(i *treeedit.Instance) error {
		i.Values["database"] = "nope"
		return nil
	})
	fmt.Println(err != nil)
	_, err = d.DeleteConnection(ctx, actor, "demo", "ext-db")
	fmt.Println(err)

	// What okdp-gitops compile does: the output, from the tree, but the
	// projects whose output is kept, and why.
	var kept []compiler.KeptProject
	_, err = store.Update(ctx, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error {
		var err error
		kept, err = d.WriteCompiled(ctx, tx)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, c := range store.Commits {
		fmt.Println(c.Subject)
	}
	fmt.Println(len(kept))
}
```

```text
data-postgresql provisions demo.analytics for demo/reports
data-postgresql provisions demo.app.database for demo/app
true
connection ext-db is still used by legacy
okdp: compile
0
```

- `Store.Update` runs a change on the latest revision and commits it, a
  change that changes nothing committing nothing; `GitStore` replays it
  on the new revision when another writer pushed in between (up to 5
  times, then `ErrConflict`). `GitStore.UpdateAt` commits on a given
  revision only, else fails with `ErrMoved`: what `okdp-gitops compile`
  commits with.
- Reads run at once, none waiting on another nor on a write
  (concurrency). `Store.View` reads a snapshot: of `GitStore`, the tree
  of the branch's last commit read into memory, within the bounds of a
  tree (a file over `MaxTreeFileSize`, or past `MaxTreeSize` together, is
  listed and not read: reading it fails with `ErrTooLarge`), and under
  the paths of `GitOptions.Layout` when set (`GitStore.Snapshot`, a
  `Snapshot`, its `Revision` the commit), immutable, which a fetch or a
  push replaces and never changes; no lock is held while `fn` runs. A
  snapshot older than `RefreshInterval` is fetched again by one reader,
  the others reading the one it replaces; only the very first read of a
  store waits on its fetch. Writes are one at a time per store, CAS on
  the revision fetched and replayed as before. `MemoryStore` reads the
  files as they were when the view started. A view of `Repositories`
  keeps the snapshot of each repository it read first (`Snapshotter`):
  one revision of each for the whole view, a compile included.
  `Compiler.Compile` runs on a copy of the tree, no lock of a store
  held; compiles of the same files and catalog revisions at once are
  one compile, which they share, then kept (8 trees), but a tree
  compiled without the chart checks (a chart that could not be fetched:
  `Charts.Failures` of its build, logged per chart and per instance
  key). Each compile gets its own `deploy.NewCharts` of
  `Compiler.Charts`, a `deploy.ChartSource`.
- `NewRepositories(topology, open)` routes the paths of the tree to the
  store of each part's repository, `open` giving the store of a location
  ([the control plane's commits](specs/04_topology.md#the-control-planes-commits)).
  `NewDeployments` takes the topology of a `Repositories`.
  `Topology.Project(name, entry)` is the location of a project, an
  error for an entry of `projects.yaml` out of the topology
  (`Topology.CheckEntry`: another host than the templates', a local
  directory or `file://` in a topology of Git repositories, a branch
  Git refuses, a path out of the repository).
- `NewMemoryStore(files)` refuses a path out of the store with an error
  (files read from a repository); `MustMemoryStore(files)` panics on
  one, for the files a program or a test writes itself.
  `catalog.Loader` refuses a URL of `catalogs.yaml` that reads the
  local file system: a local directory always (its `Dirs` read one,
  bounded), a `file://` repository unless `FileURLs`, which the
  control plane never sets.
- `Credentials`, built once, give every credential a program sends:
  `NewCredentials(CredentialsConfig{Dir, Token, InsecureIgnoreHostKey,
  InsecureHTTPHosts})` (the control plane's: the directory of its
  credentials Secret, the hosts of `InsecureHTTPHosts(raw)`, the list
  of `GITOPS_INSECURE_HTTP_HOSTS`), `CredentialsFromEnv()` (the
  compile's); `For(url)`, a repository's, for the `GitOptions.Auth` of
  the store `open` gives; `Catalogs(platformURL)`, the `catalog.Loader`
  `Auth`, the credentials of the platform's host only;
  `NewForgejoClient(config, creds)`, the token of the Forgejo API. Each
  refuses, with an error, a credential for an `http://` URL whose host
  `InsecureHTTPHosts` does not allow (`CheckInsecureHTTP`); a nil
  `*Credentials` is anonymous and allows none. `CABundle`, PEM
  certificates every TLS connection trusts beside the system's (a
  platform's CA; a bundle of no certificate refused): `Credentials.CABundle()`
  for `GitOptions.CABundle` and `catalog.Loader.CABundle` (go-git's
  fetches, pushes and listings), `Credentials.RootCAs()`, the pool of
  `NewForgejoClient`, and of `NewChartPuller(creds, insecureHosts,
  privateHosts)`, the `chartcache.RepositoryPuller` of a chart cache
  (`okdp-gitops compile --charts`, the control plane's): plain HTTP to
  the hosts of `insecureHosts`, a private address allowed to those of
  `privateHosts` (`ChartHosts(raw)`, the list of
  `GITOPS_PRIVATE_CHART_HOSTS`, `PrivateChartHostsEnv`), an entry that
  is not a host, `host:port` nor `*.<domain>` refused;
  `OutputWorkflowWith(topology, WorkflowOptions{InsecureHTTPHosts,
  PrivateChartHosts})`, the workflow whose job carries both. They replace
  `AuthFromDir`, `AuthFromEnv`, `CatalogAuth`, `CatalogAuthFromEnv` and
  the function `CheckInsecureHTTP`.
- `Compiler.Compile(ctx, reader)` compiles the tree a reader holds
  ([the compile](specs/02_compile.md)): a `*compiler.Tree`
  (`deploy.Build`, `compiler.CompileTree`), each
  `compiler.TreeInstance` with its directory (`Name`), its project
  (`Namespace()`), its release (`Release()`), its package (`Definition`, `Provisions`), its outcome
  (`Outcome`: the result, why it is refused, its claims bound), the
  problem of the graph at its release (`GraphErr`), the claims bound to
  it as a provider (`Provided`, each with its `Claimer`, and what it
  granted, `Granted`); or a `*compiler.LoadError`, the problems of a
  tree that does not load
  (`Problems`, one per line in its message), from `compiler.Build`.
  `Deployments.Tree` compiles the tree as it is.
- `Deployments.WriteCompiled(ctx, tx)` compiles the tree `tx` holds and
  writes its output, project by project
  ([problems](specs/02_compile.md#problems)), `deploy.TreeFiles`: the
  projects whose output it keeps as it was, each a
  `compiler.KeptProject` (`Namespace`, `Problems`, `Instances`); an
  error, and
  nothing written, for a problem of the whole tree.
- `Deployments.Apply(ctx, actor, edit)` commits a `treeedit.Edit`
  ([what a write checks](specs/02_compile.md#what-a-write-of-the-control-plane-checks)):
  in a transaction of the tree at the latest revisions,
  `treeedit.Apply`, the edit's change, the tree compiled before and
  after it (`Compiler.Compile`; unchecked without a compiler), the
  verdict of its `Check`, the warnings of the instances it writes
  logged; then the change committed to the repositories of its paths,
  one commit each, `okdp: <action> <target> by <user>` (`NewCommit`,
  the edit's `Action` and `Target`), authored by the `Actor` when it
  has an email, with a `Co-Authored-By` trailer naming the program and
  its version (`gitops.Program`, which a program embedding the library
  sets to its own). A repository that moved between the edit's reads and
  its commit makes the edit run again on the new revisions (up to 5
  times, then `ErrConflict`), never written over a newer version of the
  file; one that changes nothing commits nothing.
- A method of `Deployments` per edit of `treeedit`, `Apply` of it:
  `CreateInstance`, `UpdateInstance` (its change may run more than once,
  when a repository moved and the edit runs again; the instance written),
  `DeleteInstance`; `PutConnection`, `DeleteConnection` (of a project, of
  the platform for the project `""`, a `compiler.Connection`); `PutClaim`,
  `DeleteClaim` (a `compiler.ClaimSpec`); `CreateProject`,
  `UpdateProject`, `DeleteProject` (in a split topology, `projects.yaml`
  edited too); `UpdateCatalog` (a file the compile would refuse, refused:
  `Compiler.CheckCatalogs`, its catalogs fetched, its menu
  `treeedit.CheckCatalogs`; the refusal a `*treeedit.CompileError` of the
  tree, its problems `compiler.FieldErrors`', a catalog that cannot be
  fetched another error). `CreateProject` refuses first, a
  `*treeedit.CompileError` before anything is created (the repository of a
  split topology, `project.yaml`), a name the compile would refuse every
  instance of the project with, `CheckProjectName`
  (`treeedit.CheckProjectName`, `platform.yaml` as the tree holds it); a
  program that creates more of a project, its Namespace, calls
  `CheckProjectName` before it. In a split topology, it creates the
  project's repository first, named after the topology's projects url
  (`Topology.ProjectRepository`: `okdp-project-{project}.git`, the
  repository `okdp-project-<project>`), in the `forgejo` owner.
- The reads, `treeedit`'s readers on a view of the tree:
  `ListProjects`, `GetProject`, `ListInstances`, `GetInstance`,
  `ListConnections`, `GetConnection`, `ListClaims`,
  `ReadPlatformValues`, `ReadCatalog`, `Catalogs` (the catalogs of
  `catalogs.yaml`, fetched); of the output, `ReadCompiled` (an
  instance's umbrella `Chart.yaml` and values, `treeedit.ErrNotFound`
  until the CI wrote them) and `ListPublished` (what the instances of a
  project publish, their `okdp.info.yaml`).
- The errors: of `treeedit`, `treeedit.ErrNotFound` (a missing file of a
  store too), `treeedit.ErrExists`, `*treeedit.ErrInUse` (a removal of
  what another instance uses), `*treeedit.ErrReleaseTaken` (a release
  another instance has), `*treeedit.CompileError` (an edit the compile
  refuses, its fault; a project's name, `Instance` empty, the compiler's
  message alone); okdp-compiler's, `*compiler.LoadError` (every problem of
  a tree); of `gitops`, `ErrNoForgejo`, `ErrConflict`, `ErrMoved`,
  `ErrSymlink`.

## Troubleshooting

The messages about a package or a compile are okdp-compiler's: a file that
does not load names its path (`projects/demo/instances/web/instance.yaml:
...`), an instance refused is reported as `<project>/<instance>:
<message>`, their causes and fixes in its
[troubleshooting](https://github.com/OKDP/okdp-compiler/blob/main/docs/01_usage.md#troubleshooting).
okdp-gitops' own:

| Message | Cause | Fix |
|---|---|---|
| `okdp-gitops: the compiled files are not up to date: run okdp-gitops compile and commit the result` | `--check`: the output is not the compile of the tree, the `stale` and `orphan` lines before it say which files | run `okdp-gitops compile`, commit the output |
| `service "webapp" is in the catalog of no repository` | `instance.yaml` names a service no catalog has, or `catalogs.yaml` names no repository | fix `service`; name the catalogs ([catalogs.yaml](specs/01_layout.md#catalogsyaml)) |
| `version "2.0.0" of service "webapp" is not in test-charts/catalog/webapp` | a version the catalog does not have, at its ref | fix `version`; check the catalog's `ref`, or `--catalog` |
| `compiled values don't meet the specifications of the schema(s) in the following chart(s):` then `app (app-1.8.0):` and `- at '/database': got object, want string` | `--charts`: the values, over the chart's defaults, violate its `values.schema.json` | fix the instance's parameters or upstream values; else the package |
| `<key>: chart app-1.8.0 is not in the chart cache: helm pull ... --untar -d <chart cache>/...` | `okdp-compile --check --charts`: the cache, read as it is, lacks the chart | run the command, `<chart cache>` the directory of `--charts` |
| `upstream.<key>.<path>: appended to the package's list, not to the default list of ..., which Helm replaces: ...` (warning) | `--charts`: an instance appended to a list whose final list lacks items of the chart's default list | restate them in the package's values, as the warning says |
| `A chart could not be fetched: ...`, `Compiled without the checks against this chart: ...` (warnings); `upstream.<key>: the chart ... could not be fetched` | `--charts`: a chart that cannot be pulled anonymously, a private repository, no network; a plain-http registry not in `$GITOPS_INSECURE_CHART_HOSTS` | the instance's other charts compile; a project that is not privileged giving it upstream values is refused; put the chart in the cache by hand (`helm pull -d <dir>/<repository>/<chart>-<version>/`: the `.tgz` beside the chart, unpacked with `--untar`) |
| `okdp-gitops: catalog platform-charts (https://git.example.org/okdp/platform-charts.git@main): fetching the catalog: authentication required` | a catalog that cannot be fetched: its URL, its credentials (those of `$GITOPS_CREDENTIALS_DIR`, else `$GITOPS_TOKEN`, on the platform repository's host only) | fix the URL or the credentials, or `--catalog platform-charts=<dir>` |
| `... ref v1.2: no such branch, tag or commit`, `commit <hash> is not on a branch or a tag of the repository` | a catalog `ref` the repository does not have | fix `ref` |
| `catalogs.yaml: repositories[1]: two catalogs are named "x"` | two catalogs of one name, or one without `name` or `url` | fix `catalogs.yaml` |
| `catalogs.yaml: categories[2].services[0]: service "redis" is in no catalog (catalog/redis/ of no repository)`, `... .default: ...` | the console's menu names a service no catalog has, or a `default` version its catalog does not have: a problem of the whole tree, nothing written; the control plane's edit of the menu refused | add the catalog to `repositories`, or remove the service from `categories` ([catalogs.yaml](specs/01_layout.md#catalogsyaml)) |
| `release "a-b-c" is in namespaces "a" and "a-b": the HelmReleases of okdp-releases need distinct names` | two instances of two projects with one release (`a-b`/`c`, `a`/`b-c`), or an instance with the release of one the output of a kept project still has: nothing is written | rename one |
| `okdp-gitops: 2 problem(s), the output of data, demo kept as it was:`, among them `demo/app: it depends on data/data-postgresql, of the project data, whose output is kept` | an instance of `demo` depends on an instance of `data`, whose output is kept for a problem of its own, listed with it: `demo` keeps its output too ([problems](specs/02_compile.md#problems)) | fix the problem of `data` |
| `https://git.example.org/okdp/deployments.git: gitops/projects/demo/instances/app/config.yaml: a symbolic link, which the deployments repository must not hold` | a symbolic link of a repository of the topology, or of a local directory of it, that the tree reaches: the path in the repository is, or goes through, the link; the tree holds regular files only ([the tree](specs/01_layout.md)) | replace the link by the file |
| `projects/demo/instances/app/config.yaml is 4194305 bytes, more than the 4194304 a file of the tree may weigh`, `the tree holds more than 67108864 bytes` | a file of the tree over 4 MiB, a tree over 64 MiB | the tree is values files: move what is not one out of it |
| `catalog x (../catalog@main): the url "../catalog" reads the local file system: a catalog of catalogs.yaml is a Git repository, a local checkout the operator's (okdp-gitops compile --catalog x=DIR)` | a catalog URL of `catalogs.yaml` that is a local directory, or a `file://` repository | a Git URL; for a local checkout, `--catalog x=<dir>` ([--catalog](#--catalog)) |
| `project lab: projects.yaml: url "https://evil.example/lab.git": its host evil.example is not one of the topology's (...)`, `... path "../outside" goes up (..)` | an entry of `projects.yaml` out of the topology: another host than the templates', a local directory in a topology of Git repositories, a path out of its repository ([io and split](specs/04_topology.md#io-and-split)) | fix the entry |
| `refusing to send the credentials of http://git.example.org/okdp/okdp-output.git over http://, in clear: its host git.example.org is not in $GITOPS_INSECURE_HTTP_HOSTS ...` | a credential (`$GITOPS_TOKEN`, `$GITOPS_CREDENTIALS_DIR`, a password in the URL, the `forgejo` API token) for an `http://` repository, catalog or Forgejo server; `okdp-gitops workflow`: `... would go over http://, in clear, to <host>` | use `https://`; else, a host of a network of its own (a cluster's services), allow it: `GITOPS_INSECURE_HTTP_HOSTS='*.svc.cluster.local'` |
| `okdp-gitops: timed out after 10m0s (--timeout): ...` | a repository, a catalog or a chart registry that does not answer, or a run longer than its bound | check the URLs and the network; else a larger `--timeout` |
| `okdp-gitops: output.url "<url>" is the repository of the platform: the output is in a repository of its own, which the CI alone writes, never in one of the sources` | a topology whose output is in a repository of the sources (the former mono topology, the three parts in one repository) | move `compiled/` and `argocd/` to a repository of their own, which the CI's bot alone writes, and point the engines at it ([io and split](specs/04_topology.md#io-and-split)) |
| `okdp-gitops: --root: the sources and the output are never one directory: --tree DIR --output DIR, or --config FILE` | `--root`, which compiled a tree and its output in one directory, is gone | `--tree <dir> --output <another dir>`, the output moved there |
| `okdp-gitops: workflow --for project: a repository of the sources holds no workflow and no token: ...` | a workflow asked for a repository of the sources | none there: remove the workflow and the secret `OKDP_GITOPS_TOKEN` of every repository of the sources; `--for output` in the output repository ([the CI of the output](#the-ci-of-the-output)) |
