# The policy

A compile applies okdp-compiler's mandatory rule set with the security
set of okdp-compiler-security and okdp-gitops' own set added, in that
order: every table, hook, schema, function and document of the first
two is in their references, [the mandatory
set](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md)
and [the security
set](https://github.com/OKDP/okdp-compiler-security/blob/main/docs/specs/04_rules.md),
okdp-gitops' [below](#the-set-of-okdp-gitops). okdp-gitops also holds
the rules of the origins of the values, which the compiler, given flat
values layers, does not know: what each file of the tree may set ([the
levels](01_layout.md#the-levels)), enforced as the tree loads. It wires
what runs in its own Helm merge, the security set's [upstream
policy](https://github.com/OKDP/okdp-compiler-security/blob/main/docs/specs/05_upstream_policy.md)
(its package `policy`), and the render of the upstream charts, through
`Inputs.Finalize`, and the upstream policy on the globals of the files
it does not review, a project's and an instance's, through
`tree.BuildOptions.CheckGlobals`, which `deploy.Build` sets.

- [The set of okdp-gitops](#the-set-of-okdp-gitops)
- [Who applies it](#who-applies-it)
- [The render](#the-render)

## The set of okdp-gitops

`rules/set`, embedded (`rules.FS`), named `gitops`: what the targets
write, which no project may take, and the settings they read. A
platform requires it as it requires the security set
(`global.okdp.ruleSets`, `{name: gitops}`).

| Rule | What it refuses or adds |
|---|---|
| table `engineNamespaces` | `argocd`, `flux-system`, `okdp-releases`: the namespaces of the deployer engines and of the objects the targets write in them (`rules.EngineNamespaces`); extensible |
| hook `namespace`, `engineNamespace` | a project in one of them, privileged or not: its releases, with every verb of the namespace, would drive the engine |
| table `platformObjectNames` | the objects the platform writes in every project's namespace: `okdp-project`, `okdp-control-plane`, `okdp-access-viewer`, `okdp-access-editor`, `okdp-access-admin`, `okdp-secrets`, and the security set's `okdp-dcr-token`; extensible |
| table `platformObjectSettings` | the settings that name such an object: `secrets.store`, `secrets.serviceAccount`, the security set's `oidc.dcr.tokenSecret`; extensible |
| hook `release`, `platformObjectRelease` | a release named like one of them, or its beginning up to a `-`, privileged or not: its objects, `<release>` and `<release>-<x>`, would take their place |
| schema `platform` | `network` (`policy`, `ingressNamespaces`), `controlPlane` (`serviceAccount`), `access.groupPrefix`, `policy.projectApiGroups`: the settings the targets read, which the compiler reads not; `access` and `policy` stay the security set's closed maps |

The mandatory set keeps `platform` and `kube-*`, which no host may
give; the security set the namespaces the platform reserves
(`global.okdp.policy.reserved`). The Secret of the security set's
registration Jobs, `okdp-dcr-token`, is in okdp-gitops' tables, read
after the security set: the security set gives no such table, and
loads alone.

## Who applies it

`deploy.Build` gives `rules.Standard()`, the compiler's mandatory set
with the security set added (`security.Rules()`), to every compile
unless the caller sets `BuildOptions.Rules`, and wires the upstream
policy (`policy.UpstreamCheck` in its `Inputs.Finalize`,
`policy.CheckGlobals` as its `BuildOptions.CheckGlobals`, called on the
`global` of each `project.yaml` and `config.yaml` as the tree loads,
`policy.PlainUpstream` for the warnings) and the render.
`okdp-gitops-compile`, `okdp-gitops compile` and the control plane all go
through it.

The compiler runs the hooks of the security set itself: the hosts of an
instance's objects, and those of the final values, which the compile
checks on what the `Finalize` of `internal/helmvalues` returns, after
the checks against the charts and the render.

A program that calls `tree.Build` or `compiler.Compile` itself
applies the rule sets it gives, their checks and hooks among them, but
neither the upstream policy nor the render below; `tree.Build` and
`tree.Load` apply the levels, `compiler.Compile` none.

A caller's rule set is honoured everywhere the tree is read, not only in
the compile:

| Where | How it gets the rule set |
|---|---|
| `deploy.Build` | `BuildOptions.Rules` (`deploy.BuildOptions` embeds `tree.BuildOptions`); nil, `rules.Standard()` |
| `gitops.Compiler` | its `Rules` field, for its compiles and for `Deployments.CheckProjectName`; nil, `rules.Standard()` |
| `deploy.ProjectAccess`, `treeedit.CheckProjectName` | their first argument; nil, `rules.Standard()` |
| `internal/target`, `internal/target/argocd` | they write the groups the tree gives (`tree.Groups`, `target.ProjectGroups`); the privileged projects of the settings (`policy.Privileged`); the Flux Role leaves out the security set's `deployerApiGroups` of the standard set |

The control plane offers no choice of rule set. It compiles with
`rules.Standard()` and gives that same set to `CheckConnection` and
`deploy.ProjectAccess`.

## The render

For a project that is not privileged, the chart of each key the instance
gives upstream values to is rendered with its final values, its release
and its namespace (Helm SDK, `internal/render`, no cluster client, no
`lookup`), hooks and `crds/` included, `NOTES.txt` left out, and the
manifests' objects are checked as the compile's own are, with the hook
`objects`, the security set's kinds, namespaces and hosts among it
(`compiler.CheckObjects`, given the platform's settings), a credential
in clear excepted: the rendered output is never written, and the
values that would give one are warned of. The charts are
the build's (`deploy.BuildOptions.Charts`, okdp-gitops': okdp-compiler
reads no upstream chart); without them, nothing is rendered. A chart key
without upstream values is the package's, reviewed with it: not rendered.
A chart that could not be fetched refuses the instance alone
(`upstream.<key>: the chart ... could not be fetched`), a chart that
cannot be rendered too. The render sees the values of the chart as the
umbrella renders them; a render past 30 seconds is refused, its goroutine
abandoned. The upstream policy's deny-list stays the early, clear message;
the render catches what no list of keys can name: any key a chart turns
into objects. The rendered output is checked, never written.

The render belongs to the CI's compile, the boundary: `okdp-gitops
compile` and `deploy.Build` render by default. A long-running process
turns it off, `gitops.Compiler.SkipRender`,
`deploy.BuildOptions.SkipRender`, the control plane's checks of its
writes and its reads: Helm's engine takes no context, so a render past
its timeout leaves its goroutine running, and its memory is bounded only
once it returns. Without the render the checks of the values stay; a
chart that could not be fetched then refuses nothing, the values being
the render's only to vouch for. What such a compile accepted, the CI's
refuses.
