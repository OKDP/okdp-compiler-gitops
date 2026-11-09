# The rule set

The standard rule set says what a compile refuses on an OKDP platform,
what a package's mapping may call, and how OKDP names its objects. It is
data, in `rules/v1`, embedded in this module and read once by
`rules.Standard()`. This page is its reference: every table, check,
schema, function and document, and where each is applied.

- [Who applies it](#who-applies-it)
- [What is not in the rule set](#what-is-not-in-the-rule-set)
- [When each rule applies](#when-each-rule-applies)
- [Tables](#tables)
- [Checks](#checks)
- [The upstream policy](#the-upstream-policy)
- [Platform settings](#platform-settings)
- [What a mapping sees as platform](#what-a-mapping-sees-as-platform)
- [What a mapping writes](#what-a-mapping-writes)
- [Functions](#functions)
- [OAuth clients](#oauth-clients)
- [The secret store](#the-secret-store)
- [Names, labels and charts](#names-labels-and-charts)
- [What a platform can change](#what-a-platform-can-change)
- [Changing a rule](#changing-a-rule)

```text
rules/v1/
  rules.yaml                  the tables, and every file below by name
  scope.cel.yaml              what a mapping sees as platform
  schemas/
    platform.schema.json      global.okdp of the platform's values
    mapping.schema.json       what a mapping writes, and the functions' arguments
  checks/                     one file per check
  fragments/
    fragment-pod.cel.yaml     okdpPodSpec
    fragment-job.cel.yaml     okdpJob
    fragment-proxy.cel.yaml   okdpSignInProxy
    fragment-web.cel.yaml     okdpWebWorkload
    generated-secret.cel.yaml okdpGeneratedSecret
  functions/
    oidc-secret.cel.yaml      oidcClientSecret
  charts/
    oidc-clients.cel.yaml     the checks of a mapping's OAuth clients
    oidc-dcr.cel.yaml         the objects that register each client
  objects/
    secret-store.cel.yaml     the platform's secret store
    push-secret.cel.yaml      the PushSecret of a claim of another namespace
    pull-secret.cel.yaml      the ExternalSecret that reads it
```

`rules.yaml` is `apiVersion: okdp-compiler/rules.v1`, `version: 1.0.0`.
okdp-compiler's `LoadRules` reads it and fails closed. It refuses a file
that does not meet its `rules.schema.json`, another major `apiVersion`,
a table or a check that is missing or unknown (an empty table is written
`values: []`), a file it names that does not exist or does not compile,
a set without the `objects` chart or the `scope` document, and a role
other than `viewer`, `editor` or `admin`.

## Who applies it

`deploy.Build` gives `rules.Standard()` to every compile unless the
caller sets `BuildOptions.Rules`. `okdp-compile`, `okdp-gitops compile`
and the control plane all go through it.

A program that calls `compiler.Build` or `compiler.Compile` itself and
gives no rule set compiles with okdp-compiler's empty set: nothing below
applies but the names and the two tables listed under
[Names, labels and charts](#names-labels-and-charts).

A caller's rule set is honoured everywhere the tree is read, not only in
the compile:

| Where | How it gets the rule set |
|---|---|
| `deploy.Build` | `BuildOptions.Rules`; nil, the standard set |
| `gitops.Compiler` | its `Rules` field, for its compiles and for `Deployments.CheckProjectName`; nil, the standard set |
| `deploy.ProjectAccess`, `treeedit.CheckProjectName` | their first argument; nil, the standard set |
| `internal/target`, `internal/target/argocd` | they write the groups `ProjectOf` read with the compile's `Inputs.Rules`, and read no rule set themselves |

The control plane offers no choice of rule set. It compiles with the
standard set and gives that same set to `PlatformAdmins`,
`CheckConnection` and `deploy.ProjectAccess`.

## What is not in the rule set

These stay in okdp-compiler's Go code and apply whatever the rule set:

- a release, a namespace and an instance name must be DNS names;
- claim binding, classes, consent across namespaces and claim IDs;
- a level may add values but not change a lower level's (`chart.Overlap`);
- no template code in what an instance or a level above 0 gives
  (`chart.NoTemplate`);
- no two objects of one group, kind, namespace and name
  (`checkDuplicateObjects`);
- `okdpFullname`, `okdpSqlName`, `okdpObjectName`, and the walkers that
  call the checks below.

What each role may do is also fixed in Go: `target.ClusterRole` and
`argocd.Policies`. A rule set may therefore list only some of `viewer`,
`editor` and `admin`, and `LoadRules` refuses any other name. Neither
function treats an unknown role as `admin`; given one, they panic.

## When each rule applies

Each check reports its first failure only, and a failure refuses the
instance. A project is privileged when its namespace is listed in
`global.okdp.policy.privileged`. `AllowPlainSecrets` is
`--allow-plain-secrets` of `okdp-compile`.

| Rule | Reads | Runs in | Privileged project | `AllowPlainSecrets` |
|---|---|---|---|---|
| `namespace` | the namespace | compiler | applies | applies |
| `platformNamespace` | the namespace | compiler | applies: a reservation wins over a privilege | applies |
| `release` | the release | compiler | applies | applies |
| `contextLayer` | each context layer | compiler | applies | applies |
| `valuesLayer` | each values layer | compiler | applies | applies |
| `object` | each object, items of a List too | compiler | skipped | applies |
| `host` on objects | each object, items of a List too | compiler | skipped | applies |
| `plainSecret` | each object, items of a List too | compiler | applies | skipped |
| `upstreamKey`, registries | an instance's upstream values | `internal/policy`, during the Helm merge | skipped | applies |
| `upstreamKey`, registries | `global` of each layer above level 0 | `internal/policy`, `CheckGlobals` | skipped | applies |
| `host` on values | the final values of the charts | `internal/policy`, after the merge | skipped | applies |
| plain value warning | an instance's upstream values | `internal/policy` | applies | skipped |

"Objects" are all those an instance produces: the `objects` section, the
credentials Secrets, generated Secrets, PushSecrets and ExternalSecrets.

The rows in `internal/policy` run through `Inputs.Finalize` and
`Inputs.CheckGlobals`, which `deploy.Build` sets. A host that gives other
hooks, or none, does not get them.

`namespace` and `platformNamespace` also run when a project is created
(`compiler.CheckProjectName`, `treeedit`), with `project` in place of
`Inputs.Namespace` in the message.

## Tables

A check reads a table as `sets.<name>` or `tables.<name>`. A walker reads
it through `RuleSet.Has` or `RuleSet.List`.

### engineNamespaces

`argocd`, `flux-system`, `okdp-releases`.

No project may have these namespaces (check `namespace`). A project's
releases there would rewrite the engine's sources, its Applications or
HelmReleases, or other projects' identities.

### clusterScopedKinds

An object whose `<group>/<kind>` is listed is refused to a project that
is not privileged (check `object`). The group is that of `apiVersion`,
empty for the core group. A kind not listed is assumed namespaced: the
engine's identity, confined to the namespace, refuses it in the cluster.

```text
/Namespace
/Node
/PersistentVolume
storage.k8s.io/StorageClass
storage.k8s.io/CSIDriver
storage.k8s.io/CSINode
storage.k8s.io/VolumeAttachment
storage.k8s.io/VolumeAttributesClass
rbac.authorization.k8s.io/ClusterRole
rbac.authorization.k8s.io/ClusterRoleBinding
apiextensions.k8s.io/CustomResourceDefinition
admissionregistration.k8s.io/ValidatingWebhookConfiguration
admissionregistration.k8s.io/MutatingWebhookConfiguration
admissionregistration.k8s.io/ValidatingAdmissionPolicy
admissionregistration.k8s.io/ValidatingAdmissionPolicyBinding
admissionregistration.k8s.io/MutatingAdmissionPolicy
admissionregistration.k8s.io/MutatingAdmissionPolicyBinding
scheduling.k8s.io/PriorityClass
networking.k8s.io/IngressClass
networking.k8s.io/IPAddress
networking.k8s.io/ServiceCIDR
node.k8s.io/RuntimeClass
apiregistration.k8s.io/APIService
certificates.k8s.io/CertificateSigningRequest
certificates.k8s.io/ClusterTrustBundle
flowcontrol.apiserver.k8s.io/FlowSchema
flowcontrol.apiserver.k8s.io/PriorityLevelConfiguration
policy/PodSecurityPolicy
resource.k8s.io/DeviceClass
resource.k8s.io/ResourceSlice
snapshot.storage.k8s.io/VolumeSnapshotClass
snapshot.storage.k8s.io/VolumeSnapshotContent
gateway.networking.k8s.io/GatewayClass
external-secrets.io/ClusterSecretStore
external-secrets.io/ClusterExternalSecret
external-secrets.io/ClusterPushSecret
generators.external-secrets.io/ClusterGenerator
cert-manager.io/ClusterIssuer
trust.cert-manager.io/Bundle
kyverno.io/ClusterPolicy
kyverno.io/ClusterCleanupPolicy
templates.gatekeeper.sh/ConstraintTemplate
keda.sh/ClusterTriggerAuthentication
cilium.io/CiliumClusterwideNetworkPolicy
crd.projectcalico.org/GlobalNetworkPolicy
projectcalico.org/GlobalNetworkPolicy
security.openshift.io/SecurityContextConstraints
quota.openshift.io/ClusterResourceQuota
argoproj.io/ClusterWorkflowTemplate
```

### deployerApiGroups

`helm.toolkit.fluxcd.io`, `kustomize.toolkit.fluxcd.io`,
`source.toolkit.fluxcd.io`, `notification.toolkit.fluxcd.io`,
`image.toolkit.fluxcd.io`, `fluxcd.controlplane.io`,
`source.extensions.fluxcd.io`, `argoproj.io`.

An object of one of these groups is refused to a project that is not
privileged (check `object`): it would drive the engine with the engine's
own identity.

### deniedUpstreamKeys

`addFrom: policy.upstream.deny`, the only table a platform adds to.

These keys are refused in the upstream values of a project that is not
privileged, at any depth and with any value (check `upstreamKey`). The
scheduling keys stay free.

| Purpose | Keys |
|---|---|
| a pod's privileges on its node | `privileged`, `hostPath`, `hostNetwork`, `hostPID`, `hostIPC`, `hostPort`, `allowPrivilegeEscalation`, `capabilities`, `seccompProfile`, `sysctls` |
| its security context | `securityContext`, `podSecurityContext`, `containerSecurityContext` |
| its identity | `serviceAccountName`, `serviceAccount`, `automountServiceAccountToken` |
| a Service's external addresses (CVE-2020-8554) | `externalIPs` |
| containers and objects a chart adds | `extraContainers`, `sidecars`, `initContainers`, `extraInitContainers`, `extraObjects`, `extraDeploy`, `extraManifests`, `extraResources`, `extraSecrets`, `extraConfigMaps` |

### imageKeySuffixes

`image`, `imageregistry`, `imagerepository`, `imagename`.

A key whose lower-case name ends with one of these holds an image
reference by its own name. The key `image` itself is read with its
sibling keys. See [the upstream policy](#the-upstream-policy).

### platformObjectNames

`okdp-project`, `okdp-control-plane`, `okdp-access-viewer`,
`okdp-access-editor`, `okdp-access-admin`, `okdp-secrets`,
`okdp-dcr-token`.

The platform writes objects of these names in every project's namespace.
A release may not take one (check `release`).

### hostAnnotations

`cert-manager.io/common-name`, `cert-manager.io/alt-names`,
`nginx.ingress.kubernetes.io/server-alias`,
`external-dns.alpha.kubernetes.io/hostname`.

These annotations name hosts, split on commas, spaces, newlines and tabs.
Each host goes through check `host`.

### ingressValueKeys, ingressHostKeys, ingressHostListKeys

| Table | Values | Meaning |
|---|---|---|
| `ingressValueKeys` | `ingress`, `ingresses` | the keys of a chart's values that hold its ingress, at any depth |
| `ingressHostKeys` | `host`, `hostname`, `commonName` | under them, the keys that hold one host |
| `ingressHostListKeys` | `hosts`, `hostnames`, `dnsNames`, `extraHosts` | under them, the keys that hold a list of hosts |

### defaultProjectGlobals

`project`, `storageClass`.

The keys of `global` a level above 0 may set when the platform does not
set `global.okdp.policy.projectGlobals` (check `contextLayer`). A platform
list replaces this table instead of adding to it, and `[]` allows none.

### wholePaths

`connections.*`, `claims.*`, `global.okdp.provisioning.*.classes.*`,
`global.okdp.provisioning.*.default`.

`*` stands for one key. A level gives these paths whole or not at all. A
higher level that sets one a lower level set is refused, even when both
values are maps.

### projectPlatformKeys

`provisioning`.

The keys of `global.okdp` a level above 0 may set (check `contextLayer`),
read as part of the platform's settings: the classes a project adds.

### projectRoles

`viewer`, `editor`, `admin`, least first.

The roles a group may hold in `project.yaml` `access`. Each role with
groups becomes:

| Role | RoleBinding | ClusterRole | Argo CD |
|---|---|---|---|
| `viewer` | `okdp-access-viewer` | `view` | sees the project's Applications |
| `editor` | `okdp-access-editor` | `edit` | also syncs them, runs resource actions and reads logs, never execs |
| `admin` | `okdp-access-admin` | `admin` | also deletes them and overrides their sources and parameters |

With no `access`, the group `okdp-project-<namespace>` is an editor, or
has no role in a rule set that does not list `editor`.
Group names in a RoleBinding carry `global.okdp.access.groupPrefix`. Argo
CD reads the token's groups claim and has no prefix.

## Checks

A check is a document of `vars` and `errors` in the mapping language. It
sees its inputs, `sets` and `tables`, and these functions besides the
language's: `dig`, `pathKey`, `merge`, `rule`, `asList`, `asMap`,
`truthy`, `keys`, `sensitive`, `credentialReference`,
`credentialLiteral`. Checks run in a session of their own, so their cost
does not count against a package's limit.

Conditions are listed in evaluation order. Below, `...` shortens a
message; the full text is in the check's file.

### namespace

Input: `ns`. `checks/namespace.cel.yaml`. A DNS label check comes first,
in Go. `default` is allowed.

| Refused when | The reason given |
|---|---|
| `ns` is `platform` | `platform/ holds the platform's keys of the secret store, which no project writes` |
| `ns` starts with `kube-` | `kube-* is Kubernetes' own (kube-system, kube-public, kube-node-lease)` |
| `ns` is in `engineNamespaces` | `a deployer engine's, or the namespace its target writes in (...)` |

The message is `<field> "<ns>": the namespace is reserved: <reason>`.

### platformNamespace

Input: `field`, `ns`, `settings`. `checks/platform-namespace.cel.yaml`.

| Refused when | Message |
|---|---|
| `ns` is in `policy.reserved` | `<field> "<ns>": the namespace is reserved by the platform (global.okdp.policy.reserved): no project has it` and, when `policy.privileged` lists it too, that the reservation wins |

### release

Input: `field`, `release`, `settings`. `checks/release.cel.yaml`.

The names taken are `platformObjectNames` plus the values of
`secrets.store`, `secrets.serviceAccount` and `oidc.dcr.tokenSecret`.
A package names its objects `<release>` and `<release>-<x>`, so a release
is refused when it equals a taken name or is the part before a `-` of
one: `okdp` is refused because of `okdp-project`.

| Refused when | Message |
|---|---|
| a taken name `n` has `n == release` or starts with `release + "-"` | `<field> "<release>": the name of an object the platform writes in every namespace (<n>), or its beginning up to a "-": ...` |

### contextLayer

Input: `name`, `level`, `layer`, `platform`.
`checks/context-layer.cel.yaml`. Level 0 is the platform's.

| Refused when | Message starts with |
|---|---|
| level above 0 sets `global.okdp` to a non-map, or a key not in `projectPlatformKeys` | `<name>: <path>: a platform setting, level 0 only ...` |
| level above 0 sets a key of `global` other than `okdp` that is not allowed (`policy.projectGlobals`, else `defaultProjectGlobals`) | `<name>: global.<key>: a key of global the platform does not let a project set ...` (a field error at `global.<key>`) |
| `claims` at level 0 | `<name>: claims: a project's, of level 1 or above` |
| `access` at a level other than 1 | `<name>: access: the roles of a project's groups, its file's, of level 1 only` |

### valuesLayer

Input: `name`, `layer`, `declared`, the keys under `global` the package's
parameter schema declares. `checks/values-layer.cel.yaml`.

| Refused when | Message |
|---|---|
| `global` is not a map | `<name>: global is set by the context (Inputs.Context)` |
| a key of `global` is `okdp` or is not declared | `<name>: global.<key> is set by the context (Inputs.Context)` |
| `connections` is set | `<name>: connections are given by the context (Inputs.Context): ...` |
| `claims` is set | `<name>: claims are declared by the context (Inputs.Context): ...` |
| `access` is set | `<name>: access is the project's, its file's (the context at level 1)` |

### object

Input: `svc`, `ns`, `object` (`id`, `kind`, `name`, `apiVersion`, `group`,
`ns`, `raw`). `checks/objects.cel.yaml`.

| Refused when | Message starts with |
|---|---|
| `<group>/<kind>` is in `clusterScopedKinds` | `<svc>: objects: <Kind>/<name>: a cluster-scoped kind (<apiVersion>): ...` |
| the group is in `deployerApiGroups` | `<svc>: objects: <Kind>/<name>: an object of the deployer's kinds (<apiVersion>) lets a project drive the platform's engine: ...` |
| a core `Service` sets a non-empty `spec.externalIPs` | `<svc>: objects: Service/<name>: spec.externalIPs claims addresses the cluster forwards to the Service ...` |
| `metadata.namespace` is set, not empty and not the instance's | `<svc>: objects: <Kind>/<name>: namespace <x>, not the instance's (<ns>): ...` |

### plainSecret

Input: `svc`, `object`. `checks/plain-secret.cel.yaml`. A message never
prints the value.

| Refused when | The plain value named |
|---|---|
| a core `Secret` has a non-empty `data` | `data` |
| a core `Secret` has a non-empty `stringData` | `stringData` |
| an `ExternalSecret` has, in `spec.target.template.data`, or a `ClusterExternalSecret` in `spec.externalSecretSpec.target.template.data`, a literal at a sensitive key | the key's path |
| a `Fake` generator has a non-empty `spec.data`, or a `ClusterGenerator` of `spec.kind: Fake` a non-empty `spec.generator.fakeSpec.data` | that path |

These use okdp-compiler's `value` package:

- A key is sensitive when, without a final `s`, it is one of the
  package's `sensitiveKeys` (`password`, `token`, `secretkey`...), ends
  with `password`, `passwd`, `secret` or `token` in any case, or ends
  with `Key`, `_key` or `-key`.
- A key names a credential rather than holds one when it starts with
  `existing`, ends with `ref`, or is the key of a Secret field
  (`adminPasswordKey`, `tokenKey`). Those pass.
- A literal is a non-blank string with neither `{{` nor `${`, or a
  number.

### upstreamKey

Input: `key`, `value`, `at`, `ns`, `added` (`policy.upstream.deny`).
`checks/upstream-key.cel.yaml`.

| Refused when | Message starts with |
|---|---|
| `key` is in `deniedUpstreamKeys` | `<at>: <key> is denied to the instance's upstream values (a pod's privileges, identity, containers or objects): ...` |
| `key` is in `added` | `<at>: <key> is denied to the instance's upstream values (global.okdp.policy.upstream.deny): ...` |
| `key` is `runAsUser` and the value is `0` (the number or the string `"0"`) | `<at>: runAsUser 0, root, is denied ...` |
| `key` is `runAsNonRoot` and the value is `false` (the boolean or the string `"false"`) | `<at>: runAsNonRoot false is denied ...` |

### host

Input: `at`, `raw`, `host`, `ns`, `suffix`, `listed`.
`checks/host.cel.yaml`.

`host` is lower-cased without a final dot. `suffix` is
`global.okdp.ingress.suffix`, lower-cased and without outer dots. `listed`
is `global.okdp.policy.ingressHosts.<ns>`, lower-cased. A host passes when
it is listed, or when `suffix` is set and the host is exactly one DNS
label, then the namespace, then the suffix: `<label>.<ns>.<suffix>`. So
`seaweedfs.demo.okdp.sandbox` is served by `demo`; a deeper name
(`a.seaweedfs.demo.okdp.sandbox`) and the old `seaweedfs-demo.okdp.sandbox`
are not. With no suffix, only listed hosts pass.

On objects, these are checked:

| Object | Fields |
|---|---|
| any object | the `hostAnnotations` in `metadata.annotations` |
| `networking.k8s.io` `Ingress` | `spec.rules[].host`, `spec.tls[].hosts`; a rule with no host and a `spec.defaultBackend` are refused, each answers every host |
| `cert-manager.io` `Certificate` | `spec.dnsNames`, `spec.commonName` |
| `gateway.networking.k8s.io` `HTTPRoute`, `GRPCRoute`, `TLSRoute` | `spec.hostnames` |
| `gateway.networking.k8s.io` `Gateway` | `spec.listeners[].hostname`; a listener of HTTP, HTTPS or TLS without one is refused |

In the final values, under a key of `ingressValueKeys` at any depth:

- the strings at keys of `ingressHostKeys`;
- the strings at keys of `ingressHostListKeys`, and the list items;
- the `name` of an item of `extraHosts`;
- the `hostAnnotations` of an `annotations` map.

Final values are the package's computed values with the instance's
upstream values over them. The upstream chart's own defaults are not
seen.

## The upstream policy

`internal/policy` walks the values and calls the checks above. These
semantics are its own.

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
`compiler.CheckIngressHosts`). A chart key without upstream values is the
package's, reviewed with it: not rendered. A chart that could not be
fetched refuses the instance alone (`upstream.<key>: the chart ... could
not be fetched`), a chart that cannot be rendered too. The render
sees the values of the chart as the umbrella renders them; a render past
30 seconds is refused, its goroutine abandoned. The deny-list stays the
early, clear message; the render catches what no list of keys can name:
any key a chart turns into objects. The rendered output is checked,
never written.

The render belongs to the CI's compile, the boundary: `okdp-gitops
compile` and `deploy.Build` render by default. A long-running process
turns it off, `gitops.Compiler.SkipRender`, `deploy.WithoutRender`, the
control plane's checks of its writes and its reads: Helm's engine takes
no context, so a render past its timeout leaves its goroutine running,
and its memory is bounded only once it returns. Without the render the
checks of the values stay; a chart that could not be fetched then refuses
nothing, the values being the render's only to vouch for. What such a
compile accepted, the CI's refuses.

## Platform settings

`schemas/platform.schema.json` reads `global.okdp` of the context. Keys
it does not describe are allowed (`ingress.className`, a key a mapping
reads), except where a row says no other key is accepted. A null deletes
a default.

| Setting | Type | Default | Rule |
|---|---|---|---|
| `ingress.suffix` | string | | the domain of the platform's hosts |
| `ingress.annotations` | map of strings | | merged over the cluster issuer into `platform.ingressAnnotations` |
| `tls.clusterIssuer` | string | | the cert-manager ClusterIssuer |
| `tls.caBundleSecret` | string | | the Secret holding `ca.crt` and `bundle.p12` |
| `certificateIssuers` | | | refused, a former setting |
| `serviceCatalog` | | | refused, a former setting |
| `charts` | map | | only `objects` is accepted |
| `charts.objects` | `repository`, `chart`, `version` | see [charts](#charts) | `https://` or `oci://`, never `http://`; a pinned version |
| `oidc.enabled` | boolean | `true` | |
| `oidc.clientProvisioning` | `existing` or `dcr` | `existing` | |
| `oidc.scope` | string | `openid profile email groups` | |
| `oidc.issuerUri` | string | | |
| `oidc.dcr.registrationUrl` | string | | required in dcr mode when a service declares a client |
| `oidc.dcr.authMethod` | string | | required then too: `anonymous` or `initialAccessToken` |
| `oidc.dcr.tokenSecret` | string | `okdp-dcr-token` when read | |
| `oidc.dcr.image` | map | | `registry`, `repository`, `tag`, `digest`, `pullPolicy`, `pullSecrets`; a `tag` needs its `digest`; `pull_policy` and `pull_secrets` refused |
| `oidc.dcr.caSecret` | string | `tls.caBundleSecret` | `""` means none |
| `oidc.dcr.ttlSeconds` | integer ≥ 0 | `30` | |
| `oidc.dcr.backoffLimit` | integer ≥ 0 | `6` | |
| `oidc.dcr.waitTtlSeconds` | integer ≥ 0 | | absent: 600 |
| `proxy.httpProxy`, `httpsProxy`, `noProxy` | string | | |
| `provisioning.<contract>.default` | string | | a class of `classes`, set once at the lowest level that sets it |
| `provisioning.<contract>.classes.<name>` | `provider` (required), `request` | | `<name>` is a DNS label; no other key is accepted |
| `secrets.store` | string | | |
| `secrets.provider` | map | | External Secrets' `spec.provider`, verbatim |
| `secrets.serviceAccount` | string | `okdp-secrets` | |
| `secrets.refreshInterval` | string | `1h` | |
| `access` | map | | no other key than the two below |
| `access.admins` | list of groups | | each `^[A-Za-z0-9][A-Za-z0-9._@:-]{0,254}$` |
| `access.groupPrefix` | string | | `^[A-Za-z0-9._@:#/-]*$` |
| `network` | map | | no other key than the two below |
| `network.policy` | boolean | | |
| `network.ingressNamespaces` | list | | |
| `controlPlane.serviceAccount` | `namespace`, `name`, both required | | a DNS label, a DNS subdomain; no other key is accepted |
| `policy` | map | | no other key than the six below |
| `policy.privileged` | list of strings | | |
| `policy.reserved` | list of strings | | |
| `policy.ingressHosts.<namespace>` | unique DNS subdomains | | exact hosts, no wildcard; `<namespace>` is a DNS label |
| `policy.projectGlobals` | unique strings, not `okdp` | | |
| `policy.projectApiGroups` | list of strings | | the API groups added to the Role a project's releases are installed with (Flux; see [the deployer's Role](#the-deployers-role)) |
| `policy.upstream.deny` | list | | no other key than `deny` and `registries` |
| `policy.upstream.registries` | list | | see [the upstream policy](#the-upstream-policy) |

## The deployer's Role

The Role `okdp-project` of a project's namespace, which helm-controller
impersonates for its releases (Flux; `flux.ServiceAccount`), is the
document `objects/deployer-role.cel.yaml`, its input the groups of
`global.okdp.policy.projectApiGroups`: explicit API groups, every verb
on every resource of each, and `roles`, `rolebindings` of
`rbac.authorization.k8s.io` without `bind` nor `escalate`. RBAC has no
negation, so the list holds the groups the catalogs' charts render
objects of and leaves the engines' out (`deployerApiGroups`): a
`HelmRelease`, an `Application` of a project is not creatable. A
platform adds groups for the custom resources of its own charts; a chart
whose Role names a group not added is refused by the API server
(escalation prevention). The privileged projects keep helm-controller's
identity. Argo CD confines by the AppProject instead
(`clusterResourceWhitelist: []`).

## What a mapping sees as platform

`scope.cel.yaml` computes `platform`. Its inputs are `settings`,
`release`, and `items`, the project's groups with their roles.

| Value | How it is computed |
|---|---|
| `platform.access.platformAdmins` | `access.admins`, sorted and unique |
| `platform.access.viewers` | the administrators and every group of the project |
| `platform.access.editors` | the administrators and the project's `editor` and `admin` groups |
| `platform.access.admins` | the administrators and the project's `admin` groups |
| `platform.oidc` | `oidc` as given, plus `existing` (`enabled` and mode `existing`), and `dcr` as given plus `enabled` (`enabled` and mode `dcr`) and `caSecret` |
| `platform.oidc.dcr.caSecret` | `oidc.dcr.caSecret` when the key is set, else `tls.caBundleSecret` |
| `platform.oidcClientSecret` | `oidcClientSecret("")`, the main client's Secret |
| `platform.ingressAnnotations` | `cert-manager.io/cluster-issuer` from `tls.clusterIssuer`, then `ingress.annotations` over it |
| `platform.proxyEnv` | `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, each only when its setting is not empty |

## What a mapping writes

`schemas/mapping.schema.json` checks a mapping's result and the arguments
of the functions. The sections are the compiler's `output`, `instance`,
`provides` and `objects`, and `oidcClients` from `rules.yaml` `sections`.

| Section | Rule |
|---|---|
| `output` | chart key to a map or null |
| `instance` | `url` and `usage`, strings; other keys ignored |
| `provides[]` | `contract` required; `name`, `values`, `secretRef`, `secret` |
| `provides[].secretRef.name` | a DNS subdomain, 253 at most |
| `provides[].secret` | `generate` (generated keys) and `stringData`; without `generate`, `stringData` holds strings only |
| `objects[]` | `kind` and `metadata.name` required, non-empty |
| `oidcClients[]` | see [OAuth clients](#oauth-clients) |

## Functions

`rules.yaml` declares what mappings call. A fragment is a template
evaluated on its arguments, which are checked against the mapping schema
under the function's name. `result` names the value returned; without it,
all values are.

| Function | File | Returns |
|---|---|---|
| `okdpPodSpec` | `fragments/fragment-pod.cel.yaml` | `spec`, a pod spec |
| `okdpJob` | `fragments/fragment-job.cel.yaml` | `objects` |
| `okdpSignInProxy` | `fragments/fragment-proxy.cel.yaml` | `container`, `volumes`, `objects` |
| `okdpWebWorkload` | `fragments/fragment-web.cel.yaml` | `objects` |
| `okdpGeneratedSecret` | `fragments/generated-secret.cel.yaml`, called by the compiler | `objects` |
| `oidcClientSecret` | document `oidcSecret` | `secret` |

### okdpPodSpec

Required: `containers` (at least one). The schema refuses any other key,
so a `securityContext`, a host namespace or a `hostPath` volume cannot be
passed.

Always set on the pod: `runAsNonRoot: true`, `seccompProfile:
RuntimeDefault`. Always set on every container and init container:
`allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`,
`runAsNonRoot: true`, `seccompProfile: RuntimeDefault`.

| Argument | Default | Effect |
|---|---|---|
| container `readOnlyRootFilesystem` | `true` | |
| container `runAsUser`, `runAsGroup` | the image's | at least 1 |
| `automountServiceAccountToken` | `false` | |
| `serviceAccountName` | | set when not empty |
| `runAsUser`, `runAsGroup`, `fsGroup` | | at least 1, on the pod |
| `volumes` | | any but `hostPath` |
| `restartPolicy`, `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `imagePullSecrets`, `priorityClassName`, `terminationGracePeriodSeconds` | | passed as written |

### okdpJob

Required: `name`, a DNS label, and `pod`, the arguments of `okdpPodSpec`.
It returns one `batch/v1` Job, with OKDP's labels and the arguments' over
them.

| Argument | Default |
|---|---|
| pod `restartPolicy` | `Never` |
| `backoffLimit` | `1` |
| `ttlSecondsAfterFinished` | `300` |
| `activeDeadlineSeconds` | none |
| `component` | none |
| `hook.helm`, `hook.weight` | none: `helm.sh/hook`, `helm.sh/hook-weight` |
| `hook.deletePolicy` | `before-hook-creation`, with `hook.helm` |
| `hook.argocd` | none: `PreSync`, `Sync`, `PostSync`, `SyncFail`, `PostDelete` or `Skip` |
| `hook.argocdDeletePolicy` | `BeforeHookCreation`, with `hook.argocd` |

### okdpSignInProxy

oauth2-proxy, default image
`quay.io/oauth2-proxy/oauth2-proxy:v7.15.4` by digest, in front of a
service without a login. It signs users in with the platform's OIDC and
lets in members of the allowed groups only. It reads the main OAuth
client's Secret, `platform.oidcClientSecret`, so the package must declare
that client.

| Refused when | |
|---|---|
| `platform.oidc.enabled` is false | no identity provider |
| `oidc.issuerUri` is empty | |
| `host` is empty | the redirect URL needs it |
| `upstream` is empty | |
| the allowed groups are empty | it would let in every user of the realm |

| Argument | Default |
|---|---|
| `name` | `okdpFullname('oauth2-proxy')`: the Secret of the cookie secret |
| `cookieName` | `_<name>_oauth2` |
| `port` | `4180` |
| `allowedGroups` | `platform.access.editors` |
| `groupsClaim` | `groups` |
| `caBundleSecret` | `global.okdp.tls.caBundleSecret`, mounted at `/cacerts` |
| `cookieExpire` | `8h` |
| `upstreamTimeout` | the proxy's |
| `resources` | requests `50m`, `64Mi`; limits `200m`, `128Mi` |

Fixed flags: `--provider=oidc`, `--email-domain=*` (the groups decide),
`--cookie-secure=true`, `--skip-provider-button=true`,
`--reverse-proxy=true`, `--silence-ping-logging=true`,
`--redirect-url=https://<host>/oauth2/callback`, and
`--code-challenge-method=S256` unless `platform.oidc.usePKCE` is false.
The cookie secret is an `okdpGeneratedSecret` of 32 characters.

### okdpWebWorkload

Required: `pod`, whose first container is the application, and `port`.
It returns a Deployment and a Service, plus these when asked:

| Object | When | Rule |
|---|---|---|
| PersistentVolumeClaim `<name>-<volume>` | `storage` | `ReadWriteOnce`; kept on uninstall unless `keep: false` |
| Ingress | `ingress` | the platform's annotations with the arguments' over them; class from the arguments or `global.okdp.ingress.className`; `<name>-tls` unless given |
| sign-in proxy as a sidecar | `signIn` | the Service then reaches the proxy only |
| NetworkPolicy | `signIn`, unless `networkPolicy: false` | lets in the proxy's port alone |

| Refused when | |
|---|---|
| `signIn` without `ingress` | no host to send users to |
| `signIn` with a `port` that is not a number | the proxy reaches the application on the pod's loopback |
| `signIn` and a container named `oauth2-proxy` | |

Defaults: `name` is `okdpFullname(component)`, `servicePort` `80`,
`replicas` `1`, `strategy` `Recreate` with storage and `RollingUpdate`
otherwise, `storage.volumeName` `data`, `ingress.path` `/`.

The NetworkPolicy does not isolate the application from its own
namespace when `global.okdp.network.policy` is on: policies are a union,
and the project's lets the namespace's pods reach every port.

### okdpGeneratedSecret

External Secrets generates the values in the cluster, so Helm renders the
same objects each time. It returns one Password generator per key and an
ExternalSecret that writes the Secret `name`.

| Argument | Default |
|---|---|
| `keys[].length` | `32`, also for `0` |
| `keys[].digits` | `6` |
| `keys[].symbols` | `0` |
| `keys[].noUpper` | `false` |
| `keys[].transform` | none: `sha256` or `b64enc` |
| `namespace` | the release's; another one is for privileged projects only (check `object`) |
| `refreshInterval` | `"0"`, generated once |
| `apiVersion` | `external-secrets.io/v1` |

A generator is named `<name>-<key>`, lower-cased, with `_` and `.` as
`-`. Past 63 characters it is cut to 54, a trailing `-` is dropped, and 8
hex characters of the key's SHA-256 are added. Two keys that give one
generator name are refused. `stringData` holds static keys, and a
generated key replaces a static key of the same name.

### oidcClientSecret

For the client `arg`, `""` being the main client:

| Mode | Secret |
|---|---|
| dcr | `<release>-<namespace>[-<arg>]-dcr` |
| otherwise | `creds-<release>-oauth2[-<arg>]` |

## OAuth clients

`charts/oidc-clients.cel.yaml` reads a mapping's `oidcClients` section.

Each client requires `grantTypes` (at least one). The schema refuses any
key other than `name`, `grantTypes`, `redirectUris`, `scope`,
`clientName`, `public`, `request`, `secret` and `caSecret`. `name` is a
DNS label of 54 characters at most; empty means the service's main
client.

| Refused when | Mode |
|---|---|
| two clients have the same name, or both have none | any |
| `secret` is given and is not `<release>-<name>`, or `<release>-oidc` for the main client | any |
| `request` sets a URL the provider trusts (`redirect_uris`, `post_logout_redirect_uris`, `request_uris`, `sector_identifier_uri`, `jwks_uri`, `initiate_login_uri`, `backchannel_logout_uri`, `frontchannel_logout_uri`) | any |
| a `redirectUris` entry, in a project that is not privileged, is not http or https, has no host or holds user information, or its host is not served by the project: `<label>.<namespace>.<suffix>` or listed in `policy.ingressHosts` | dcr |
| `oidc.dcr.image` has a `tag` and no `digest` | dcr |
| `oidc.dcr.authMethod` is neither `anonymous` nor `initialAccessToken`, and there is a client | dcr |
| `oidc.dcr.registrationUrl` is missing or empty, and there is a client | dcr |
| two clients write one Secret | dcr |

There is no cap on the number of clients. In dcr mode each client adds
objects to the instance's, from `charts/oidc-dcr.cel.yaml`, under the same
policy as the mapping's objects. They are named `<release>-oidc-dcr`, or
`<release>-<name>-oidc-dcr`, cut to 56 characters without a final `-`.

| Object | Role | Hook |
|---|---|---|
| ServiceAccount, Role, RoleBinding, ConfigMap (the script), `<name>` | the registration | `pre-install,pre-upgrade`, `PreSync`; weight and sync wave -10 |
| Service `<name>-headless` | reverse DNS of the Job's pod, for a provider with a trusted hosts policy | the same |
| Job `<name>`, by `okdpJob` | registers the client and writes its Secret | the same; weight and sync wave -5 |
| ServiceAccount, Role, RoleBinding, ConfigMap `<name>-delete` | the removal | `pre-delete`, `PostDelete`; -10 |
| Job `<name>-delete`, by `okdpJob` | removes the client and its Secret | the same; -5 |

The pods are those of `okdpJob`: non-root (UID 65534), no privilege
escalation, every capability dropped, a read-only root filesystem, the
image by digest, a memory limit. They mount their ServiceAccount's token,
needed to write the Secret. The registration's Role has `create` on
Secrets, which Kubernetes cannot limit by name, and `get`, `patch` on the
client's Secret by name; the removal's has `get`, `delete` on it by name.

| Job setting | Source |
|---|---|
| `DCR_REGISTRATION_URL` | `oidc.dcr.registrationUrl` |
| the token volume, `REGISTRATION_TOKEN_FILE` | with `initialAccessToken`: `oidc.dcr.tokenSecret`, key `token` |
| `REQUEST.client_name` | `clientName`, else `<release>-<namespace>[-<name>]` |
| `REQUEST.application_type` | `web` |
| `REQUEST.grant_types` | `grantTypes` |
| `REQUEST.scope` | the client's scope, else `oidc.scope`, without `openid`, which Keycloak refuses as a client scope |
| `REQUEST.redirect_uris` | `redirectUris`, when any |
| `REQUEST.token_endpoint_auth_method` | `none` for a public client |
| any other field of `REQUEST` | the client's `request`, which overrides the computed fields, never a URL the provider trusts |
| the CA volume | the client's `caSecret`, else `platform.oidc.dcr.caSecret` |
| `SECRET_NAME` | the client's Secret |
| `PUBLIC` | whether the client is public: no `client_secret` key |
| `ttlSecondsAfterFinished`, `backoffLimit` | `oidc.dcr.ttlSeconds`, `oidc.dcr.backoffLimit` |
| `DISCOVERY_URL` | with an issuer: `<issuerUri>/.well-known/openid-configuration`, one trailing `/` dropped |
| `WAIT_TTL_SECONDS` | `oidc.dcr.waitTtlSeconds`, 600 by default |
| the image | `oidc.dcr.image` over `quay.io/adaltas/oidc-dcr-job:0.6.0` by digest |

The Job keeps the client as the mapping asks. The Secret holds
`client_id`, `client_secret` unless public, `registration_client_uri` and
`registration_access_token`. At each sync the Job reads the registration
with that token: a field that differs is sent by `PUT` on
`registration_client_uri`, and the token the provider returns is written
back. A client gone from the provider (`404`) is registered again.

If the registration token was lost or is refused (`401`, `403`), or the
Secret holds none, the Job registers a new client and overwrites the
Secret; the old client stays at the provider, its `client_id` in the Job's
log, for an administrator to remove. An unreachable provider fails the Job,
which registers nothing.

The removal, at uninstall, deletes the client with its token, then the
Secret. It never fails, so it cannot stop an uninstall: with no Secret it
does nothing, with a refused token it deletes the Secret and leaves the
client, with an unreachable provider it keeps both. Argo CD runs it as a
`PostDelete` hook, Helm as `pre-delete`.

The Job's log holds no answer of the provider, no `client_secret` and no
token: a failure says the HTTP status and the provider's `error` code.

## The secret store

Claims across namespaces carry credentials through the platform's store.
A rule set that does not define these documents cannot compile such a
claim.

The document `secretStore`, `objects/secret-store.cel.yaml`, reads
`secrets.*`:

| Value | Rule |
|---|---|
| `store` | present when `secrets.store` and `secrets.provider` are both set: the SecretStore the targets write in each project |
| `store.serviceAccount` | `secrets.serviceAccount`, else `okdp-secrets` |
| `ref.name` | `secrets.store` |
| `ref.kind` | `SecretStore` with a provider, else `ClusterSecretStore` |
| `refreshInterval` | `secrets.refreshInterval`, else `1h` |

| Object | Document | Rule |
|---|---|---|
| `external-secrets.io/v1alpha1` PushSecret, named after the claim's ID | `pushSecret`, `objects/push-secret.cel.yaml` | pushes each secret field of the provider's Secret as a property of the key; `updatePolicy: Replace`, `deletionPolicy: Delete` |
| `external-secrets.io/v1` ExternalSecret, named after the Secret | `pullSecret`, `objects/pull-secret.cel.yaml` | reads each field back into that Secret; `creationPolicy: Owner` |

The key in the store is `<provider namespace>/<claim ID>`, computed by
the compiler.

## Names, labels and charts

### names

`$<word>` stands for a value.

| Name | Pattern | Used for |
|---|---|---|
| `credentialsSecret` | `$output-$contract-credentials` | the Secret of a provider's output |
| `connectionSecret` | `$release.$connection-credentials` | the Secret of a connection held in the store |
| `claimSecret` | `$release-$secret` | a claimer's copy of a Secret from another namespace |
| `credentialsLabel` | `okdp.io/credentials-$contract` | the label of a credentials Secret, set to `"true"` |

The empty set in okdp-compiler keeps these four names, and the values of
`projectPlatformKeys` and `wholePaths`, so that claims still bind.

### labels

| Group | Labels |
|---|---|
| selector, `okdpSelectorLabels` | `app.kubernetes.io/name: <service>`, `app.kubernetes.io/instance: <release>` |
| common, added by `okdpLabels` | `app.kubernetes.io/managed-by: Helm`, `app.kubernetes.io/part-of: okdp`, `okdp.io/instance: <release>`, `okdp.io/service: <service>` |
| with a component argument | `app.kubernetes.io/component: <component>` |

### keepSync

An object annotated `helm.sh/resource-policy: keep`, trimmed and in any
case, also gets `Prune=false` and `Delete=false` in
`argocd.argoproj.io/sync-options`. An option the object already sets by
that name is left alone. Argo CD reads none of Helm's annotations.

### charts

| Name | Chart | Override |
|---|---|---|
| `objects` | `oci://quay.io/okdp/charts`, `okdp-objects`, `0.1.0` | `global.okdp.charts.objects` |

`oci://quay.io/okdp/charts` is not published yet. Only the sandbox's
local registry holds `okdp-objects`, and the sandbox overrides
`charts.objects` to reach it.

`ownCharts` ties the section `oidcClients` to the chart `oidcDcr`, the
list document `oidcClients` and the values template `oidcDcr`.

## What a platform can change

`platform.yaml` cannot add a check, a function or a schema. It can only
set these:

| Setting | Effect on the rules |
|---|---|
| `policy.upstream.deny` | adds to `deniedUpstreamKeys` |
| `policy.upstream.registries` | turns on the image check |
| `policy.reserved` | adds namespaces to `platformNamespace` |
| `policy.privileged` | exempts namespaces, as in [the table above](#when-each-rule-applies) |
| `policy.ingressHosts` | adds exact hosts to check `host` |
| `policy.projectGlobals` | replaces `defaultProjectGlobals` |
| `policy.projectApiGroups` | adds API groups to the Role of a project's releases, never an engine's nor `*` (the document `deployerRole`) |
| `ingress.suffix` | sets the host pattern of check `host` |
| `secrets.store`, `secrets.serviceAccount`, `oidc.dcr.tokenSecret` | add names to check `release` |
| `charts.objects` | replaces the `objects` chart |

Only `deniedUpstreamKeys` is added to through `addFrom`. Nothing in
`platform.yaml` removes a value from a table.

## Changing a rule

Edit `rules/v1`, and run `make check`. A rule's message is part of its
contract: `testdata/cli` and the tests of `internal/policy` compare them.

okdp-compiler holds a copy of this set in `testdata/rules` for its own
tests, as its module cannot import this one. A change here must be made
there too. `rules.TestCompilerCopy` fails while the two differ, finding
the compiler's module with `go list -m`.

A new table or check name also needs a change in okdp-compiler:
`LoadRules` refuses names it does not know.
