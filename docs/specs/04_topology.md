# Topologies

Where each part of the [tree](01_layout.md) lives: the platform, the
projects and the output, each in a Git repository or a local directory, at
a path. A topology file says so: `okdp-gitops compile --config <file>`,
the control plane's `GITOPS_CONFIG`. The compile is the same whatever the
topology: a path of the tree is read from, and written to, the repository
of its part: `projects/<project>/...` the project's, `compiled/...` and
`argocd/...` the output's, the rest (`platform.yaml`, `catalogs.yaml`,
`projects.yaml`) the platform's.

## The topology file

```yaml
platform: {url: https://git.example.org/okdp/okdp-sources.git, branch: main, path: ""}
projects: {url: https://git.example.org/okdp/okdp-sources.git, branch: main, path: "projects/{project}"}
output:   {url: https://git.example.org/okdp/okdp-output.git, branch: main, path: ""}
forgejo:  {url: https://git.example.org, owner: okdp, tokenFile: /etc/okdp/forgejo/token}   # optional
```

| Key | Value |
|---|---|
| `url` | required: a Git repository (`https://`, `http://`, `ssh://`, `git@host:path`, `file://`), or a local directory ([below](#local-directories)) |
| `branch` | the branch; `main` by default |
| `path` | the directory of the part in the repository, `""` its root: the platform's holds `platform.yaml`, `catalogs.yaml` and, split, `projects.yaml`; a project's what the tree has under `projects/<project>/`; the output's `compiled/` and `argocd/` |
| `forgejo` | the control plane's: the Forgejo server, the organization and the file of the API token with which it creates the repository of a new project of a split topology ([the control plane's commits](#the-control-planes-commits)) |

`{project}` stands for a project's name, in `projects.url` and
`projects.path` only. An unknown key is refused. The output's repository
is never one of the sources': not the platform's, not the projects', on
no branch, whatever URL names it (another scheme, user, port, case, a
trailing `/` or `.git`), a local directory by its clean path; a project
that `projects.yaml` places in it is refused too
([io and split](#io-and-split)). The output's path is the default
`--path-prefix`, the path at which the engines read the compiled tree
([`--path-prefix`](../01_usage.md#--path-prefix)).

| Message | Cause |
|---|---|
| `platform.url is required` | a part without its URL |
| `output.url "<url>" is the repository of the platform: the output is in a repository of its own, which the CI alone writes, never in one of the sources` | the output in the platform's repository, or the projects' |
| `project lab: its repository <url> is the output's, which holds no source (projects.yaml)` | an entry of `projects.yaml` in the output's repository |
| `project lab: its repository <url> is the platform's, which holds no project in a split topology (projects.yaml)`, `... is the catalog <name>'s (catalogs.yaml)`, `... is project <other>'s (projects.yaml)` | a project, by the template or an entry of `projects.yaml`, in another part's repository (a split topology) |
| `output: {project} names a project's repository only` | `{project}` in the platform's or the output's URL or path |
| `platform.path: path "../x" escapes the deployments repository` | a path out of its repository |
| `projects.path "projects": without {project} in projects.url, the projects are the directories of one repository, the path must end with /{project}` | the projects of one repository are the directories of the parent of `projects.path` |
| `forgejo: url, owner and tokenFile are required` | an incomplete `forgejo` section |
| `output.branch "a..b" is not a branch name` | a branch name Git refuses |
| `project lab: projects.yaml: url "https://evil.example/lab.git": its host evil.example is not one of the topology's (git.example.org)` | an entry of `projects.yaml` on another host than the templates' |
| `project lab: projects.yaml: url "/srv/lab": a local directory or a file:// repository, in a topology whose projects are Git repositories: ...` | an entry of `projects.yaml` that reads the local file system, in a topology of Git repositories |
| `project lab: projects.yaml: path "../outside" goes up (..): a path in the project's repository` | an entry's path out of its repository, or absolute (`path "/etc" is absolute`) |
| `invalid topology: yaml: unmarshal errors: ...` | not YAML, or an unknown key |

## io and split

| Topology | Where | For |
|---|---|---|
| io | the sources, the platform and the projects, in one input repository (or the platform in a repository of its own, [below](#the-platform-apart)), the output in another | a small team, every writer of the input repository trusted with every project |
| split | a repository per project, `projects.url` naming `{project}`, the platform's apart, the output in another; the projects listed in `projects.yaml` | bigger deployments: a team writes its project's repository only, the permissions per repository |

In both, the output is in a repository of its own, which the CI alone
writes ([the CI of the output](../01_usage.md#the-ci-of-the-output)):
the engines read it only, with every right, and the history of the
sources holds no generated commit. There is no topology of one
repository for the sources and the output: Forgejo grants write access
per repository, not per path, and a writer of the output deploys
anything, past every check of the compile.

io:

```yaml
platform: {url: https://git.example.org/okdp/okdp-sources.git}
projects: {url: https://git.example.org/okdp/okdp-sources.git, path: "projects/{project}"}
output:   {url: https://git.example.org/okdp/okdp-output.git}
```

### Who can do what

The repository is the unit of write permission, without the control
plane: who writes a repository writes every file of it.

- io: a writer of the input repository writes every project, the
  platform's own services among them (its operators, its databases),
  and `platform.yaml` when it is there: the
  platform's settings, its classes, its secret store. Every writer is
  then a platform admin. io is for a small team that trusts each of its
  members with all of it.
- split: a team writes its project's repository, nothing else; the
  platform's repository is the platform admins'. The levels the
  compiler enforces (a project sets only what a project may set, an
  instance never `global.okdp`) hold the platform's settings against a
  project's files.

In both, nobody but the CI's bot writes the output, and the sources hold
no token ([the CI of the output](../01_usage.md#the-ci-of-the-output)).

### The platform apart

io with the platform in a repository of its own, written by the platform
admins only, stops the team's write access at the projects: the team
writes `projects/`, not `platform.yaml` nor `catalogs.yaml`. The
recommended io:

```yaml
platform: {url: https://git.example.org/okdp/okdp-platform.git}             # platform.yaml, catalogs.yaml: the platform admins
projects: {url: https://git.example.org/okdp/okdp-projects.git, path: "projects/{project}"}   # the team
output:   {url: https://git.example.org/okdp/okdp-output.git}               # the CI's bot
```

The projects are the directories of `projects/` of `okdp-projects`, a
write of the control plane goes to the repository of the file it
changes, and a compile reads the three.

Split, each part at the root of its repository:

```yaml
platform: {url: https://git.example.org/okdp/okdp-platform.git}
projects: {url: "https://git.example.org/okdp/okdp-project-{project}.git"}
output:   {url: https://git.example.org/okdp/okdp-output.git}
forgejo:  {url: https://git.example.org, owner: okdp, tokenFile: /etc/okdp/forgejo/token}
```

```yaml
# projects.yaml, at the root of okdp-platform, beside platform.yaml
projects:
  - name: demo
  - name: lab                                   # overrides the template
    url: https://git.example.org/data/lab.git
    branch: okdp
    path: deployments
```

A project of a split topology is listed there, or it is not one: its
location is the template's, `{project}` replaced by its name, with what its
entry overrides. The control plane creates the repository of a new project
in the owner of its `forgejo` server, named after the template's URL, its
last segment without `.git` (`okdp-project-{project}.git`:
`<owner>/okdp-project-<project>`; `Topology.ProjectRepository`): the
owner of the template's URL is the `forgejo` owner, for it to be found;
and only for a name the compile accepts (`CheckProjectName`): a reserved
one creates no repository. An
entry never places a project in the platform's repository, in a
catalog's, in another project's (the first listed keeps it) nor in the
output's: the project's writers would write the platform, a catalog or
another project.

`projects.yaml` is the platform repository's content, not the
operator's: its entries are checked as the templates are
(`Topology.CheckEntry`), each refused one failing the reads of its
project only, before anything of it is read:

- `url`: in a topology whose projects are Git repositories, never a
  local directory nor a `file://` repository, and on the host of one of
  the templates, `platform.url`, `projects.url` or `output.url` (any
  scheme, port or user): the credentials go to those hosts only. In a
  topology of local directories, the operator's, an entry may name
  another local directory;
- `branch`: a branch name Git accepts (`a..b`, `-f` refused); the
  templates' too;
- `path`: relative to the root of the repository, without `..`; its
  trailing `/` left out.

## Local directories

A URL without a scheme is a local directory: an absolute path, or a path
relative to the topology file (`./okdp-output`, `../platform`,
`okdp-output`). `file://` and `git@host:path` are Git repositories.
okdp-gitops reads a local directory, `.git` left out, and writes the
output into it, in place; it commits nothing: a person, a test or a CI
that commits by itself. A symbolic link in it is not followed: one the
tree reaches fails the run, as one committed in a repository does
([the tree](01_layout.md)).

```yaml
# topology.yaml: a split topology of three local directories
platform: {url: ./okdp-platform}
projects: {url: './okdp-project-{project}'}
output:   {url: ./okdp-output}
```

```console
$ okdp-gitops compile --config topology.yaml --catalog platform-charts=../platform-charts
compiled okdp-output/compiled/projects/data/instances/data-postgresql/Chart.yaml
...
compiled okdp-output/compiled/projects/kustomization.yaml
```

Each file is printed with its directory, as the topology file names it,
from the current directory. `--tree <dir> --output <dir>` is the io
topology of two local directories, without a file: the platform and the
projects (`projects/<project>`) in the first, the output in the other,
never the same one (`--tree and --output are one directory`); an output
directory inside the tree's is beside the tree, not in it:

```console
$ okdp-gitops compile --tree okdp-sources --output okdp-output --catalog platform-charts=../platform-charts
compiled okdp-output/compiled/projects/data/instances/data-postgresql/Chart.yaml
...
```

## Git repositories

Each run clones, in a temporary directory of its own (the packs stay
compressed, the directory is removed when the run ends), the
repositories it reads, at the last commit of their branch (a depth of
1; the whole history from a server that does not fetch shallow): the
platform's, the output's, and each project's; of each, it reads the
paths of the layout only (`Topology.LayoutPaths`), a project's
repository whole until the platform's `projects.yaml` places it, then
read again from the clone at the same revision (`GitStore.Reread`). The
size of a file is read from its object header before the file is: a file
over `MaxTreeFileSize` (4 MiB), or that would take the files read over
`MaxTreeSize` (64 MiB), is not read, and the compile that needs it fails
naming it (`ErrTooLarge`); a file beside the layout is neither listed nor
read, so a push of a very large file outside the layout cannot exhaust
the run's memory. An empty repository, or a missing branch, is an empty
part: the first commit creates the branch.

The credentials of `okdp-gitops compile` (`gitops.CredentialsFromEnv`) are,
first, those of `$GITOPS_CREDENTIALS_DIR` when it is set, a directory
with a file per key, as Flux's `GitRepository` Secret holds them: the
Secret the engine reads the output with can be mounted as is, with write
access. Else, `$GITOPS_TOKEN`, a bearer token for every HTTP(S)
repository (an SSH one needs the directory): the job of the workflow of
the output gets it from the output repository's secret
`OKDP_GITOPS_TOKEN`, the only token of the CI
([the CI of the output](../01_usage.md#the-ci-of-the-output)). The
control plane reads the directory only (`gitops.NewCredentials`).

A credential for an `http://` URL, which would travel in clear, is
refused (`gitops.Credentials.CheckInsecureHTTP`) unless its host is one
of `$GITOPS_INSECURE_HTTP_HOSTS`: comma-separated host names or globs of
them (`path.Match`, `*.svc.cluster.local` for a cluster's services, a
`*` matching dots too). It is refused on every path, the compile's and
the control plane's: a repository's credentials, a catalog's, a
password in the URL, the token of the `forgejo` API alike. An `http://`
repository without a credential is read anonymously, a catalog of
another host too. A glob admits every host it matches: name the forge's
host itself (`forgejo-http.forgejo.svc.cluster.local`) rather than a
cluster's every service when it can.

| File | For |
|---|---|
| `username`, `password` | HTTPS: a user and its password or token (`git` when only `password` is set) |
| `bearerToken` | HTTPS: a bearer token, in place of `username` and `password` |
| `identity` | SSH: the private key; required for an `ssh://` or `git@host:path` URL |
| `known_hosts` | SSH: the server's keys; required with `identity` |
| `passphrase` | SSH: the key's passphrase, when it has one |

No directory nor token, or an empty directory: anonymous. The same
credentials serve every repository of the topology, and the catalogs on
the host of the platform's repository; the catalogs of other hosts are
read anonymously, the credentials never sent to another forge.

### The compile's commits

A compile writes the output's repository only. Its changes, if any, are
one commit, `okdp: compile`, authored and committed by
`OKDP control plane <okdp-control-plane@okdp.io>`, and pushed: the
compiled files, and `sources.yaml`, the revisions of the sources and of
the catalogs it read ([sources.yaml](03_output.md#sourcesyaml)). It is
made on the revision of the output the compile read, never replayed on a
newer one: the output is the compile of the sources it read. When the
output moved meanwhile (the push of another run, of newer sources), the
push is refused, and the whole tree read and compiled again, from the
new revisions:

```text
okdp-gitops: https://git.example.org/okdp/okdp-output.git: the branch moved since the change was computed: compiling again from the new revision
```

Up to 5 compiles per run; then the run fails, status 1. Runs that
overlap end on the compile of the latest sources, whichever pushes
last: a run that compiled older sources is refused, compiles again, and
finds the output up to date. A commit of the sources after a run read
them is the next run's: its revision is not the one `sources.yaml`
names. The workflow of the output runs on a schedule: no commit, of the
sources or of the compile, starts a run
([the CI of the output](../01_usage.md#the-ci-of-the-output)).

Each commit is logged on stderr: `Committed to the deployments repository`,
its revision, author and subject.

### The control plane's commits

The control plane writes the tree, through the same stores
(`gitops.Repositories`), a commit per write, checked by a compile of the
whole tree ([what a write checks](02_compile.md#what-a-write-of-the-control-plane-checks)):
`okdp: <action> <project>/<name> by <user>`, authored by the logged-in
user, committed by its service identity (`GITOPS_AUTHOR_NAME`,
`GITOPS_AUTHOR_EMAIL`), which a `Co-Authored-By` trailer names
([control plane](https://github.com/OKDP/okdp-control-plane-server)).

- The write runs once on the latest revisions, then each repository it
  changes is committed in turn, with the same message: the projects',
  the platform's, then the output's, so that the sources are in Git
  before what is compiled from them; the platform and the projects of
  one repository (io) are one commit. Each repository the write read is
  committed on the revision it read (`GitStore.UpdateAt`), so a file
  computed from a version is never written over a newer one (a push of
  an `access:` revocation between the read and the commit): a repository
  that moved makes the whole edit run again on the new revisions, up to 5
  times (`Repositories.MaxRetries`), then it fails with `ErrConflict`
  wrapping `ErrMoved`; the repositories are checked for it before the
  first commit. A repository the edit only wrote, never read, takes its
  changes on its latest revision. When one is committed and the next
  fails:
  `<url> is committed, but not <url> (run okdp-gitops compile)`.
- It writes nothing to the output: the CI of the output compiles their
  commits, at its next run.

In a split topology, creating a project creates its repository,
`<owner>/<the last segment of the projects url>`, through the Forgejo
API, pushes its `project.yaml`, then adds it to `projects.yaml`. A
repository that exists already is taken only when it is empty (a
creation that failed halfway); one that holds commits is refused
(`ErrRepositoryNotEmpty`, `okdp/okdp-project-lab: the repository exists
already and is not empty`), whoever filled it: the repository of a
project deleted since is kept with its content, and a repository of
someone else is not the project's. To adopt it, add the project to
`projects.yaml` by hand. The name is refused first when its repository
is the platform's, a catalog's (`catalogs.yaml`), another project's or
the output's. The new repository gets no workflow, no
secret and no runner: the next run of the workflow of the output
compiles it, its token's user reading it (a member of a team of the
organization with read access to its repositories, for example)
([the CI of the output](../01_usage.md#the-ci-of-the-output)). Without
a `forgejo` section, it is refused:
`each project has a repository of its own (...), and the topology has no forgejo section to create it: add forgejo (url, owner, tokenFile) to GITOPS_CONFIG, or create the repository and add the project to projects.yaml by hand`.
Deleting a project removes its entry; its repository is kept, with its
content, and the project cannot be created again while it holds any (the
repository is deleted by hand, in Forgejo, when it is not wanted).

The Forgejo token goes to the API's origin only: a redirect to another
scheme or host is refused, not followed.
