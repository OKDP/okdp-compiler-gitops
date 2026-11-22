# okdp-gitops documentation

| Document | For | Content |
|---|---|---|
| [Usage](01_usage.md) | platform engineers, integrators, package authors | install, `okdp-gitops compile` and `okdp-gitops workflow`, `okdp-compile` and its targets, installing with Helm, the catalogs, the chart cache, the CI of the output and its runner, the engines, the Go library, troubleshooting |
| [Setting up a deployments repository](02_setting_up.md) | platform engineers | from an empty repository to deployed instances, step by step: the platform, the catalogs, the projects and their instances, the compile, Flux or Argo CD, the CI, the sources split; a checklist |
| [The tree](specs/01_layout.md) | everyone (reference) | the tree as okdp-gitops reads and writes it: the release rule, okdp-gitops' own files (`catalogs.yaml`, `projects.yaml`), what the control plane's transactions write |
| [The compile](specs/02_compile.md) | everyone (reference) | what okdp-gitops gives okdp-compiler and what it keeps of the answer: the parts and the catalogs, the chart cache and the checks against the upstream charts, the deployment graph, the problems; what a write of the control plane checks |
| [The output](specs/03_output.md) | platform engineers, contributors (reference) | the targets, what each writes; `compiled/`, the files of the Flux and Argo CD targets, their paths, Argo CD's switch, what a compile rewrites; what the engines need |
| [Topologies](specs/04_topology.md) | platform engineers (reference) | where each part lives: the topology file, io and split and who can write what, local directories, the credentials, the compile's commits and the runs that race, the control plane's commits |
| [The policy](specs/05_rules.md) | platform engineers, contributors (reference) | the rule sets okdp-gitops gives its compiles, okdp-compiler's and the security set, who applies them, the render of the upstream charts |
| [Architecture](00_architecture.md) | contributors | the position in OKDP, the source map, a compile, the stores, the design decisions, the tests, the limitations |

The packages, the mapping language, how the instances compile and their
claims are okdp-compiler's:
[its documentation](https://github.com/OKDP/okdp-compiler/blob/main/docs/README.md).
