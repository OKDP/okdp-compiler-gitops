# The tree

The tree of a platform's deployments: the platform, the catalog of
packages, the projects and their instances. okdp-gitops reads it
(package `tree`, `tree.Load`), each file the input of the instances it
is for, compiles them together with okdp-compiler (`compiler.CompileAll`)
and writes the output, `compiled/`. The packages and the contracts are
the compiler's to read (`compiler.LoadCatalog`); the rest of the layout
is okdp-gitops'. The paths below are paths of the tree; a
[topology](04_topology.md) says which repository holds each part, and
where in it. How the tree compiles is in [the compile](02_compile.md),
the output in [the output](03_output.md).

```text
platform.yaml                          the platform's part: the values of every instance, level 0
catalogs.yaml                          the catalog repositories; the console's menu
contracts/<contract>.schema.json       a catalog's part: the contracts (Inputs.Contracts)
catalog/<service>/<version>/           a catalog's part: a package (compiler.Definition)
projects.yaml                          okdp-gitops', split topology only: the projects
projects/<project>/                    a project's part: a namespace (Inputs.Namespace)
  project.yaml                         the values of its instances, level 1; its description, its access
  instances/<instance>/                an instance: the release <project>-<instance> (Inputs.Release)
    instance.yaml                      {service, version}; claimers, optional
    config.yaml                        optional: its values (Inputs.Values)
    connections.yaml                   optional, rare: its own connections, level 2
compiled/                              the output's part: generated
argocd/root.yaml                       the output's part, optional: Argo CD's entry point
sources.yaml                           the output's part: generated, the revisions it is the compile of
```

The directory of a project is the namespace of its instances: nothing
else names it, and the engines install there. Each file of the tree is
read for the compile of the instances it is for:

