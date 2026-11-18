# The policy

A compile applies okdp-compiler's rule set, its mandatory set, embedded
in that module: every table, check, schema, function and document is in
[its
reference](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md).
okdp-gitops adds what the compiler does not hold: the upstream and
ingress policy, `internal/policy`, and the render of the upstream charts,
both through `Inputs.Finalize` and `Inputs.CheckGlobals`, which
`deploy.Build` sets.

- [Who applies it](#who-applies-it)
- [The upstream policy](#the-upstream-policy)
- [The render](#the-render)

## Who applies it

`deploy.Build` gives `rules.Standard()`, the compiler's mandatory set, to
every compile unless the caller sets `BuildOptions.Rules`, and the hooks
of the policy and the render. `okdp-compile`, `okdp-gitops compile` and
the control plane all go through it.

A program that calls `compiler.Build` or `compiler.Compile` itself
applies the compiler's mandatory set, its checks among them, but neither
the policy nor the render below.

A caller's rule set is honoured everywhere the tree is read, not only in
the compile:

| Where | How it gets the rule set |
|---|---|
| `deploy.Build` | `BuildOptions.Rules` (`deploy.BuildOptions` embeds `compiler.BuildOptions`); nil, `rules.Standard()` |
| `gitops.Compiler` | its `Rules` field, for its compiles and for `Deployments.CheckProjectName`; nil, `rules.Standard()` |
| `deploy.ProjectAccess`, `treeedit.CheckProjectName` | their first argument; nil, `rules.Standard()` |
| `internal/target`, `internal/target/argocd` | they write the groups `ProjectOf` read with the compile's `Inputs.Rules`, and read no rule set themselves |

The control plane offers no choice of rule set. It compiles with the
compiler's mandatory set and gives that same set to `PlatformAdmins`,
`CheckConnection` and `deploy.ProjectAccess`.

## The upstream policy

`internal/policy` walks the values and calls the checks of the rule set
([checks](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md#checks)).
These semantics are its own.

**Keys.** `UpstreamCheck` runs check `upstreamKey` on every key, in maps
and in lists, at any depth, in key order. A string that holds a `:` and
parses as YAML to a map or a list, in any of its `---` documents, is
walked as values too, because charts render some strings as YAML. The
package's own computed values are not checked, as they are reviewed with
the package.

**Globals.** `CheckGlobals` applies the same check to `global` of every
layer above level 0, values layers included. The failure is a field error
at its path.

**Registries.** With `policy.upstream.registries` set, images must come
from an entry. Absent, images are not checked. `[]` means no image may be
set. In each map of the instance's upstream values:

1. A non-empty string at a key that matches `imageKeySuffixes`, other than
   `image` itself, is a reference.
2. A string or null at `image` is a reference. Null means the chart's
   default, which the compile cannot read, and is refused.
3. A string or null at `registry` or at `repository` makes the reference
   `<registry>/<repository>`, or the repository alone with no registry.
   The half the instance does not set comes from the computed values.
   Under a key `image` with no `repository` on either side, `name` stands
   for `repository`.

A value of another type at these keys is not read as an image. An entry
of `registries` loses a final `/`. A reference is allowed when it starts
with an entry and then ends, or continues with `/`, or, for an entry with
a path, with `:` or `@`. `registry.example.org` therefore does not allow
`registry.example.org:5000/x`.

**Plain values.** `PlainUpstream` warns about a literal at a sensitive
key of the instance's upstream values. It is a warning, not a refusal,
because the key's name is a guess.

## The render

For a project that is not privileged, the chart of each key the instance
gives upstream values to is rendered with its final values, its release
and its namespace (Helm SDK, `internal/render`, no cluster client, no
`lookup`), hooks and `crds/` included, `NOTES.txt` left out, and the
manifests' objects and hosts are checked as the compile's own are, check
`object` and check `host` (`compiler.CheckObjects`,
`compiler.CheckIngressHosts`). The charts are the build's
(`deploy.BuildOptions.Charts`, okdp-gitops': okdp-compiler reads no
upstream chart); without them, nothing is rendered. A chart key without
upstream values is the package's, reviewed with it: not rendered. A
chart that could not be fetched refuses the instance alone
(`upstream.<key>: the chart ... could not be fetched`), a chart that
cannot be rendered too. The render sees the values of the chart as the
umbrella renders them; a render past 30 seconds is refused, its
goroutine abandoned. The deny-list stays the early, clear message; the
render catches what no list of keys can name: any key a chart turns into
objects. The rendered output is checked, never written.

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
