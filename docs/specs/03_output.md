# The output

`compiled/`, what `okdp-gitops compile` writes from the
[tree](01_layout.md) and the GitOps engines deploy: the files of the
Flux and Argo CD targets, as they write them, under `compiled/` of the
output's part.

## The targets

A target (`deploy.Target`) writes compiled instances for a deployer,
given their results and their deployment graph: the files, path ->
content, every one under `projects/`.

| `okdp-gitops-compile --target` | Target | Each instance's files | Besides | Options |
|---|---|---|---|---|
| `helm` (the default) | `deploy.Helm` | `values.yaml`, `okdp-objects.values.yaml`, `okdp.info.yaml`: values, no chart | none | none |
| `umbrella` | `deploy.Umbrella` | `Chart.yaml`, `instance.values.yaml`, `okdp.info.yaml`: one chart, a release of the package's charts and `okdp-objects` | none | none |
| `flux` | `deploy.Flux` | the umbrella, `helmrelease.yaml`, `kustomization.yaml` | each project's `project/` and `kustomization.yaml`, `projects/kustomization.yaml` | `Source`, `flux-system/okdp-gitops` by default; `PathPrefix`; `Keep` |
| `argocd` | `deploy.ArgoCD` | the umbrella, `application.yaml` | each project's `project/` | `RepoURL`, required; `Revision`, `HEAD` by default; `PathPrefix` |

`okdp-gitops compile` writes the Flux target, and the Argo CD target
when the output has `argocd/root.yaml`:

| Target | Written | Options okdp-gitops gives |
|---|---|---|
| Flux | always | `PathPrefix`: `<prefix>/compiled`; the GitRepository of the output is `flux-system/okdp-gitops` |
| Argo CD | when the output has `argocd/root.yaml` | `RepoURL` and `Revision`: the `spec.source` of the Application of `argocd/root.yaml` (`HEAD` without a revision); `PathPrefix`: `<prefix>/compiled` |

