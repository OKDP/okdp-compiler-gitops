# The tree

The tree of a platform's deployments is okdp-compiler's
([repository layout](https://github.com/OKDP/okdp-compiler/blob/main/docs/01_usage.md#repository-layout)):
okdp-gitops reads it as the compiler does, adds two files of its own
beside it, and writes the output, `compiled/`. The paths below are paths
of the tree; a [topology](04_topology.md) says which repository holds
each part, and where in it. How the tree compiles is in
[the compile](02_compile.md), the output in [the output](03_output.md).

```text
platform.yaml                          the platform's part: the context of every instance, level 0
catalogs.yaml                          okdp-gitops': the catalog repositories; the console's menu
projects.yaml                          okdp-gitops', split topology only: the projects
projects/<project>/                    a project's part: a namespace
  project.yaml                         the context of its instances, level 1
  instances/<instance>/                an instance: the release <project>-<instance>
    instance.yaml                      {service, version}; claimers, optional
    config.yaml                        optional: its values
    connections.yaml                   optional, rare: its own connections, level 2
compiled/                              the output's part: generated
argocd/root.yaml                       the output's part, optional: Argo CD's entry point
sources.yaml                           the output's part: generated, the revisions it is the compile of
```

What a file of the compiler's holds, and how it is read, is the
compiler's: [levels](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#levels),
[connections](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#connections),
[claims](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#claims).
`compiler.LoadRepository` ignores what it does not know, `projects.yaml`
among them; `catalogs.yaml` it checks
([catalogs.yaml](#catalogsyaml)).

The tree holds regular files and directories only: a symbolic link is
never followed, and a path of the tree that is, or goes through, one, to
anything, fails the compile, the repository and the link named
(`<repository>: <path>: a symbolic link, which the deployments repository
must not hold`). A link beside the tree, in a repository that holds other
things, is no problem. A file of the tree weighs 4 MiB at most, the tree
64 MiB ([security boundaries](../00_architecture.md#security-boundaries)).

## Names

A project, an instance, a connection and a named claim are named by a DNS
label: lowercase letters, digits and `-`, 63 characters at most, starting
and ending with a letter or a digit. The directory is the identity: the
project is the namespace, the instance's release is
`<project>-<instance>` (`compiler.ReleaseName`), 53 characters at most
(Helm's). Two instances of two projects may not have the same release
(project `a-b` instance `c`, project `a` instance `b-c`): their Flux
HelmReleases share one namespace. A compile keeps both projects' output
as it was ([problems](02_compile.md#problems)).

## platform.yaml

The platform: `global.okdp` (the ingress, the OIDC provider, the secret
store, the provisioning classes of the claims and their default),
`global` keys every instance gets, and the platform's connections,
`connections.<name>`, which every instance may reference: an S3 store or
a database the platform uses and does not deploy, its credentials from
the platform's secret store (`secretRef: {remoteKey: <key>}`) or a
Secret of the consuming namespace (`secretRef: {name: <secret>}`). The
platform's dependencies, which okdp-compiler does not compile (Keycloak,
an S3 store, the CloudNativePG operator, external-secrets, Forgejo, the
engine), are installed before it; their connections are declared here.

## catalogs.yaml

The catalog repositories a compile fetches, each a Git repository at a
ref, holding packages (`catalog/<service>/<version>/`) and their contracts
(`contracts/<contract>.schema.json`), parts of the tree the compiler
reads beside the deployments' (`catalog.Repositories`). A catalog is a
repository of role catalog (`compiler.RoleCatalog`): a `platform.yaml` or
a `projects/` in it fails the compile, the deployments' parts are the
topology's alone:

```yaml
repositories:
  - {name: platform-charts, url: https://github.com/OKDP/platform-charts.git, ref: main}
  - {name: sandbox-charts, url: https://github.com/OKDP/sandbox-charts.git, ref: v1.4.0}
categories:            # the console's: its menu of the services, checked by the compile
  - title: Databases
    services:
      - {name: cnpg-postgresql, description: PostgreSQL}
```

| Key | |
|---|---|
| `name` | required, unique: the catalog's name in messages and for `--catalog NAME=DIR` |
| `url` | required: a Git URL; a local directory (an absolute path, `./...`, `../...`) is refused, a `file://` repository too unless the platform's repository is itself local: the file is the platform repository's content, a local checkout the operator's (`--catalog NAME=DIR`) |
| `ref` | a branch, a tag or a commit; `main` by default |

A service is in one catalog; a contract in several holds the same bytes
in each. A catalog on the host of the platform's repository is fetched
with its credentials, one of another host anonymously
([Git repositories](04_topology.md#git-repositories)). No file, or no
repository in it: no catalog, and every instance refused.

The file is okdp-compiler's to check
([repository layout](https://github.com/OKDP/okdp-compiler/blob/main/docs/01_usage.md#repository-layout)),
and okdp-gitops gives it to the compile with `platform.yaml`: read
strictly, no other key at any level; each service of `categories` one
of the catalogs', listed once, its `default` a version of it. A menu
the compiler refuses is a problem of the whole tree, nothing written
(`catalogs.yaml: categories[2].services[0]: service "redis" is in no
catalog (catalog/redis/ of no repository)`); the control plane's edit
of the file is refused the same way, its catalogs fetched
(`Compiler.CheckCatalogs`, `treeedit.CheckCatalogs`).

## projects.yaml

The projects of a split topology, a repository each
([io and split](04_topology.md#io-and-split)):

```yaml
projects:
  - name: demo
  - name: lab                                   # overrides the template
    url: https://git.example.org/data/lab.git
    branch: okdp
    path: deployments
```

A project of a split topology is listed there, or it is not one. In the
other topologies, the projects are the directories of `projects/`. An
entry's `url`, `branch` and `path` stay in the topology: the hosts of
its templates, no local directory in a topology of Git repositories, no
path out of the repository
([io and split](04_topology.md#io-and-split)).

## What the control plane writes

The control plane's edits write the tree as a person would. Package
`treeedit` owns what each writes and its check; okdp-gitops runs it in a
transaction (`gitops.Deployments`), checked by a compile of the whole tree
before and after it ([what a write
checks](02_compile.md#what-a-write-of-the-control-plane-checks)), a commit
each ([the control plane's
commits](04_topology.md#the-control-planes-commits)):

| Operation | Writes |
|---|---|
| a project | `projects/<p>/project.yaml`; its description is its key `description`, first when new, the project's own: the compiler leaves it out of the context of the project's instances (`compiler.ProjectDescription`). In a split topology, the project's repository first (Forgejo, named after the projects url, without a workflow), then its entry of `projects.yaml` |
| an instance | `instance.yaml` (`service`, `version`, and `claimers` as read: absent, `[]` or listed, `treeedit.Instance.Claimers`) and `config.yaml` (its values; none when it has none), each only when its content changes, so that a hand-written file keeps its comments |
| a connection | the entry `connections.<name>` of `project.yaml`, or of `platform.yaml` for a platform connection, as okdp-compiler writes a `compiler.Connection`: `contract`, `description`, `secretRef`, then the contract's fields, sorted |
| a named claim | the entry `claims.<name>` of `project.yaml`, a `compiler.ClaimSpec`: `contract`, `class` or `provider`, `request` |
| the catalog list | `catalogs.yaml`, key by key: the keys that do not change keep their nodes and their comments; a file the compile would refuse is not written ([catalogs.yaml](#catalogsyaml)) |

An edit of `project.yaml` or `platform.yaml` keeps the head of the file
(its leading comments, a licence) as written, the other
keys, and the comments of the entries it replaces. A project is a
directory of `projects/` with a `project.yaml`, whoever wrote it.
Deleting a project removes its directory (in a split topology, its entry
of `projects.yaml`; its repository is kept).
