# The policy

A compile applies okdp-compiler's mandatory rule set with the security
set of okdp-compiler-security added: every table, check, schema,
function and document is in their references, [the mandatory
set](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md)
and [the security
set](https://github.com/OKDP/okdp-compiler-security/blob/main/docs/specs/04_rules.md).
okdp-gitops holds no rule of the rule sets. It holds those of the
origins of the values, which the compiler, given flat values layers,
does not know: what each file of the tree may set ([the
levels](01_layout.md#the-levels)), enforced as the tree loads. It wires
what runs in its own Helm merge, the security set's [upstream
policy](https://github.com/OKDP/okdp-compiler-security/blob/main/docs/specs/05_upstream_policy.md)
(its package `policy`), and the render of the upstream charts, through
`Inputs.Finalize`, and the upstream policy on the globals of the files
it does not review, a project's and an instance's, through
`tree.BuildOptions.CheckGlobals`, which `deploy.Build` sets.

- [Who applies it](#who-applies-it)
- [The render](#the-render)

## Who applies it

`deploy.Build` gives `rules.Standard()`, the compiler's mandatory set
with the security set added (`security.Rules()`), to every compile
unless the caller sets `BuildOptions.Rules`, and wires the upstream
policy (`policy.UpstreamCheck` in its `Inputs.Finalize`,
`policy.CheckGlobals` as its `BuildOptions.CheckGlobals`, called on the
`global` of each `project.yaml` and `config.yaml` as the tree loads,
`policy.PlainUpstream` for the warnings) and the render.
`okdp-compile`, `okdp-gitops compile` and the control plane all go
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
| `internal/target`, `internal/target/argocd` | they write the groups the tree gives (`tree.Groups` of `Inputs.Settings`, `target.ProjectGroups`), and read no rule set themselves |

The control plane offers no choice of rule set. It compiles with
`rules.Standard()` and gives that same set to `CheckConnection` and
`deploy.ProjectAccess`.

## The render

For a project that is not privileged, the chart of each key the instance
gives upstream values to is rendered with its final values, its release
and its namespace (Helm SDK, `internal/render`, no cluster client, no
`lookup`), hooks and `crds/` included, `NOTES.txt` left out, and the
manifests' objects are checked as the compile's own are: check `object`,
then the hook `objects`, the security set's hosts among it
(`compiler.CheckObjects`, given the platform's settings). The charts are
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
