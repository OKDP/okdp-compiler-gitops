# The compile

What okdp-gitops gives okdp-compiler and what it keeps of the answer. How
an instance compiles, its context, its claims, what it publishes and the
deployment graph are okdp-compiler's
([inputs and outputs](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/01_inputs_outputs.md)).
`gitops.Compiler.Compile` is the compile of the [tree](01_layout.md) a
store holds, the same for `okdp-gitops compile` and for the control
plane.

## The steps

1. **The catalogs.** The repositories of `catalogs.yaml`, each fetched at
   its ref, that ref only and its last commit (`catalog.Loader`: kept in
   memory, the files of the last commit of each ref only, a clone that
   grew past 512 MiB replaced by a new one, a symbolic link left out as a
   local directory's, fetched again after 30 seconds; a commit by its hash, or,
   from a server that serves none, with every branch and tag; a local
   directory read as it is; `--catalog NAME=DIR` in its place). A
   catalog that cannot be fetched fails the compile: it is the
   platform's problem, not the tree's.
2. **The tree.** `platform.yaml` and every file under `projects/`, read
   from the parts of the topology at their paths: a file of 4 MiB at
   most, 64 MiB together, no symbolic link ([the tree](01_layout.md)).
3. **The load.** `deploy.Build`, `compiler.Build` with the
   compiler's mandatory [rule
   set](https://github.com/OKDP/okdp-compiler/blob/main/docs/specs/05_rules.md),
   Helm's merge and the [policy](05_rules.md), of the tree and the
   catalogs, each a `compiler.Repository`
   (`compiler.LoadRepository`, then the compile):
   every instance, its package, its context (`platform.yaml`,
   `project.yaml`, `connections.yaml`), its values (`config.yaml`), its
   release `<project>-<instance>` and namespace. Any problem of the load
   (a file that does not parse, a package or a version no catalog has, a
   part in two repositories) is a `*compiler.LoadError`, okdp-compiler's,
   listing every one, each naming its file: the tree
   compiles no instance.
4. **The charts.** With a chart source (`--charts`, the control plane's
   chart cache: `deploy.BuildOptions.Charts`), the upstream charts of
   every instance that loaded, those its `Chart.yaml` names, are fetched
   before any instance compiles (`compiler.BuildOptions.Loaded`), each
   once for the build (`deploy.Charts`): no fetch counts against an
   instance's timeout. okdp-compiler reads no upstream chart: the checks
   against them are okdp-gitops'. A chart that cannot be fetched is
   recorded (`Charts.Failures`), and the instances naming it compile
   without the checks of that chart, the others checked, with a warning
   per chart and per instance key; the control plane does not keep such
   a tree, the next compile of the same files tries the charts again.
5. **The compile.** `compiler.CompileTree` of every instance, each claim
   bound to its provider, of any namespace, each instance after those it
   depends on, with what they publish; each instance's CEL cost bounded
   (1 000 000, `gitops.DefaultCostLimit`). An instance refused is the
   tree's answer, its outcome's error. Its final values, merged by
   okdp-compiler as Helm merges them (`compiler.FinalValues`), are
   checked by okdp-gitops against the chart of each key
   (`helmvalues.New`, `internal/chartcheck`): coalesced over the chart's
   `values.yaml` by Helm's `chartutil.CoalesceValues`, an empty `global`
   added, they must meet the chart's `values.schema.json` (the
   violations of every key reported together, the chart's own files
   only, not its subcharts'; a `$ref` to a URL or a file never fetched);
   a list the instance appended to that lacks items of the chart's
   default list is a warning. Then the platform's policy. An instance of
   a project that is not privileged that gives upstream values to a
   chart that could not be fetched is refused, alone, by a compile that
   renders (`helmvalues.CheckRendered`): nothing can check what it would
   deploy.
   A chart key given upstream values, of such a project, is rendered with
   its final values and its manifests checked (the render, [the
   policy](05_rules.md#the-render)); the rendered output is never written.
   The CI's compile renders, always; the control plane's checks do not
   (`Compiler.SkipRender`, `deploy.BuildOptions.SkipRender`).
   Each instance's compile is bounded (`--instance-timeout`, the CI's
   `gitops.DefaultInstanceTimeout`, one minute), its checks and its
   render included, the fetch of its charts not.
6. **The graph.** `CompileTree`'s, of the instances that compiled: a
   release waits for those it depends on, of its namespace or, for a
   claim, of its provider's. A problem of the graph (a dependency on a
   release that does not provide what is referenced, a cycle) is the
   release's.

The result is a `compiler.Tree`, okdp-compiler's (`compiler.CompileTree`):
each instance (`compiler.TreeInstance`: its directory, its release, its
package, its outcome, the problem of the graph at its release, the claims
bound to it as a provider), and the graph. `Tree.Problems` lists why each
instance that does not deploy does not, as `<project>/<instance>: <why>`.
The same files and catalog revisions give the tree compiled last (8 kept),
without compiling again.

## Problems

`okdp-gitops compile` writes the output project by project
(`Deployments.WriteCompiled`, `deploy.TreeFiles`), the rule
okdp-compiler's, `compiler.Partial`, the same as `okdp-compile
--partial`: a project whose instances all compile is written. A project
of an instance refused, or of a problem of the graph of its own at its
release (a cycle, a release that does not provide what is referenced),
keeps its output as it was, the files the output repository has of it
(none for a project never compiled); so does a project of an instance
that depends on a release of a kept project, a connection reference or
a claim, round after round: it was compiled with what the other
publishes now, which is not deployed. The run fails, status 1, every
problem reported, one per line, then a line per kept project:

```text
okdp-gitops: 2 problem(s), the output of data, demo kept as it was:
data/broken: database: "nowhere" is not a connection, nor an output of an instance of the namespace
demo/app: it depends on data/data-postgresql, of the project data, whose output is kept
okdp-gitops: data: kept since 30ad2cc7f4f677838d1b1435a3359dfb95b12a32 of https://git.example.org/okdp/okdp-sources.git#main (sources.yaml), 3 file(s) re-derived
okdp-gitops: demo: kept since 30ad2cc7f4f677838d1b1435a3359dfb95b12a32 of https://git.example.org/okdp/okdp-sources.git#main (sources.yaml), 11 file(s) re-derived
```

A kept project's line says which revision of the sources its output is
the compile of: the revision of the repository that holds the project
in the output's [sources.yaml](03_output.md#sourcesyaml) as the run read
it, which the last compile that wrote the project's output recorded (a
compile that keeps a project leaves the record as it was); `kept, no
revision recorded in sources.yaml` without one (a local directory, a
repository new since). Its re-derived files are those the targets
write of the project all the same, of today's settings
(`compiler.KeptProject.Rederived`: its manifests, its instances'
identity), the rest of its output kept.

The engines keep deploying the last output of the kept projects, and
the new output of the others. A problem of the whole tree writes
nothing, any project's:

- a file that does not load (`platform.yaml`, a contract, the file of a
  project or of an instance, a package or a version no catalog has,
  `catalogs.yaml` whose menu names a service of no catalog), every one
  reported (`okdp-gitops: <n> problem(s):`): okdp-compiler loads no
  instance of a tree that does not load;
- a catalog that cannot be read;
- a release of the output a kept project keeps that a project written
  has too, or that two kept outputs hold (`release "a-b-c" is in
  namespaces "a" and "a-b": ...`).

A release name two projects of the tree share (project `a-b` instance
`c`, project `a` instance `b-c`) keeps those two projects, and those
that depend on them, each instance's problem the release's (`a/b-c:
release "a-b-c" is in namespaces "a" and "a-b": ...`); the others are
written.

The provider of a claim is compiled with the claims of the whole tree,
those of a kept project among them, and written when its project is: a
claim a kept project adds is provisioned before its claimer deploys; one
it removes, or one of an instance whose claims do not bind (a class
that does not exist, a provider that refuses the project), is no longer
provisioned, while the kept output still uses it. A change of the claims
of a project is best committed apart from a change that may break it.

The compile's messages are okdp-compiler's, their causes and fixes in its
[troubleshooting](https://github.com/OKDP/okdp-compiler/blob/main/docs/01_usage.md#troubleshooting),
but those of the checks against the upstream charts, okdp-gitops'
([troubleshooting](../01_usage.md#troubleshooting)).

## What a write of the control plane checks

Each edit of the control plane (an instance, a connection, a named
claim, a project) is a `treeedit` edit, its check too
(`Check.Verify`); its transaction (`gitops.Deployments.Apply`,
`treeedit.Apply`) compiles the tree before its change and after it, and
is refused, nothing committed, when the change adds a problem to the
tree:

| After the change | Refused with |
|---|---|
| the instance written does not compile, or the tree does not load | `*treeedit.CompileError`: `instance <p>/<i> does not compile: <why>` |
| an instance that compiled before does not | `*treeedit.CompileError` of that instance; for a removal (an instance, a connection, a named claim, a project), `*treeedit.ErrInUse` naming every such instance: what is removed is still used |
| the tree does not load, and it did, or with a problem it did not have | `*treeedit.CompileError`: `the deployments do not compile: <the new problems>` |

A tree that did not load before the change has no instance that compiled:
a change that leaves its problems as they were, and adds none, is
accepted, so that the fix of a broken file, or the removal of what broke
it, goes through. A new instance that compiles also fails when its
release is another instance's, of another project
(`*treeedit.ErrReleaseTaken`):
the Flux and Argo CD targets refuse it (`deploy.CheckReleases`). The
compile is that
of the whole tree, the instances of every project with the providers of
their claims: a change of a project, a named claim or a class, is checked
against every instance it reaches.
