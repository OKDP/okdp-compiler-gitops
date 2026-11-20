# Setting up a deployments repository

From empty repositories to instances deployed by Flux or Argo CD, their
output compiled by the CI of the output repository: the sources in one
repository first (io), then split. Each step links to the reference of
what it writes. The packages are those of the repository's test catalog,
`testdata/catalog`: `postgresql`, a database server that provisions the
databases claimed of it, and `app`, which claims one.

## 1. The repositories

Two Git repositories at least: the sources,
`https://git.example.org/okdp/deployments.git` below, which people and
the control plane write, and the output,
`https://git.example.org/okdp/okdp-output.git`, which the CI alone
writes and the engines read ([io and split](specs/04_topology.md#io-and-split)).
The sources hold the tree at their root, or under a directory: `gitops/`,
for a repository that holds other things too. The paths below are the
tree's, under that directory ([the tree](specs/01_layout.md)).

```text
platform.yaml                   # the sources
catalogs.yaml
projects/<project>/project.yaml
projects/<project>/instances/<instance>/{instance.yaml,config.yaml}
argocd/root.yaml                # the output: Argo CD only, its entry point
compiled/                       # the output: generated
```

Everyone who writes the sources is trusted with every project, and with
the platform's settings: io is for a small team. Give `platform.yaml`
and `catalogs.yaml` a repository of their own, written by the platform
admins only, to stop the team's write access at `projects/`
([the platform apart](specs/04_topology.md#the-platform-apart)); split
the projects when each team should write its own
([splitting the sources](#8-splitting-the-sources)).

## 2. The platform

The platform's dependencies, which okdp-compiler does not compile, are
installed first, with `helm` and their few objects: the engine (Flux or
Argo CD), Keycloak, an S3 store, the CloudNativePG operator,
external-secrets and its ClusterSecretStore, Forgejo.

`platform.yaml`: what the platform gives every instance, `global.okdp`
first, the settings the packages require (`require` in their
`okdp.yaml`): the ingress, the OIDC provider and its mode, the secret
store, the classes of the claims and their default; and the platform's
connections, those of its dependencies
([fields the compiler reads](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#fields-the-compiler-reads),
the security set's
[settings](https://github.com/OKDP/okdp-compiler-security/blob/main/docs/specs/01_inputs_outputs.md#platform-settings),
[platform.yaml](specs/01_layout.md#platformyaml)). The ingress and the
OIDC provider are settings of the security set, which okdp-gitops gives
every compile; `ruleSets` requires it, so that a compile without it is
refused rather than leaving the hosts unchecked:

```yaml
global:
  okdp:
    ruleSets:
      - {name: security, version: "^1.0.0"}
    ingress:
      suffix: okdp.example.org
    secrets:
      store: okdp-secrets
    provisioning:
      database-server:
        default: standard
        classes:
          standard: {provider: data/postgresql}
connections:
  lake:
    contract: s3
    endpoint: https://s3.example.org
    bucket: lake
    secretRef: {remoteKey: s3/lake}
```

A package that requires a value the platform does not set does not
compile, and says which
(`global.okdp.ingress.className is required. It is a context value: ...`).

## 3. The catalogs

`catalogs.yaml` names the catalogs, the Git repositories of the packages,
each at a ref ([catalogs.yaml](specs/01_layout.md#catalogsyaml)):

```yaml
repositories:
  - {name: platform-charts, url: https://git.example.org/okdp/platform-charts.git, ref: main}
```

A package is a directory `catalog/<service>/<version>/` of a catalog, its
contracts in `contracts/`
([writing a package](https://github.com/OKDP/okdp-compiler/blob/main/docs/02_writing_package.md)).
Pin a tag, or a commit, to deploy a reviewed catalog; a branch follows its
packages as they are released. While a package is written,
`--catalog platform-charts=<checkout>` compiles with the checkout.

## 4. Projects and their instances

A project is a directory of `projects/` with its `project.yaml`, and a
namespace. The platform's own services are instances of projects too: a
database server in the project `data`, the provider of the platform's
default class (`data/postgresql`, its instance; its release,
`data/data-postgresql`, names it too):

```yaml
# projects/data/project.yaml
description: The platform's databases
```

```yaml
# projects/data/instances/postgresql/instance.yaml
service: postgresql
version: 1.0.0
```

A project of users: its classes beside the platform's, its connections,
its named claims ([levels](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#levels),
[claims](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#claims)):

```yaml
# projects/demo/project.yaml
description: The demo project
connections:
  ext-db:
    contract: database-server
    engine: postgresql
    host: db.example.org
    dbName: ext
    secretRef: {name: ext-db-credentials}
claims:
  analytics:
    contract: database-server
```

The `description` of `project.yaml` is the project's own, which the
console shows: the compiler leaves it out of the context of the
project's instances.

A connection's secret fields are in a Secret of the namespace that
references it (`secretRef: {name}`), which okdp-gitops never writes, or
in the platform's secret store (`secretRef: {remoteKey}`), the compiler
writing the ExternalSecret.

An instance is a directory of `projects/<project>/instances/`, its
`instance.yaml` and its `config.yaml`. An application whose database is
its own, claimed of the provider of the default class (its parameter's
default, `{}`):

```yaml
# projects/demo/instances/app/instance.yaml
service: app
version: 1.0.0
```

and one whose database is the project's named claim `analytics`, or a
connection, by name:

```yaml
# projects/demo/instances/reports/config.yaml
database: analytics
```

The release of `demo-app` waits for `data-postgresql`'s, the provider of
its claim: the deployment graph is the only order.

## 5. Compile

From checkouts of the two repositories, side by side:

```bash
okdp-gitops compile --tree deployments --output okdp-output                                 # the tree read, the output written
okdp-gitops compile --tree deployments --output okdp-output --charts ~/.cache/okdp/charts   # the values checked against the upstream charts
okdp-gitops compile --tree deployments --output okdp-output --check                         # fail when the output is not up to date
```

Every problem is reported at once; the output of a project with a
problem is not written until there is none, nor, for a problem of the
whole tree, anything ([usage](01_usage.md#compile)). Review the
compiled tree, then commit the tree to its repository, and, this once,
the output to its own: `compiled/projects/demo/instances/demo-app/`
holds the umbrella chart, its values, the HelmRelease that waits for
`data-postgresql` ([the output](specs/03_output.md)). From the CI on
([7](#7-the-ci-of-the-output)), nobody but its bot writes the output.

## 6. Deploy with Flux

Flux reads the output only, from the `GitRepository` `okdp-gitops` of
`flux-system`. Install it with Flux, with the namespace `okdp-releases`
and a `Kustomization` of `./compiled/projects`, the compiled tree
([what the engines need](specs/03_output.md#what-the-engines-need)):

```bash
flux get kustomizations
flux get helmreleases -n okdp-releases     # data-postgresql, demo-app, demo-reports...
```

`demo-app` is installed once `data-postgresql` is ready. A private output
repository needs the `GitRepository`'s `secretRef`.

**Mandatory on the controllers**: run helm-controller and
kustomize-controller with `--default-service-account=<a ServiceAccount
with no permission>` and `--no-cross-namespace-refs=true`: without them a
`HelmRelease` that names no identity runs as the controller, and the
confinement of a project stops being the namespace
([what the engines need](specs/03_output.md#what-the-engines-need),
[the deployer's
Role](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md#the-deployers-role)).
The Role of a project's releases lists explicit API groups: a chart of a
custom resource that is not listed needs its group in
`global.okdp.policy.projectApiGroups`.

## 6'. Or with Argo CD

Write `argocd/root.yaml`, in the output's checkout, the Application
`okdp` whose source is the output repository and its revision: from it,
the compile writes an Application per instance. Install beside it the
AppProject of each project, one parent Application per project (an
ApplicationSet over the directories of `compiled/projects`) that applies
that project's generated Applications, never one parent for the whole
tree, whose sync waves would let one tenant block another's, and the
health of an Application in `argocd-cm`, for the sync waves
([what the engines need](specs/03_output.md#what-the-engines-need)).
Compile again: each compiled directory gets its `application.yaml`.

```bash
kubectl get applications -n argocd        # data-postgresql, demo-app, demo-reports...
```

`demo-app` is in the sync wave after `data-postgresql`'s.

## 7. The CI of the output

From now on, nobody compiles by hand: the CI of the output repository
compiles the latest tree every 2 minutes, a person's commits and the
control plane's, and commits the output
([the CI of the output](01_usage.md#the-ci-of-the-output)). The
repository of the sources gets no workflow and no token: only the
output repository holds them, which its bot alone writes.

1. The topology of the compile, a file; the sources in
   `okdp/deployments`, the output in `okdp/okdp-output`, over HTTPS:

   ```yaml
   platform: {url: https://git.example.org/okdp/deployments.git}
   projects: {url: https://git.example.org/okdp/deployments.git, path: "projects/{project}"}
   output:   {url: https://git.example.org/okdp/okdp-output.git}
   ```

2. The workflow, on the default branch of the output repository, the
   topology in it:

   ```bash
   okdp-gitops workflow --for output --config topology.yaml > .forgejo/workflows/okdp-gitops.yaml
   ```

   A topology of `http://` URLs, a Forgejo reached in clear inside a
   cluster, is refused unless their hosts are allowed:
   `GITOPS_INSECURE_HTTP_HOSTS=forgejo-http.forgejo.svc.cluster.local`
   before the command, which the job's environment then carries
   ([workflow](01_usage.md#workflow)). Name the forge's host rather than
   a glob: `*.svc.cluster.local` admits every Service of the cluster.
   The token goes to the hosts of the topology's templates only, an
   entry of `projects.yaml` on another host being refused, a catalog of
   another host read anonymously.
3. A bot, a Forgejo user of its own, the only writer of the output
   repository (its branch protected, the bot alone in its push
   whitelist), with read access to the repository of the sources and to
   the catalogs of its host; the secret `OKDP_GITOPS_TOKEN` of the
   output repository (its settings, Actions, Secrets), a Forgejo access
   token of the bot, of scope `write:repository`. The repository of the
   sources has no secret.
4. A Forgejo runner dedicated to okdp-gitops, registered for the output
   repository only (its settings, Actions, Runners), not for its
   organization, whose every repository's jobs it would run; on its
   docker executor, the label `okdp-gitops` mapped to the okdp-gitops
   image, the image of this repository's `Dockerfile`, and the chart
   cache, a Docker volume `okdp-charts` writable by the image's user,
   65534, the only volume a job may mount. In its `config.yml`:

   ```yaml
   runner:
     labels: ["okdp-gitops:docker://quay.io/okdp/images/okdp-gitops:<version>"]
   container:
     valid_volumes: [okdp-charts]
     docker_host: "-"        # the jobs do not get the Docker socket
     privileged: false
   ```

   and, once, on its Docker daemon:

   ```bash
   docker volume create okdp-charts
   docker run --rm -u 0 --entrypoint chmod -v okdp-charts:/c quay.io/okdp/images/okdp-gitops:<version> 1777 /c
   ```

   Each job runs in a new container of the image, with nothing of the
   runner's: the workflow carries the topology, the secret the token.
   A forge served over https with a CA of your own: add the directory
   of the bundle (`/etc/okdp/ca`, mounted in the daemon) to `valid_volumes`
   and generate the workflow with `GITOPS_RUNNER_CA_FILE=/etc/okdp/ca/ca.crt`
   (the job mounts it read-only and trusts it, `SSL_CERT_FILE`), so that
   the token never goes over `http://`
   ([workflow](01_usage.md#workflow)).

Push a change of the tree: within 2 minutes, a run commits
`okdp: compile` to the output repository, and the engine deploys it
(dispatch the workflow, from the Actions page of the output repository,
not to wait). A run that fails commits the output of the projects
without a problem only, or nothing; its log says why.

## 8. Splitting the sources

A writer of the sources writes every project there, and `platform.yaml`
when it is there. When the platform's settings should be the platform
admins' only, the platform moves to a repository of its own (io, the
platform apart); when each team should write its project's repository
only, each project gets one (split)
([io and split](specs/04_topology.md#io-and-split)):

1. create the repositories; move the files: `platform.yaml` and
   `catalogs.yaml` to the platform's repository, each `projects/<p>/` to
   its project's (split);
2. for split, list the projects in `projects.yaml`, beside
   `platform.yaml`;
3. give the write access: the platform's repository to the platform
   admins, each project's to its team; the output stays the bot's;
4. put the workflow of the new topology in the output repository
   (`okdp-gitops workflow --for output --config topology.yaml`), and give
   the bot read access to the new repositories of the sources; they get
   no workflow, no secret, no runner;
5. for the control plane to create the repositories of new projects, the
   `forgejo` section of its topology.

`okdp-gitops compile --config <topology> --check` compiles the new
topology without writing: the output it would write is the same.

## Checklist

- [ ] The output is in a repository of its own; the writers of the
      sources are trusted with what they can write (io: every project,
      and the platform unless it is apart; split: their project).
- [ ] The platform's dependencies are installed; `platform.yaml` sets
      what the packages require, and declares their connections.
- [ ] `catalogs.yaml` names the catalogs, at the refs to deploy.
- [ ] Each project has its `project.yaml`.
- [ ] The Secrets the connections name exist in their namespaces, or in
      the platform's secret store.
- [ ] Each instance has its `instance.yaml`; releases,
      `<project>-<instance>`, of 53 characters at most, unique.
- [ ] `okdp-gitops compile --tree <sources> --output <output> --check`,
      with `--charts`, passes.
- [ ] The engine reads the output repository: Flux's `GitRepository`
      `okdp-gitops`, or Argo CD's `argocd/root.yaml`, its AppProjects, and
      the health of an Application.
- [ ] The output repository holds the workflow of the topology and the
      secret `OKDP_GITOPS_TOKEN`, its bot the only writer of it, reading
      the sources; the repositories of the sources hold no workflow and
      no secret; the runner, registered for the output repository only,
      maps its label `okdp-gitops` to the okdp-gitops image and lets the
      jobs mount the volume `okdp-charts`.