`<prefix>` is the path of the output in its repository
([`--path-prefix`](../01_usage.md#--path-prefix)): the topology's output
path by default. `argocd/root.yaml` is Argo CD's entry point, written by
hand: an Application whose source is the output repository; without it,
the output is deployed by Flux only, and has no Applications
(`deploy.ReadArgoRoot`). Two targets that write a file differently fail
the compile (`<path>: two targets write it differently`): every file
both write is the same, so that one tree holds both.

```text
compiled/
  projects/
    kustomization.yaml                 flux: the directories of the projects, what syncs the whole tree
  projects/<project>/
    kustomization.yaml                 flux: the directories of the project's instances, and project
    project/                           the project's own manifests
      namespace.yaml                   the Namespace, with its Pod Security labels
      secret-store.yaml                the platform's secret store in it
      access.yaml                      the RoleBindings of the project's groups
      control-plane.yaml               the control plane's Role and RoleBinding
      network-policy.yaml              the NetworkPolicy of its namespace
      service-account.yaml             flux: the identity of its releases
      kustomization.yaml               flux: these files
      appproject.yaml                  argocd: the AppProject of its Applications
    instances/<project>-<instance>/    an instance: its umbrella (Chart.yaml, instance.values.yaml, okdp.info.yaml)
      charts/<chart>-<version>.tgz     with a chart cache: each chart the compile checked, vendored (below)
      helmrelease.yaml                 flux: its HelmRelease, dependsOn the releases it waits for
      kustomization.yaml               flux: its HelmRelease and its values ConfigMap
      application.yaml                 argocd: its Application, its sync wave after those it waits for
```

The namespace and the release of every file come from the tree's
`projects/<project>/instances/<instance>/`. Each file of the Flux and
Argo CD targets is marked generated on its first line. Their objects in
the engines' namespaces are named after the releases and the projects:
the HelmReleases `<release>` and their ConfigMaps `values-<release>` in
`okdp-releases`, the ServiceAccounts `project-<namespace>` there; the
Applications `<release>` and the AppProjects `project-<namespace>` in
`argocd`. A release is a DNS-1123 label, no dot: the platform's own
objects in these namespaces must take a name with one
(`okdp.instances`), or an instance could replace them.

The HelmRelease is in `okdp-releases`, named after the release;
`targetNamespace` and `storageNamespace` the namespace;
`serviceAccountName: project-<namespace>`, unless the project is one of
the platform's own; the chart of the instance's directory,
`reconcileStrategy: Revision`; `dependsOn` the releases it waits for;
its values from the ConfigMap `values-<release>`, key
`instance.values.yaml`, user values. The `flux` target refuses an
`instance.values.yaml` over the 1 MiB of a ConfigMap: that instance is
refused, its project kept, the others written.

Compiled with a chart cache, each chart of the umbrella is vendored:
`charts/<chart>-<version>.tgz`, the archive the compile pulled and
checked, byte for byte, and its digest in `Chart.yaml`
(`annotations.okdp.io/chart-digests`,
`<name>-<version>=sha256:<hex>,...`), the okdp-objects chart among them.
What an engine honours decides it: a Chart.lock's digest is a hash of the
declarations, not of the chart, a dependency has no digest in `Chart.yaml`
(Helm 3 and 4), and `helm dependency build` fetches by tag: only
`charts/*.tgz` deploys the bytes the compile checked. Argo CD runs `helm
template`, running `helm dependency build` only when a dependency is
missing; Flux's source-controller builds the chart from the Git source
with the `charts/` it finds and fetches only what is missing. A chart
the compile could not fetch, or an archive over 4 MiB (`helm.VendorMaxSize`, a
file of the output must fit the compile's reads; such a chart's digest is
recorded all the same), stays fetched by the engine by tag. A project's
kept output keeps the archives it was last compiled with.

The Application is in `argocd`, named after the release; its sync wave
from the graph; `project: project-<namespace>`; its source the chart of
the instance's directory, released as the release with
`instance.values.yaml`, destined to the namespace; synced automatically,
pruned and self-healed. Argo CD prunes what Helm would keep, unless the
object carries its sync options, `Prune=false,Delete=false`: the objects
Helm keeps do.

`project/` is the project's boundary: its instances are installed with
an identity confined to its namespace, unless the project is one of the
platform's own (`global.okdp.policy.privileged`), whose instances keep
the engine's identity. The platform's entry point applies it before the
instances.

## What a compile rewrites

A compile rebuilds `compiled/` from the tree: what no instance produces
any more is removed (an instance deleted, its release renamed, a project
deleted), every other file written again. A project kept by the compile
([problems](02_compile.md#problems)) keeps its directory,
`compiled/projects/<project>/`, as it is, read without following a
symbolic link, and the root's `projects/kustomization.yaml`, which the
Flux target writes for the projects written and the kept ones whose
`compiled/projects/<project>/kustomization.yaml` the output holds
(`Flux.Keep`, set by `deploy.TreeFiles`), lists it: the engines prune
none of its releases. Kept, but not whole: an instance refused today
whose output in the kept directory today's platform policy refuses on
its own (the value a platform has since denied, a host, an object the
render of its kept values now refuses, with a chart cache only: the
charts `deploy.TreeFiles` is given) is withdrawn: its directory is not
kept, the engines prune it, a problem of the project says so. An instance
refused for what its author just wrote keeps what runs: the check is on
what is deployed, not on the refusal (`output.Files`, `withdraw`). Only
the files whose bytes change are written, or committed: a compile of an up-to-date tree commits
nothing ([the compile's commits](04_topology.md#the-compiles-commits)).
The tree is never written. The control plane writes the tree only,
never the output: the CI of the output compiles their commits, on its
schedule ([the CI of the output](../01_usage.md#the-ci-of-the-output)).

## sources.yaml

Beside `compiled/`, a compile of Git repositories writes `sources.yaml`:
what the output is the compile of. A commit of the sources that changes
no compiled file still changes it, a commit of the output with it alone.

```yaml
# Generated by okdp-gitops. Do not edit.
# The sources this output is the compile of (okdp-gitops compile).
okdp-gitops: 0.7.0
topology: sha256:daef8e1e...
output: sha256:5aaf6776...
repositories:
  - url: https://git.example.org/okdp/okdp-sources.git
    branch: main
    revision: 30ad2cc7f4f677838d1b1435a3359dfb95b12a32
catalogs:
  - name: platform-charts
    url: https://git.example.org/okdp/platform-charts.git
    ref: main
    revision: aeebc5ce174b36bb56daf956e289cb502926507f
```

| Key | |
|---|---|
| `okdp-gitops` | the version of the okdp-gitops that compiled it |
| `topology` | a digest of the topology's locations (URL, branch, path of each part), the path prefix and whether the values were checked against the charts (`--charts`) |
| `output` | a digest of the output's files, `compiled/` and `argocd/`, as the compile left them |
| `repositories` | each repository of the sources read, but the output's: its URL, branch and the commit read (`""` for an empty one) |
| `catalogs` | each catalog of `catalogs.yaml`: its name, URL, ref and the commit read |

A run of `okdp-gitops compile` reads the output first, then lists the
refs of each repository and catalog of the record, without a fetch: when
the version, the digests and every revision are the same, it stops,
`okdp-gitops: up to date: the sources did not move since the compile
sources.yaml records`, before the catalogs, the charts and the compile.
The CI's run every 2 minutes costs, then, the output's fetch and a list
of refs per repository. Anything else compiles: a commit of a source or
a catalog (a branch that moved, a tag moved), another okdp-gitops,
another topology, an output changed by another hand, a build without a
version (`dev`).

A compile that keeps a project leaves the record as it was: the output
it wrote, or the sources, no longer match it, and the next run compiles
again, the problem reported each time, with the revision of the record
the kept output is the compile of
([problems](02_compile.md#problems)). A compile of a local directory,
or with a catalog read from one (`--catalog`), writes no record and
removes the output's: that output is not the compile of revisions.
`--check` neither stops early nor writes it.

## What the engines need

The output repository is the only repository the engines read; their
entry points are installed once, at the platform's bootstrap, with the
engine itself (a dependency of the platform), and are not generated:

- Flux: the `GitRepository` `okdp-gitops` in `flux-system`, of the output
  repository; the namespace `okdp-releases`, where the HelmReleases and
  their values ConfigMaps are; a `Kustomization` of the path
  `./<prefix>/compiled/projects`, which syncs the whole tree.
- Argo CD: `argocd/root.yaml`, an AppProject `project-<project>` per
  project, the health of an Application in `argocd-cm` for the sync
  waves, and one parent Application **per project** that applies its
  generated ones, `<prefix>/compiled/projects/<project>` with
  `directory: {recurse: true, include: 'instances/*/application.yaml'}`:
  an ApplicationSet over the directories `<prefix>/compiled/projects/*`
  (the Git generator), not one Application of the whole tree: a sync wave
  waits for the previous wave of its own parent only, so with one parent
  for the tree, one tenant's Degraded Application would block every later
  wave of every other project.
- Flux, mandatory: helm-controller and kustomize-controller run with
  `--default-service-account=<a ServiceAccount with no permission>` (a
  HelmRelease or Kustomization that names none must not run as the
  controller) and `--no-cross-namespace-refs=true`; an install without
  them leaves a HelmRelease of a project (`okdp-releases`, the
  deployer's Role grants no way to create one) running as the
  controller. The HelmReleases of the Flux target name their
  `GitRepository` in `flux-system` (`Flux.Source`): check on your
  Flux version that `--no-cross-namespace-refs` does not refuse that
  reference, and use `Flux.Source` in `okdp-releases` if it does.

A sync wave orders the Applications one parent Application syncs: a
provider and the projects of its claims in several projects are ordered
by the retries of the Applications waiting for them, one parent per
project. It orders their creation and the changes to them, not a later change of an
instance's chart or values, which its own Application syncs at once;
Flux's `dependsOn` holds at every reconcile. The Argo CD health of an
Application must be configured, or a wave does not wait for the previous
one.

## What the control plane reads

- An instance's compiled files (`Deployments.ReadCompiled`,
  `treeedit.ReadCompiled`): its umbrella `Chart.yaml` and
  `instance.values.yaml`, `treeedit.ErrNotFound` until the CI has
  compiled it.
- What the instances of a project publish (`Deployments.ListPublished`,
  `treeedit.ListPublished`): the `okdp.info.yaml` of each instance of the
  tree whose compiled directory the output has, with the package of the
  `Chart.yaml` beside it.