- `instance.yaml` holds `service` and `version`, the instance's package,
  `catalog/<service>/<version>/`, and, optional, `claimers`, nothing
  else: the namespaces whose instances may claim of it, a provider, from
  another namespace, by naming it or through a class, the platform's or
  their project's (`claimers: [demo]`, `["*"]` for any; absent, its
  package's, `provisions`, none by default): a class routes a claim, it
  does not consent for the provider
  ([providers](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#providers)).
  Its `Chart.yaml` must have that `name` and `version`; a released
  version directory is immutable: a change is a new version.
- The values of an instance have a file per level ([the
  levels](#the-levels)): 0 `platform.yaml`, the platform values
  (`global.okdp`, the classes of the claims and their default among it,
  the platform's secret store, the connections every instance may
  reference); 1 `project.yaml`, what a project adds for its instances
  (its settings under `global`, its classes beside the platform's, a
  default class of a contract the platform sets none of, its
  connections, its named claims, `claims: {<name>: {contract:
  <contract>}}`, which its instances reference by name); 2
  `connections.yaml`, the instance's own connections (`connections:
  {<name>: {...}}`), and nothing else: `global` is the platform's or
  the project's. A level adds values and changes none of a lower
  level's. The named claims are a project's only: `platform.yaml`
  declares none
  ([connections](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#connections),
  [claims](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md#claims)).
- `project.yaml` may describe its project, `description: <text>`, a
  string: the project's own, which is no value of its instances, and
  which no instance receives (`tree.ProjectDescription`). It may give
  its people their roles, `access: {<group>: viewer|editor|admin}`, the
  identity provider's groups (`tree.ProjectAccess`): absent or null,
  `okdp-project-<project>` is an editor (`tree.DefaultProjectGroup`);
  `{}`, no group. No other file gives it, and no instance receives it
  as a value: the tree gives it every instance of the project as its
  groups, the security set's `global.okdp.access.groups`, in the
  compiler's host settings, beside the files' layers
  (`tree.GroupsSettings`, `compiler.Inputs.Settings`; a mapping reads
  them in `platform.access`, and no chart receives them: a change of a
  project's access changes no instance's values), and the targets
  write it as the Argo CD roles of the project's
  AppProject and the RoleBindings of its namespace ([the
  output](03_output.md)). Whoever writes `project.yaml` sets it: in a
  repository of its own (the split topology), its writers, the
  project's administrators.
- `config.yaml` is the instance values: its parameters and upstream
  values, of `global` only the keys its package's parameter schema
  declares, no `connections`, no `claims`, no `access` ([the
  levels](#the-levels)). An instance's directory holds
  `instance.yaml`, `config.yaml` and `connections.yaml` only: another
  entry, a `values.yaml`, a directory, a hidden file, would be read by
  no compile, and is refused, each named.
- `tree.Load` ignores what it does not know, `projects.yaml` among
  them; `catalogs.yaml` it checks ([catalogs.yaml](#catalogsyaml)).

The tree may be in several repositories (a `tree.Repository` each, of
the role `tree.RoleTree`): a platform repository with `platform.yaml`, a
repository per project with `projects/<project>/`, or all in one; and
catalogs (`tree.RoleCatalog`), with `catalog/` and `contracts/` only. A
catalog is trusted for its packages, not for the deployment: one that
holds `platform.yaml` or `projects/`, a file or a directory, even
empty, is refused, each path named, rather than read without them, so
that its maintainers add no project, of a privileged name or not, nor
the platform's values. Each repository needs its role: the zero value is
refused. They make one tree, part by part, with no precedence, so that
their order changes nothing:

| Part | Across repositories |
|---|---|
| `platform.yaml`, `catalogs.yaml` | in one at most, each |
| `catalog/<service>/` | in one: every version of a service from it |
| `contracts/<contract>.schema.json` | in several, the same bytes in each: a catalog ships the contracts of its packages |
| `projects/<project>/` | in one: the project's file and its instances |

The problems of the tree's parts are listed before those of the
catalog, each naming its repositories. An instance's package and
contracts come from any repository.

The tree holds regular files and directories only: a symbolic link is
never followed, and a path of the tree that is, or goes through, one, to
anything, fails the compile, the repository and the link named
(`<repository>: <path>: a symbolic link, which the deployments repository
must not hold`). A link beside the tree, in a repository that holds other
things, is no problem. A file of the tree weighs 4 MiB at most, the tree
64 MiB ([security boundaries](../00_architecture.md#security-boundaries)).

## The levels

okdp-compiler takes the values of an instance as flat layers
(`Inputs.Values`), merged in order as `helm -f` merges files, any of
them setting anything. Where each layer comes from is the tree's to
know, and what each may set the tree's to enforce: `tree.Load`, and
every build, the control plane's and the CI's, refuse a file that sets
more than its level may, a problem of the file's owner (the platform's
fails the tree, a project's refuses that project, `tree.Refused`),
before any instance compiles. A project without instances is checked
alike. Then the layers are given to the compiler in their order:
`platform.yaml`, `project.yaml` (without its `description` and its
`access`), the instance's `connections.yaml`, its `config.yaml`; an
absent file gives none. Beside them, the tree's own settings
(`compiler.Inputs.Settings`): the roles of the project's groups, its
`access`, as `global.okdp.access.groups`, which no chart receives.

- A level adds values and changes none a lower level sets, even to the
  same value, a null included: a file of level 1 or 2 refused for a
  value of a lower level names it and the file that sets it. A
  connection (`connections.<name>`), a named claim (`claims.<name>`), a
  class (`global.okdp.provisioning.<contract>.classes.<class>`) and the
  default class of a contract
  (`global.okdp.provisioning.<contract>.default`) are each taken whole:
  a level adds connections, claims and classes beside the lower levels',
  and the classes and default of the contracts they leave out.
- Of `global.okdp`, the platform's settings, a file above level 0 sets
  `provisioning` alone, its classes, whether the platform sets a key or
  not, and nothing else, `global.okdp` itself being a map.
- Of the rest of `global`, which every chart of the instances receives
  past their packages' protect, a file above level 0 sets only the keys
  `platform.yaml` lists in `global.okdp.policy.projectGlobals`
  (absent, `project` and `storageClass`; `[]`, none), each refusal a
  field problem at `global.<key>` (`compiler.FieldErrors`), privileged
  project or not.
- `platform.yaml` declares no named claim; `access` is `project.yaml`'s
  alone, and `platform.yaml` sets no `global.okdp.access.groups`: the
  groups of a namespace are its project's, which the tree gives. The
  compiler would refuse it too, a layer giving a setting the tree's own
  settings give; the tree names the file first.
- Only `platform.yaml` gives template code (`{{`), reviewed with the
  platform: under no key of a project's file nor of an instance's.
- A connection above level 0 whose credentials are in the platform's
  secret store (`secretRef: {remoteKey}`) names a key of its own
  namespace, `<project>/...`: the platform's keys are read through the
  platform's connections. A field problem at `connections.<name>`.
- `config.yaml` sets of `global` only the keys its package's parameter
  schema declares (`properties.global.properties` of
  `config.schema.json`), never `okdp`; no `connections` (an instance's
  own are its `connections.yaml`), no `claims` (its project's), no
  `access`, no template code.
- With `BuildOptions.CheckGlobals` (`deploy.Build`:
  `policy.CheckGlobals`, the security set's upstream policy), the
  `global` of `project.yaml` and of `config.yaml` is checked as the
  upstream values are, with the settings of the project's values: an
  image of a registry the platform does not list, a key of a pod's
  privileges, at any depth, refused at its field, a privileged project
  excepted. `tree.Load` runs no policy.

The compiler checks the rest, whatever layer sets it: the settings
against their schema, the connections against their contracts, the
claims, the namespace.

## Names

A project, an instance, a connection and a named claim are named by a DNS
label: lowercase letters, digits and `-`, 63 characters at most, starting
and ending with a letter or a digit. The directory is the identity: the
project is the namespace, the instance's release is
`<project>-<instance>` (`tree.ReleaseName`), 53 characters at most
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
repository of role catalog (`tree.RoleCatalog`): a `platform.yaml` or
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

The tree checks it with `platform.yaml` (`tree.ParseCatalogs`,
`tree.CheckMenu`): read strictly, no other key at any level (a former
`serviceCatalog` wrapper, a service's `versions` or `repository` are
refused); each service of `categories` one of the catalogs', listed
once, its `default` a version of it. A menu refused is a problem of the
whole tree, nothing written
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
| a project | `projects/<p>/project.yaml`; its description is its key `description`, first when new, the project's own: `tree.Load` leaves it out of the values of the project's instances (`tree.ProjectDescription`). In a split topology, the project's repository first (Forgejo, named after the projects url, without a workflow), then its entry of `projects.yaml` |
| an instance | `instance.yaml` (`service`, `version`, and `claimers` as read: absent, `[]` or listed, `treeedit.Instance.Claimers`) and `config.yaml` (its values; none when it has none), each only when its content changes, so that a hand-written file keeps its comments |
| a connection | the entry `connections.<name>` of `project.yaml`, or of `platform.yaml` for a platform connection, as `treeedit` writes a `compiler.Connection`: `contract`, `description`, `secretRef`, then the contract's fields, sorted |
| a named claim | the entry `claims.<name>` of `project.yaml`, a `compiler.ClaimSpec`: `contract`, `class` or `provider`, `request` |
| the catalog list | `catalogs.yaml`, key by key: the keys that do not change keep their nodes and their comments; a file the compile would refuse is not written ([catalogs.yaml](#catalogsyaml)) |

An edit of `project.yaml` or `platform.yaml` keeps the head of the file
(its leading comments, a licence) as written, the other
keys, and the comments of the entries it replaces. A project is a
directory of `projects/` with a `project.yaml`, whoever wrote it.
Deleting a project removes its directory (in a split topology, its entry
of `projects.yaml`; its repository is kept).
