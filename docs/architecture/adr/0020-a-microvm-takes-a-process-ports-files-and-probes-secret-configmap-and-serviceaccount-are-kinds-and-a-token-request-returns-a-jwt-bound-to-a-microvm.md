# ADR 0020: A MicroVM takes a process, ports, files and probes, Secret, ConfigMap and ServiceAccount are kinds, and a token request returns a JWT bound to a MicroVM

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#124, steps D5, D13 and D14 of goal garamsh/gaon#117. The decisions are the gaon PM's, taken in the session of 2026-11-12 and recorded on that goal.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A MicroVM names an image and nothing about what runs in it.** Its process, environment, files, health and identity come from the image alone, so a workload that needs a credential has no way to be given one.
- **`spec.ports` is a list of numbers.** [ADR 0012](./0012-the-node-agent-asks-a-network-plugin-and-a-storage-plugin-compiled-into-it-before-it-creates-a-machine.md) recorded the consequence: ingress is TCP only. A probe that names a port, and garam's services, need a name and a protocol (D14).
- **mvmd takes the process and the files at create** (garamsh/mvmd#437): `process` with `command`, `args`, `env`, `working_dir` and `user` in OCI string form, and `files` with `path`, `content`, `mode`, `uid` and `gid`, at most 64 files and 1 MiB. It runs probes in the guest (garamsh/mvmd#438). It never reads gaon's objects, so the Node Agent resolves references and passes content.
- **The secret store and the PostgreSQL operator of aurora#749 need a workload identity** that OpenBao's JWT method and AWS's OIDC federation accept (D13). Kubernetes' projected service-account token is that shape.
- **The client reads at most 1 MiB of an answer** (`maxBody` in `lib/api/client.go`), and JSON carries bytes in base64, a third larger.

## Decision

**A MicroVM's spec gains the fields of its process, files, probes, termination and identity, and its ports gain a name and a protocol. `Secret`, `ConfigMap` and `ServiceAccount` are kinds of `compute.gaon.garam.sh/v1alpha1`. A token request on a ServiceAccount returns a short-lived JWT bound to one MicroVM by name and uid.**

1. **Process.** `command` replaces the image's entrypoint and `args` its default arguments, as in Kubernetes. When `command` is set its first element is non-empty. `env` is a list of `{name, value}` or `{name, valueFrom}`: a name has letters, digits, `-`, `_` and `.` and does not start with a digit, and is unique; `value` and `valueFrom` are exclusive. `valueFrom` holds exactly one of `secretKeyRef` and `configMapKeyRef`, each `{name, key}`. `user` is `user` or `user:group`, each part non-empty, the OCI form mvmd takes. `workingDir` is an absolute path. Kubernetes' `$(VAR)` expansion in `command` and `args` is not adopted: the process is given its variables and expands them itself.
2. **References resolve when the MicroVM starts.** The Node Agent reads the Secret or ConfigMap then. A change to one is not seen by a MicroVM that runs, and a MicroVM whose reference does not resolve does not start, with a reason on its status. Whether a missing reference may be marked optional is not decided here.
3. **Files.** `files` is a list of `{path, mode, owner, content}` or `{path, mode, owner, valueFrom}`. A path is absolute, clean and not `/`, and unique. `mode` is 0 to 0777 and defaults to 0644. `owner` is `{uid, gid}`, each 0 or more, and defaults to the MicroVM's `user`, root when it has none. `content` and `valueFrom` are exclusive; `valueFrom` takes the form of an environment variable's.
4. **Bounds.** At most 64 files, and at most 1 MiB of content, mvmd's bounds. The API server checks the inline `content` only, because a reference's content can change after the MicroVM is stored. The Node Agent checks the resolved content, with the exported `MaxFiles` and `MaxFilesBytes`, and refuses a MicroVM over the bound with a reason on its status rather than send mvmd a request it refuses.
5. **Probes.** `readinessProbe` and `livenessProbe` each hold exactly one of `exec` (a `command`, its first element non-empty), `tcpSocket` (a `port`) and `httpGet` (a `path`, empty or beginning with `/`, and a `port`). `httpGet` is plain HTTP, without headers or a scheme, until a need shows otherwise. The timing fields are `initialDelaySeconds`, `periodSeconds`, `timeoutSeconds`, `successThreshold` and `failureThreshold`; none is negative; a zero field takes the default, which is 0, 10, 1, 1 and 3 seconds or counts, and a liveness probe's `successThreshold` is 0 or 1. Readiness sets the `Ready` condition; liveness failure restarts the MicroVM. mvmd runs both in the guest.
6. **Ports.** `spec.ports` is a list of `{name, port, protocol}`. `port` is 1 to 65535 and `protocol` is `TCP` or `UDP`, required. `name` is optional, a DNS label of at most 15 characters, and unique. A port and protocol pair is unique. A probe names a port by name, which `spec.ports` must declare, or by number, 1 to 65535, and not both.
7. **Ports change in place.** `Ports []int32` becomes `[]MicroVMPort` in `v1alpha1`, as [ADR 0017](./0017-the-machine-kind-becomes-microvm-short-name-mvm-in-v1alpha1-in-place.md) renamed a kind in place. **A MicroVM stored with numeric ports no longer decodes.** Nothing migrates it and nothing accepts the old form; the object is deleted and created again with the object form. No other field of the MicroVM changes meaning, and a MicroVM without the new fields reads and writes as before, byte for byte.
8. **Termination and identity.** `terminationGracePeriodSeconds` is 0 or more, and 30 when absent. A stop honours it. `serviceAccountName` is a lowercase DNS subdomain naming a ServiceAccount; the API server does not check that it exists at create, as a Volume's references are not.
9. **Mutability.** Every new field is immutable, as the rest of the spec but `runStrategy`, `idleTimeoutSeconds`, `resources` and `volumes`. They are replaced by a new MicroVM.
10. **`Secret` and `ConfigMap`** have `metadata` and `data`, and no `spec` or `status`, as `ClusterRole` has its `rules` at the top. A Secret's `data` maps keys to bytes, base64 in JSON; a ConfigMap's maps keys to strings. A name is a lowercase DNS subdomain of at most 253 characters. A key is 1 to 253 characters of alphanumerics, `-`, `_` and `.`, other than `.` and `..`. Keys and values together are at most 512 KiB, half of the client's answer bound, because of the base64. A change to `data` is an update by compare-and-swap.
11. **Secret values are never logged and never put in an Event.** A Secret formats, in `fmt` and in `log/slog`, as its name only, and a validation error names a key and never a value. A Secret is sent to admission webhooks as any write is, so a webhook registered for `secrets` sees its values; whoever registers one is trusted with them, as with Kubernetes.
12. **`ServiceAccount`** has `metadata`, and no `spec` or `status`. Its name is a lowercase DNS subdomain, which holds no `:`, because it is part of a token's subject.
13. **The token request.** `POST /serviceaccounts/{name}/token` takes a `TokenRequest` of `compute.gaon.garam.sh/v1alpha1` and answers with it, `status` filled. Its `spec` has `audiences`, at least one, each non-empty; `expirationSeconds`, 0 for 3600 or 600 to 86400; and `boundMicroVM`, the `name` and `uid` of the MicroVM the token is for. gaon binds a token to a MicroVM only, where Kubernetes takes any object. `status` has the `token` and its `expirationTimestamp`. A TokenRequest has no `metadata`, is not stored, and is not sent to admission webhooks: the answer is a credential, and a webhook would see it. A token is never logged and never put in an Event.
14. **What the API server checks.** It refuses, as `Invalid`, a request that does not validate, a MicroVM whose `serviceAccountName` is not the account's name, and an account or a MicroVM that has no uid. It refuses, as `Conflict`, a MicroVM that is not the one the request binds, by name and uid, which is a MicroVM deleted and created again, and one with `deletionTimestamp` set. `NotFound` is the account's or the MicroVM's absence.
15. **The claims.** `iss`, the issuer URL; `sub`, `gaon:serviceaccount:<name>`; `aud`, the request's audiences; `iat` and `exp`, in seconds, with `exp` after `iat` by at most 86400; and `gaon.garam.sh`, an object with `serviceaccount` and `microvm`, each `{name, uid}`. The API server authenticates a bearer token of its own issuer as the subject, and RBAC binds `gaon:serviceaccount:<name>` like any user; and it refuses a token whose MicroVM no longer exists with that uid. Verifying the time and the bound MicroVM belongs to that serving, not to the library, which fixes the shape of the claims and checks it.
16. **What this decision leaves to the serving issue.** The signing algorithm, the keys and their rotation, the issuer URL, the OpenID Connect discovery document and the JWKS, the path of authenticating a bearer token, and the Node Agent's writing and renewing of the token as a file.
17. **The Node authorizer** allows a Node to read, with `get`, a Secret or a ConfigMap only when a MicroVM whose `spec.nodeName` is that Node references it in `env` or `files`. It does not allow `list` or `watch` of either kind, so a Node cannot learn the names of the rest. It allows `create` on `serviceaccounts/token` only for a MicroVM bound to it whose `serviceAccountName` is the account in the path, and whose name and uid are the request's bound ones. The check is by the `ReferencesSecret` and `ReferencesConfigMap` methods of `MicroVM`.
18. **Bootstrap roles are unchanged.** `gaon:admin`'s `*` covers the new kinds. No bootstrap role other than the Node authorizer reads Secrets.
19. **In `lib/api/`:** `MicroVMPort` and `Protocol`; `EnvVar`, `ValueSource`, `KeyReference`, `MicroVMFile` and `FileOwner`; `Probe`, `ExecAction`, `TCPSocketAction`, `HTTPGetAction` and `PortRef`; `Secret`, `ConfigMap` and `ServiceAccount` with their lists; `TokenRequest`, `TokenRequestSpec`, `TokenRequestStatus`, `BoundObject`, `TokenClaims` and `NewTokenClaims`; the constants `MaxFiles`, `MaxFilesBytes`, `MaxDataBytes`, the token lifetimes and the probe defaults; and, on `Client`, `Put`, `Get`, `List`, `Watch` and `Delete` for each of the three kinds and `CreateServiceAccountToken`.

## Consequences

- A MicroVM stored with numeric `ports` stops decoding after this change. aurora is the only consumer outside the repository ([ADR 0017](./0017-the-machine-kind-becomes-microvm-short-name-mvm-in-v1alpha1-in-place.md), Context); a MicroVM of any consumer that carries ports is deleted and created again.
- A caller that sends a MicroVM with the new fields to a server that predates them has them dropped or refused, as with any new field of a `v1alpha1` object.
- `network-default` still allows TCP only for every port. A UDP port is allowed as TCP until the plugin reads the port's protocol; that is the next change to it, not this one.
- Secrets are stored in the Store as they are written, in the clear, as every other kind is. Encryption at rest is not decided here.
- The Node authorizer reads a MicroVM's references from the Store on every `get` of a Secret or a ConfigMap, a list of the MicroVMs of the Node. The cost is the serving issue's to measure.
- The token request is the first `POST` of the compute group that is neither stored nor sent to a webhook; the admission rule of ADR 0010 gains an exception.

## Signs the decision was wrong

- A user needs a Secret or ConfigMap to change under a running MicroVM, and restarts it only for that.
- An operator needs a missing reference to be tolerated, and the start-time refusal makes the MicroVM unusable.
- A workload needs an HTTP header or HTTPS in a probe, and cannot say it.
- A Secret larger than 512 KiB is needed, and the client's answer bound is the only reason it cannot be stored.
- Someone deletes and creates a MicroVM again to change a port, often enough that ports belong with `resources` and `volumes` among the mutable fields.
- A token bound to a MicroVM is wanted for another kind of object.

## Rejected alternatives

- **A second Go field beside `ports`, keeping the numbers.** Two fields for one thing, and the numeric form could never name a protocol.
- **A decoder that accepts both forms.** It keeps a form no one writes and puts a default protocol in the contract; the ADR 0017 rule is a clean break.
- **A client certificate per workload.** D13: it needs a signer path per MicroVM, and neither OpenBao nor AWS accepts it as cleanly as an OIDC token.
- **Live updates of a referenced Secret or ConfigMap.** It needs the Node Agent to watch them and mvmd to rewrite a file in a running guest, which mvmd#437 does not offer.
- **`$(VAR)` expansion.** It puts a second syntax in the contract for what a shell or the program already does.
- **A Secret with `spec` and `status`.** Neither has content here, and the payload at the top is the form of the existing `ClusterRole`.
- **A TokenRequest bound to any object.** Only a MicroVM holds an identity in gaon, and a general reference needs a rule per kind.

## Errata

### 2026-11-13: mvmd takes no working directory at create

- **What was wrong.** Decision says mvmd takes `process` with `command`, `args`, `env`, `working_dir` and `user` at create (garamsh/mvmd#437).
- **What falsified it.** At mvmd commit `223e3eca764c`, the runtime API in `lib/runtimeapi` has no `working_dir` in the create request, only on exec. The first decision of garamsh/mvmd#437 listed it and the shipped API dropped it.
- **What stands.** The decision: a MicroVM takes a `workingDir`. garamsh/mvmd#519 asks mvmd for it, and until it lands the Node Agent fails a MicroVM that sets it, with the reason `InvalidSpec`.
