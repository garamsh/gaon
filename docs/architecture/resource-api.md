# Resource API

The `v1alpha1` resource API contract of gaon: who writes what, the state a MicroVM moves through, the Lease and fencing rule, the invariants and the paths.

Basis: section 4 of garamsh/discussions Discussion #2 ([comment](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288320)), with the decisions of [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). The mvmd statements below hold for garamsh/mvmd at commit `2288ea94810b8151abece78b47f2618db2a645d1`.

## Current decisions

### Naming and groups

- **`MicroVM` is the microVM.** A physical machine is the core resource `Node`. A machine naru has discovered but gaon has not admitted is a `Node` whose state is `Discovered`. There is no Host resource. The `Node` schema is in §Node.
- **API group suffix `gaon.garam.sh`:** `compute.gaon.garam.sh/v1alpha1` holds the core resources named here, `rbac.gaon.garam.sh/v1alpha1` the roles and bindings, `apps.gaon.garam.sh` the default extensions, `apiextensions.gaon.garam.sh` the resource definitions, `admissionregistration.gaon.garam.sh` the webhook configurations, `admission.gaon.garam.sh` the `AdmissionReview` a webhook receives (§Admission webhooks), `coordination.gaon.garam.sh` the `Lease` a controller holds for leader election (§Metadata, §Coordination Lease and Event).
- **Core and extension.** Core resources carry an invariant the core must guarantee: Namespace, Node, Lease, Event, MicroVM, Volume, Secret, ConfigMap, ServiceAccount, roles and bindings (`ClusterRole` and `ClusterRoleBinding`, in `rbac.gaon.garam.sh`, §RBAC), resource definitions (`CustomResourceDefinition`, in `apiextensions.gaon.garam.sh`, §CustomResourceDefinition), webhook configurations (`MutatingWebhookConfiguration` and `ValidatingWebhookConfiguration`, in `admissionregistration.gaon.garam.sh`, §Admission webhooks), the Driver interface. MicroVMSet, MicroVMDeployment, Job, Service, Gateway and Autoscaler are default extensions built on resource definitions and controllers; Network and NetworkPolicy are extensions a Driver interprets. The core keeps one network rule: no MicroVM runs before its network policy is applied, and the default network blocks all outside traffic.

### Transport

- HTTP with JSON bodies on the paths in §Paths.
- **The root is `/apis/compute.gaon.garam.sh/v1alpha1`** on the API server's address, the Kubernetes `/apis/<group>/<version>` form, which leaves room for the `apps` and `apiextensions` groups beside it. §Paths are relative to it. A client is configured with the full root URL.
- A watch is a streaming HTTP response of newline-delimited JSON events.
- Without TLS files the API server serves plain HTTP and refuses any address that is not loopback. With them it serves mutual TLS on any address, loopback included: the caller presents a client certificate that the API server's client CA signed, and a Node Agent's subject CN is its Node name ([ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md)), except for the bearer-token access in the next bullet.
- **Bearer-token access.** A request with no client certificate and a valid `Authorization: Bearer <token>` may create a CertificateSigningRequest and read one it created, and nothing else. Any other request without a client certificate is refused. A token is `<id>.<secret>`, matching `[a-z0-9]{6}\.[a-z0-9]{16}`, and the API server honours one only over TLS ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)).

### Common rules

- Every resource has `apiVersion`, `kind`, `metadata`, `spec` and `status`.
- Each section has one writer: `spec` users and controllers, `/binding` the Scheduler, `/status` the Node Agent for what runs on its node and controllers for the rest.
- Every write is a compare-and-swap on `resourceVersion`. A change of `spec` is tracked by `generation` and `observedGeneration`.
- Deletion sets `deletionTimestamp`; the owner of each finalizer removes it when its work is done. Ownership and garbage collection use `ownerReferences` (§Metadata).
- **A finalizer's owner adds and removes it through `PUT /microvms/{name}`**, by compare-and-swap, changing only its own entry of `metadata.finalizers`. The Node Agent's finalizer is `compute.gaon.garam.sh/node-agent`: it adds it before it first starts a MicroVM bound to its Node, and removes it once a MicroVM with `deletionTimestamp` set is released on mvmd. The API server removes a MicroVM on the write that clears its last finalizer. A Volume's finalizer is added and removed the same way through `PUT /volumes/{name}` (§Volume).
- A `PUT` without `metadata.resourceVersion` creates the object and answers `AlreadyExists` if it exists; a `PUT` with one is the compare-and-swap. A `DELETE` carries the version in the query `resourceVersion`.
- Enum values are PascalCase, fields camelCase, quantities in `500m` and `4Gi` form, and detail is carried in `conditions`.

### Metadata

Every kind, the custom resources included, has the same `metadata` ([[ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md)]). Every field below is omitted when empty, so an object written without them reads and writes as before.

- **`uid`** is a version 4 UUID the API server sets at create, replacing any value the request carries. It never changes: an update that carries another is `Invalid`, and one that carries none keeps the stored one. An object deleted and created again has another. An object stored before has none and takes one on its next write.
- **`labels`.** A key is an optional prefix and a name joined by `/`: the prefix a lowercase DNS subdomain of at most 253 characters, the name 1 to 63 characters of alphanumerics, `-`, `_` and `.`, beginning and ending with an alphanumeric. A value is empty or at most 63 characters of that form. Anything else is `Invalid`.
- **`annotations`** have the keys of labels and any string as a value, at most 256 KiB for keys and values together.
- **`ownerReferences`** have `apiVersion`, `kind`, `name`, `uid`, `controller` and `blockOwnerDeletion`. The first three are required; `uid` is the owner's, required on a reference at create and on one an update adds, and a reference stored without one may stay. At most one reference is `controller: true`. `blockOwnerDeletion: true` means the dependent holds up the deletion of its owner when the owner is deleted in the foreground. What garbage collection does with them is not part of this contract yet.
- **Label selectors.** A list and a watch of any kind take the query parameter `labelSelector`:
  - the forms are `key=value`, `key==value`, `key!=value`, `key in (v1,v2)`, `key notin (v1,v2)`, `key` and `!key`, joined by commas, every requirement holding. `!=` and `notin` hold for an object without the label. `gt` and `lt` are not supported;
  - an empty or absent selector selects every object. Text that does not parse, or a key or value invalid in a label, is `Invalid`;
  - a list answers the objects selected. A watch sends `Added` when an object starts to match, `Deleted` when it stops matching or is removed, each with the object as it is after the change, and `Modified` for a change to an object that matches before and after.
- **The types** are in `lib/api/`: `UID`, `ObjectMeta`, `OwnerReference`, `Selector`, `ParseSelector`, and the client option `WithLabelSelector`.

### MicroVM

- **`spec`:** image, kernel (the name of a kernel the node offers, empty for the node's default; the node agent fails a MicroVM naming one the node does not offer, with reason `Rejected`), resources (requests and limits for cpu and memory), root disk size, volumes and mounts, networks, ports, `runStrategy`, `idleTimeoutSeconds`, `nodeSelector`, `requiredCapabilities`, and the fields below, see [ADR 0020](./adr/0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md). Each is omitted when empty, so a MicroVM without them reads and writes as before.
  - **`ports`** is a list of `{name, port, protocol}`. `port` is 1 to 65535 and `protocol` is `TCP` or `UDP`, required. `name` is optional, a DNS label of at most 15 characters, and unique; a port and protocol pair is unique. A MicroVM stored with numeric ports does not decode: the object is deleted and created again.
  - **`command` and `args`** replace the image's entrypoint and default arguments. The first element of `command` is non-empty. There is no `$(VAR)` expansion.
  - **`env`** is a list of `{name, value}` or `{name, valueFrom}`. A name has letters, digits, `-`, `_` and `.`, does not start with a digit, and is unique. `valueFrom` holds exactly one of `secretKeyRef` and `configMapKeyRef`, each `{name, key}`.
  - **`user`** is `user` or `user:group`, each non-empty. **`workingDir`** is an absolute path. Until mvmd takes a working directory (garamsh/mvmd#519), the Node Agent fails a MicroVM that sets it, with the reason `InvalidSpec`.
  - **`files`** is a list of `{path, mode, owner, content}` or `{path, mode, owner, valueFrom}`, `valueFrom` as in `env`. A path is absolute, clean, not `/`, and unique. `mode` is 0 to 0777, default 0644. `owner` is `{uid, gid}`, each 0 or more, default the MicroVM's `user`. `content` and `valueFrom` are exclusive. At most 64 files and 1 MiB of content: the API server checks the inline content, and the Node Agent the resolved content, refusing a MicroVM over the bound with a reason on its status.
  - **References resolve when the MicroVM starts** and are not updated while it runs.
  - **`readinessProbe` and `livenessProbe`** each have exactly one of `exec` (`command`), `tcpSocket` (`port`) and `httpGet` (`path` empty or beginning with `/`, `port`), and `initialDelaySeconds`, `periodSeconds`, `timeoutSeconds`, `successThreshold` and `failureThreshold`, none negative. A zero field takes the default: 0, 10, 1, 1 and 3. A liveness probe's `successThreshold` is 0 or 1. A `port` is a name `spec.ports` declares, or a number 1 to 65535. mvmd runs the probes in the guest; readiness sets the `Ready` condition and liveness failure restarts the MicroVM.
  - **`terminationGracePeriodSeconds`** is 0 or more, and 30 when absent.
  - **`serviceAccountName`** is a lowercase DNS subdomain, the ServiceAccount whose token the MicroVM may be given (§Secret, ConfigMap and ServiceAccount).
- **`runStrategy`:** `Always`, `RerunOnFailure`, `Once`, `OnDemand`, `Halted`.
- **Set by `/binding` only:** `spec.nodeName` and `spec.bindingEpoch`. The first binding sets the epoch to 1, and each binding sets it to the current epoch plus one.
- **Mutable:** `runStrategy`, `idleTimeoutSeconds`, `resources` and `volumes`, as far as the Driver supports a change while running. Everything else, every field of the list above included, is replaced by a new MicroVM; MicroVMDeployment does rolling updates.
- **`status`:** `phase` (`Pending`, `Running`, `Suspended`, `Succeeded`, `Failed`, `Stopped`), `bindingEpoch`, `observedGeneration`, `conditions`, `restartCount`, `addresses`, `lastActiveTime`, and the exit code and time of the last exit.
- **A status whose `bindingEpoch` differs from `spec.bindingEpoch` is void** and read as `Pending`, so no one rewrites a dead node's status.

### State transitions

Every transition below is decided and written by the Node Agent except the rebinding.

| Transition | Condition |
|---|---|
| created, then `Pending` | API server on creation |
| `Pending` to `Running` | image, volumes and network policy ready, then boot or restore succeeded |
| `Running` to `Suspended` | `OnDemand` and idle for at least `idleTimeoutSeconds` |
| `Suspended` to `Running` | a demand signal arrives, the MicroVM is not `Halted` and not being deleted, and the epoch is current |
| any to `Stopped` | `runStrategy` is `Halted` |
| `Stopped` to `Pending` | `runStrategy` changes from `Halted` |
| `Running` to `Pending` | exit and `runStrategy` asks for a restart, with backoff |
| `Running` to `Succeeded` | a `Once` MicroVM whose entrypoint exits cleanly |
| `Running` to `Failed` | a `Once` MicroVM whose entrypoint exits with a failure |
| `Running` to `Succeeded` | `RerunOnFailure` and the entrypoint exits cleanly; it is not started again |
| any to `Failed` | unrecoverable error: image error, no node with a required capability |
| rebinding, epoch plus one | the node's Lease expiry is confirmed (Scheduler writes `/binding`) |

- **OnDemand, what the Store holds:** the phase on each transition, and `lastActiveTime` no more often than a minimum interval, provisionally 60s. Momentary activity is not in the Store; the node serves it on the data plane.
- **OnDemand, a process that exits by itself:** the MicroVM goes to `Suspended`, keeping its disk. The exit code and time go into status, and the next demand signal starts it. Exits that repeat without demand back off before the next start.
- **Restart backoff,** provisionally: 10s, doubled per restart, at most 5 minutes, reset after the MicroVM has run for 10 minutes. Exits that repeat without demand under OnDemand back off the same way.
- **Without snapshot support,** `Suspended` means the disk is kept and the process ended; only resume speed differs.

### Volume

A Volume is a disk a MicroVM names in its `spec.volumes`. Its semantics follow Kubernetes' PersistentVolume and StorageClass, without a claim, and the rules below are gaon's own ([ADR 0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md)).

- **`spec`, written by users at creation:** storage class (required), access modes, requested storage, data source, and `nodeName` (node-local classes only, set by `/binding`).
- **`spec` is immutable after creation.** A `PUT` that changes any field of it, `nodeName` included, is `Invalid`, and so is a create that carries a `nodeName`. Resizing is not designed.
- **`status`, written by the Node Agent of the Volume's Node:** `phase` (`Pending`, `Available`, `Bound`, `Released`, `Failed`), `attachment` (microvm name and binding epoch), capacity. The API server starts a Volume `Pending`.
  - `Available` with `capacity` once the disk exists; `Bound`, with the attachment, while a MicroVM has it attached; `Released` once no MicroVM holds it, keeping the last attachment; `Failed` when the disk cannot be made.
- **Attachment is a compare-and-swap by the Node Agent** on `status.attachment`. It succeeds only if the Volume is not being deleted and `attachment` is empty or its epoch is lower than the MicroVM's current epoch.
- **Class `local`** is a disk of `spec.storage` bytes on one Node, which that Node's agent creates through mvmd and formats ext4. Its `accessModes` is exactly `ReadWriteOnce`, `storage` is positive and `dataSource` is empty; any other value is `Invalid`. A Volume of another class waits `Pending` for a plugin that serves it.
- **Pinning.** `/binding` reads each Volume the MicroVM uses and, in the same write, sets `spec.nodeName` of a `local` Volume that has none to the MicroVM's Node and adds the finalizer `compute.gaon.garam.sh/node-agent` to it. A binding to a Node other than a used Volume's `nodeName`, or of a MicroVM that uses a Volume that is being deleted, is `Conflict`, and of a MicroVM that names a Volume that does not exist `Invalid`; nothing is written. A MicroVM whose `local` Volume is pinned to an expired Node is not rebound elsewhere; moving between nodes is out of the first scope.
- **Deletion.** `DELETE` sets `deletionTimestamp`. The Node Agent of the Volume's Node removes its finalizer through `PUT /volumes/{name}`, changing only its own entry, once no MicroVM has the Volume attached and the disk is deleted. A Volume with no `nodeName` has no finalizer, and `DELETE` removes it at once.
- The types are in `lib/api/`: `Volume`, `VolumeList`, `VolumeSpec` and `VolumeStatus`.

### Node

- **`spec`, written by users:** `admitted` (boolean). A user admits a Node by setting it. Until then the Node is `Discovered`.
- **Creation:** the Node Agent creates its Node on registration, with an empty `spec`, by a `PUT` without a version, and the API server sets `status.state` to `Discovered` on creation. This is the one write to a Node's `spec` path the Node Agent makes, and it makes it only to create.
- **`status`:**
  - `state`: `Discovered` while `spec.admitted` is false, `Admitted` once it is true. The Node Agent writes it. Any other value is refused.
  - `capacity`: cpu, memory and storage, from mvmd's runtime info. The Node Agent writes it.
  - `addresses`: the Node Agent writes it.
  - `microvmPrefixes`: the CIDR prefixes the Node's MicroVMs are addressed from, which the Node Agent writes from mvmd's runtime info. The cluster learns from it which Node owns which prefix. A Node whose mvmd reports none has none, and a value that is not a CIDR is refused.
  - `conditions`: each has a `status` of `True`, `False` or `Unknown`, and any other value is refused. `Ready` is written by the Node Agent. `LeaseExpired` is written by the node lifecycle controller.
- **Each writer changes only its own fields**, by compare-and-swap on the whole object, as §Common rules states for every write.
- **The Node's Lease** is one Lease per Node, named as the Node.
- **A Node with no Lease.** The node lifecycle controller also watches Nodes. For a Node whose Lease it has not seen, it counts validity from its first sighting of that Node, for the Lease duration plus the expiry margin. When that elapses with no Lease seen, it writes `LeaseExpired` true with reason `LeaseMissing`. Seeing the Lease afterwards applies the Lease rule: a new version makes it false, with reason `LeaseRenewed`. This keeps invariant 1: without an acknowledged Lease renewal the Node Agent never renews mvmd's node deadline, so no MicroVM runs on that Node. A Lease cannot be deleted, as §Paths has no such path, so a Node never having had one is the only case.
- **Placement:** the Scheduler places a MicroVM only on a Node that is `Admitted`, is `Ready`, and does not have `LeaseExpired` true.

### CertificateSigningRequest

A request for a client certificate from the cluster CA, used by a Node Agent to get and renew its certificate ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)). The requester names it.

- **`spec`:**
  - `request`: a PEM PKCS#10 request, a block of type `CERTIFICATE REQUEST`. Any other value is refused.
  - `username`: the requester's identity, `bootstrap:<id>` for a token or the CN of the client certificate. The API server sets it from authentication on creation and replaces any value the requester sent.
- **`spec` is immutable.** A `PUT` that carries `metadata.resourceVersion` is refused as `Invalid`; a new request is a new object.
- **`status`:**
  - `conditions`: the types are `Approved` and `Denied`, and any other type is refused. Each has a `status` of `True`, `False` or `Unknown`, and any other value is refused.
  - `certificate`: the issued certificate, PEM, written with the `Approved` condition.
- **Writers.** The requester writes `spec`, once, by creating the request. The signer, which is `gaon-controllers`, writes `status` and nothing else, by compare-and-swap on the whole object.
- **The signer's approval rule:** the request's subject is exactly two attributes, a non-empty CN that does not start with `bootstrap:` and is the requesting Node's name (the Node need not exist yet) and the Organization `gaon:nodes`, the request is client-auth only, and `spec.username` is a bootstrap token or equals that CN. A request that fails it is `Denied` ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), [ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md)).
- **A token holder reads only a request it created,** by `GET /certificatesigningrequests/{name}`. List and watch need a client certificate.
- **There is no delete path.** Requests stay in the Store.

### Lease and fencing

- **A Lease per node, separate from `Node`.** It holds a holder, a renew time and a duration, and the Node Agent renews it by compare-and-swap.
- **Validity is measured by the observer.** A Lease is expired when its duration, plus a margin, has elapsed on the observer's monotonic clock since the observer first saw its current `resourceVersion`. The renew time the node wrote is not compared with another machine's clock.
- **The Node Agent's validity bound.** After the Store acknowledges a renewal, the Lease is valid for the duration counted from the start of that renewal call on the Node Agent's own monotonic clock, because the observer sees it no earlier.
- **The fencing obligation.** The Node Agent renews mvmd's node deadline only so that, for each renewal acknowledged at monotonic time `t` with length `L`, `t + L + δ` is no later than the end of validity of the latest acknowledged Lease renewal. mvmd's runtime API gives the bound: a recorded expiry is never later than the acknowledgement time plus the length, and a MicroVM runs at most δ past it. This keeps invariant 1 when the Node Agent dies.
- **Rebinding waits for expiry.** The epoch increases only after the node's Lease expiry is confirmed.
- **Parameters, provisional until measured.** Each binary that takes one as a flag defaults to it:

| Parameter | Value | Basis |
|---|---|---|
| Lease duration | 30s | Node Agent's default |
| Renew period | 5s | Node Agent's default |
| Deadline length | 15s | Node Agent's default |
| Call timeout | 2s | Node Agent's default |
| δ | 1s | not measured in mvmd; above a real-time thread's wake-up and a SIGKILL (mvmd ADR 0003), and the Node Agent's check 30 >= 15 + 1 + 2x2 holds |
| Expiry margin | 5s | covers the rate error between two monotonic clocks, well under 1% of 30s; a larger margin only delays rebinding |

### Coordination Lease and Event

- **`Lease` of `coordination.gaon.garam.sh/v1alpha1`** is a lock any controller can hold, for leader election, with the root `/apis/coordination.gaon.garam.sh/v1alpha1`. It is a different kind from the Node's `Lease` (§Lease and fencing). `spec` has `holder` (empty for a released lease), `renewTime` and `durationSeconds`, positive. A candidate takes or renews it by compare-and-swap on the `resourceVersion` it read, and expiry is measured by the observer as for the Node's Lease. The group is reserved. The types are `CoordinationLease` and `CoordinationLeaseList`.
- **`Event` of `compute.gaon.garam.sh/v1alpha1`** records that something happened to an object. It has `metadata` and `spec`, and no `status`. `spec` has `involvedObject` (`apiVersion`, `kind` and `name` required, `uid`), a required `reason`, a `message` of at most 1024 bytes, a `type` of `Normal` or `Warning`, a `count` of at least 1, and `firstTimestamp` and `lastTimestamp`, the last not before the first. A reporter that sees an event again raises `count` and moves `lastTimestamp`. An update keeps `involvedObject`, `reason` and `type`, and does not lower `count`. Nothing expires an Event. The types are `ClusterEvent`, `ClusterEventList` and `ObjectReference`.

### Secret, ConfigMap and ServiceAccount

All three are kinds of `compute.gaon.garam.sh/v1alpha1`, cluster-scoped, with `apiVersion`, `kind` and `metadata`, and no `spec` or `status` ([ADR 0020](./adr/0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md)).

- **`Secret`** has `data`, a map of keys to bytes, base64 in JSON. **`ConfigMap`** has `data`, a map of keys to strings. A key is 1 to 253 characters of alphanumerics, `-`, `_` and `.`, other than `.` and `..`. Keys and values together are at most 512 KiB, half of the client's answer bound, because of the base64. A name is a lowercase DNS subdomain of at most 253 characters. A change to `data` is an update by compare-and-swap.
- **Secret values are never logged and never put in an Event.** A Secret formats and logs as its name, and a validation error names a key and never a value. A Secret is sent to admission webhooks as any write is.
- **`ServiceAccount`** is the identity a MicroVM names in `spec.serviceAccountName`. Its name is a lowercase DNS subdomain, with no `:`, because it is part of a token's subject.
- **`POST /serviceaccounts/{name}/token`** takes a `TokenRequest` and answers with it, `status` filled; it is not stored and not sent to webhooks. `spec` has `audiences`, at least one, each non-empty; `expirationSeconds`, 0 for 3600 or 600 to 86400; and `boundMicroVM`, the `name` and `uid` of the MicroVM. The API server answers `Invalid` for a request that does not validate, a MicroVM whose `serviceAccountName` is not the account's name, and an account or MicroVM with no uid; and `Conflict` for a MicroVM that is not the one bound, by name and uid, or that has `deletionTimestamp` set. `status` has `token` and `expirationTimestamp`. A token is never logged and never put in an Event.
- **The claims** are `iss`, `sub` (`gaon:serviceaccount:<name>`), `aud`, `iat`, `exp` (after `iat` by at most 86400 seconds), and `gaon.garam.sh`, with `serviceaccount` and `microvm`, each `{name, uid}`. The token authenticates as the subject, and RBAC binds it like any user. The signing algorithm, keys, issuer URL, discovery document, JWKS and the authentication of a bearer token are not part of this contract yet.
- **The types** are in `lib/api/`: `Secret`, `ConfigMap`, `ServiceAccount` and their lists, `TokenRequest`, `BoundObject`, `TokenClaims`, `NewTokenClaims`, and, on `MicroVM`, `ReferencesSecret` and `ReferencesConfigMap`. `Client` has `Put`, `Get`, `List`, `Watch` and `Delete` for each kind, and `CreateServiceAccountToken`.

### Invariants

| # | Invariant | Writer it constrains |
|---|---|---|
| 1 | A MicroVM runs on at most one node at a time, the one holding the current epoch. | Node Agent: runs only under the current epoch and holds the fencing obligation. Scheduler: rebinds only after confirmed Lease expiry. |
| 2 | A Volume is written by at most one (MicroVM, epoch). | Node Agent: attaches only by compare-and-swap on `status.attachment`. API server: applies the attachment rule. |
| 3 | `spec`, `/binding` and `/status` are each written by their assigned writer only. | API server: refuses any other writer. Every writer: stays in its section. |
| 4 | A status or request with a stale epoch is ignored. | API server: refuses a status write whose epoch is not the spec's. Readers: treat a void status as `Pending`. |
| 5 | A `Halted` MicroVM, or one with `deletionTimestamp` set, is woken by no demand signal. | Node Agent: refuses to wake it. |
| 6 | No MicroVM runs before its network policy is applied. | Node Agent: starts a MicroVM only after applying the policy. |

### Paths

Relative to the root `/apis/compute.gaon.garam.sh/v1alpha1` (§Transport); where the namespace appears is an open question.

| Path | Writer | Use |
|---|---|---|
| `PUT /microvms/{name}` | users, controllers; the Node Agent for its finalizer only | create and change `spec`; add or remove a finalizer |
| `POST /microvms/{name}/binding` | Scheduler | bind and set the epoch; pin the MicroVM's unpinned `local` Volumes to the Node (§Volume) |
| `PUT /microvms/{name}/status` | Node Agent, controllers | report status, epoch checked |
| `DELETE /microvms/{name}` | users | set `deletionTimestamp`; the Node Agent removes its finalizer after release |
| `GET /microvms` | all | list, with the `resourceVersion` to watch from |
| `GET /microvms/{name}` | all | read one MicroVM |
| `GET /microvms?watch=true` | all | subscribe to changes |
| `PUT /volumes/{name}` | users; the Node Agent for its finalizer only | create a Volume; add or remove a finalizer; `spec` is immutable after creation |
| `PUT /volumes/{name}/status` | Node Agent, controllers | attachment and volume status |
| `DELETE /volumes/{name}` | users | set `deletionTimestamp`; the Node Agent removes its finalizer after the disk is deleted |
| `GET /volumes` | all | list, with the `resourceVersion` to watch from |
| `GET /volumes/{name}` | all | read one Volume |
| `GET /volumes?watch=true` | all | subscribe to changes |
| `PUT /nodes/{name}` | users, Node Agent | users create a Node and admit it; the Node Agent only creates it |
| `PUT /nodes/{name}/status` | Node Agent, controllers | report state, capacity, addresses, MicroVM prefixes and conditions |
| `GET /nodes` | all | list, with the `resourceVersion` to watch from |
| `GET /nodes/{name}` | all | read one Node |
| `GET /nodes?watch=true` | all | subscribe to changes |
| `PUT /leases/{name}` | Node Agent | create and renew the node's Lease |
| `GET /leases` | all | list, with the `resourceVersion` to watch from |
| `GET /leases/{name}` | all | read one Lease |
| `GET /leases?watch=true` | all | subscribe to changes |
| `PUT /certificatesigningrequests/{name}` | a bearer-token holder or any authenticated caller | create a request, without a version; `spec.username` is set by the API server |
| `PUT /certificatesigningrequests/{name}/status` | signer | approve or deny, and record the issued certificate |
| `GET /certificatesigningrequests` | all authenticated by certificate | list, with the `resourceVersion` to watch from |
| `GET /certificatesigningrequests/{name}` | all; a token holder only a request it created | read one request |
| `GET /certificatesigningrequests?watch=true` | all authenticated by certificate | subscribe to changes |
| `PUT /events/{name}` | controllers, Node Agent, users | create an Event, without a version; raise its `count`, with one |
| `DELETE /events/{name}` | users | delete it |
| `GET /events` | all | list, with the `resourceVersion` to watch from |
| `GET /events/{name}` | all | read one Event |
| `GET /events?watch=true` | all | subscribe to changes |
| `PUT /secrets/{name}` | users | create a Secret, without a version; change its `data`, with one |
| `DELETE /secrets/{name}` | users | delete it |
| `GET /secrets`, `GET /secrets/{name}`, `GET /secrets?watch=true` | users; a Node only `GET /secrets/{name}` of a Secret a MicroVM bound to it references (§Authorization) | list, read one, subscribe |
| `PUT /configmaps/{name}` | users | create a ConfigMap, without a version; change its `data`, with one |
| `DELETE /configmaps/{name}` | users | delete it |
| `GET /configmaps`, `GET /configmaps/{name}`, `GET /configmaps?watch=true` | users; a Node only `GET /configmaps/{name}` of a ConfigMap a MicroVM bound to it references (§Authorization) | list, read one, subscribe |
| `PUT /serviceaccounts/{name}` | users | create a ServiceAccount, without a version; change its `metadata`, with one |
| `DELETE /serviceaccounts/{name}` | users | delete it |
| `GET /serviceaccounts`, `GET /serviceaccounts/{name}`, `GET /serviceaccounts?watch=true` | all | list, read one, subscribe |
| `POST /serviceaccounts/{name}/token` | users, controllers; the Node Agent for a MicroVM bound to its Node | request a token bound to a MicroVM (§Secret, ConfigMap and ServiceAccount) |

Every list and watch path in this section takes `labelSelector` (§Metadata).

The RBAC paths are relative to their own root, `/apis/rbac.gaon.garam.sh/v1alpha1` (§RBAC), and follow the same forms: `PUT`, `DELETE` and `GET` of one, a list and a watch.

| Path | Writer | Use |
|---|---|---|
| `PUT /clusterroles/{name}` | users | create a ClusterRole, without a version; change its `rules`, with one |
| `DELETE /clusterroles/{name}` | users | delete it |
| `GET /clusterroles`, `GET /clusterroles/{name}`, `GET /clusterroles?watch=true` | all authenticated by certificate | list, read one, subscribe |
| `PUT /clusterrolebindings/{name}` | users | create a ClusterRoleBinding, without a version; change its `subjects`, with one; `roleRef` cannot change |
| `DELETE /clusterrolebindings/{name}` | users | delete it |
| `GET /clusterrolebindings`, `GET /clusterrolebindings/{name}`, `GET /clusterrolebindings?watch=true` | all authenticated by certificate | list, read one, subscribe |

The resource-definition paths are relative to their own root, `/apis/apiextensions.gaon.garam.sh/v1alpha1` (§CustomResourceDefinition).

| Path | Writer | Use |
|---|---|---|
| `PUT /customresourcedefinitions/{name}` | users | create a definition, without a version, named `<plural>.<group>`; change its `versions`, with one |
| `DELETE /customresourcedefinitions/{name}` | users | delete it and its objects (§CustomResourceDefinition) |
| `GET /customresourcedefinitions`, `GET /customresourcedefinitions/{name}`, `GET /customresourcedefinitions?watch=true` | all authenticated by certificate | list, read one, subscribe |

The webhook-configuration paths are relative to their own root, `/apis/admissionregistration.gaon.garam.sh/v1alpha1` (§Admission webhooks), and follow the same forms.

| Path | Writer | Use |
|---|---|---|
| `PUT /mutatingwebhookconfigurations/{name}` | users | create a configuration, without a version; change its `webhooks`, with one |
| `DELETE /mutatingwebhookconfigurations/{name}` | users | delete it |
| `GET /mutatingwebhookconfigurations`, `GET /mutatingwebhookconfigurations/{name}`, `GET /mutatingwebhookconfigurations?watch=true` | all authenticated by certificate | list, read one, subscribe |
| `PUT /validatingwebhookconfigurations/{name}` | users | create a configuration, without a version; change its `webhooks`, with one |
| `DELETE /validatingwebhookconfigurations/{name}` | users | delete it |
| `GET /validatingwebhookconfigurations`, `GET /validatingwebhookconfigurations/{name}`, `GET /validatingwebhookconfigurations?watch=true` | all authenticated by certificate | list, read one, subscribe |

The Lease paths are relative to their own root, `/apis/coordination.gaon.garam.sh/v1alpha1` (§Coordination Lease and Event), and follow the same forms.

| Path | Writer | Use |
|---|---|---|
| `PUT /leases/{name}` | controllers | create a Lease, without a version; take or renew it, with one |
| `DELETE /leases/{name}` | controllers, users | delete it |
| `GET /leases`, `GET /leases/{name}`, `GET /leases?watch=true` | all authenticated by certificate | list, read one, subscribe |

The objects of a registered kind have their own root per definition, `/apis/<group>/<version>`, and the forms of the compute kinds.

| Path | Writer | Use |
|---|---|---|
| `PUT /<plural>/{name}` | users, controllers | create an object, without a version; change it, with one |
| `PUT /<plural>/{name}/status` | controllers | write `status`; present only when the definition enables the status subresource |
| `DELETE /<plural>/{name}` | users | set `deletionTimestamp`; the object is removed when its last finalizer is |
| `GET /<plural>`, `GET /<plural>/{name}`, `GET /<plural>?watch=true` | all authenticated by certificate | list, read one, subscribe |

### Authorization

The API server's `--authorization-mode` is `AlwaysAllow`, the default, which allows every authenticated request, or `RBAC`, which allows only what the Node authorizer or a bound role allows ([ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)). `RBAC` requires the TLS flags.

- **Identity.** The user is the certificate's CN and the groups are its subject's Organization values. A bearer token keeps `bootstrap:<id>` and the rule of §Transport; neither authorizer is consulted for it.
- **A request is** a verb on a resource of an API group:
  - `PUT` without `resourceVersion` is `create`, with one `update`; `DELETE` is `delete`; `GET` of one object is `get`, of a collection `list`, and with `watch=true` `watch`;
  - a subresource is named `resource/sub`: `PUT /microvms/{name}/status` is `update` on `microvms/status`, and `POST /microvms/{name}/binding` is `create` on `microvms/binding`;
  - the group and resource of a custom resource are the definition's `group` and `plural` (`PUT /widgets/{name}/status` is `update` on `widgets/status` in `apps.gaon.garam.sh`), and the definitions themselves are `customresourcedefinitions` in `apiextensions.gaon.garam.sh`, and the webhook configurations are `mutatingwebhookconfigurations` and `validatingwebhookconfigurations` in `admissionregistration.gaon.garam.sh`. `gaon:admin`'s `*` covers all.
- **Order.** The Node authorizer first, then the roles bound to the caller. Permissions only add; there is no deny. A request neither allows is `Forbidden`.
- **The Node authorizer** applies to a caller whose CN equals a Node's name, or the name of the Node it creates. It allows:
  - create its own Node, read it and write its `status`;
  - read and write its own Lease;
  - read MicroVMs; write the `status` of a MicroVM whose `spec.nodeName` is that Node, and its own finalizer through `PUT /microvms/{name}` on such a MicroVM (the API server checks that only the finalizer entry changed, invariant 3);
  - read Volumes, for the reason it reads MicroVMs; write the `status` of a Volume whose `spec.nodeName` is that Node, or that a MicroVM bound to it uses; and its own finalizer through `PUT /volumes/{name}` on a Volume whose `spec.nodeName` is that Node (the API server checks that only the finalizer entry changed, invariant 3);
  - create CertificateSigningRequests and read one it created;
  - `get` one Secret or ConfigMap that a MicroVM whose `spec.nodeName` is that Node references in `env` or `files`, and no `list` or `watch` of either;
  - `create` on `serviceaccounts/token` for a MicroVM whose `spec.nodeName` is that Node, whose `serviceAccountName` is the account in the path, and whose name and uid are the request's `boundMicroVM`.

  Reads of MicroVMs and Volumes are open to a Node because a list cannot be filtered by Node.
- **Escalation.** A caller creates or updates a ClusterRole only if it holds every permission the role grants, and a ClusterRoleBinding only if it holds every permission of the role it names. The Node authorizer's permissions do not count.

### RBAC

The group `rbac.gaon.garam.sh/v1alpha1` has the root `/apis/rbac.gaon.garam.sh/v1alpha1`. Both kinds have `apiVersion`, `kind` and `metadata`, and no `spec` or `status`; they are cluster-scoped.

- **`ClusterRole`:** `rules`, a list; each rule has non-empty `apiGroups`, `resources` and `verbs`, lists of names. A resource is `microvms` or a subresource `microvms/status`; `*` as a whole name matches every value, and a name with `*` inside it is not a wildcard. The verbs are `get`, `list`, `watch`, `create`, `update`, `delete` and `*`; any other verb is `Invalid`. A rule may carry `labelSelector`, a selector of §Metadata's syntax; one that does not parse is `Invalid`, and a rule without one is unrestricted. A role with no rules is valid and grants nothing.
- **`ClusterRoleBinding`:** `roleRef` (`kind` `ClusterRole` and `name`) and `subjects`, each with `kind` `User` or `Group` and a `name`. An empty `roleRef`, another kind and an unknown subject kind are `Invalid`.
- **The label-selector rule** ([[ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md)]). A request on an object whose labels fall outside the `labelSelector` of a rule is not allowed by that rule. Each rule is judged alone, since permissions only add.
  - `get`, `update` and `delete` are judged on the labels of the stored object, an object that does not exist counting as one with no labels. An `update` is also judged on the labels in the request body, so a write cannot move an object into or out of the selection.
  - `create` is judged on the labels of the object in the request body. A mutating webhook that changes them afterwards is not judged again.
  - `list` and `watch` are allowed only when the request carries a `labelSelector` that selects nothing the rule's selector does not. A request with no selector, or a wider one, is `Forbidden`; the server does not filter the answer.
  - A subresource is judged on the stored object's labels.
  - A caller holds a rule with a selector, for the escalation check, only through a rule of the same groups, resources and verbs with no selector or with a selector the new one is within.
- **Bootstrap roles.** In `RBAC` mode the API server creates each of these that is missing at start and never overwrites one, with a binding of each to a group, all in the group `compute.gaon.garam.sh` unless said:

  | Role (group bound) | Rules |
  |---|---|
  | `gaon:scheduler` (`gaon:schedulers`) | `microvms`, `nodes`, `volumes`: `get`, `list`, `watch`; `microvms/binding`: `create`; `microvms/status`: `update`; `leases` of `coordination.gaon.garam.sh`: `get`, `create`, `update` |
  | `gaon:controllers` (`gaon:controllers`) | every resource of every group: `get`, `list`, `watch`, `delete`; `microvms`: `create`, `update`; `microvms/status`, `nodes/status`, `volumes/status`, `certificatesigningrequests/status`: `update`; `leases` of `coordination.gaon.garam.sh`: `get`, `create`, `update` |
  | `gaon:admin` (`gaon:admins`) | `*` on `*` for every API group |

- **The coordination Lease and the controllers' read and delete** are [ADR 0021](./adr/0021-the-bootstrap-roles-give-the-scheduler-and-the-controllers-the-coordination-lease-and-the-controllers-read-and-delete-every-resource.md)'s: the scheduler and the controllers take and renew their own Lease, and the controllers read and delete any object, a custom one included, which the garbage collector needs.
- **An existing cluster** keeps the roles it has, so its `gaon:scheduler` lacks `volumes` and the scheduler's list of Volumes is `Forbidden` until an administrator adds the rule to the role, or deletes the role so the next API server start creates it ([ADR 0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md)). The same holds for the Lease rules and the controllers' read and delete of ADR 0021.
- The types are in `lib/api/`: `ClusterRole`, `PolicyRule` (with `AllowsLabels` and `AllowsSelector`), `ClusterRoleBinding`, `RoleRef` and `Subject`.

### CustomResourceDefinition

A user registers a resource kind with a `CustomResourceDefinition` in `apiextensions.gaon.garam.sh/v1alpha1`, root `/apis/apiextensions.gaon.garam.sh/v1alpha1`. It is cluster-scoped and has `apiVersion`, `kind`, `metadata`, `spec` and `status`. Its semantics follow Kubernetes' and the rules below are gaon's own ([ADR 0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md)).

- **Name.** `<plural>.<group>`; any other name is `Invalid`. The whole name is at most 253 characters.
- **`spec`:**
  - `group`: a domain of at least two lowercase DNS labels. It may not be `compute.gaon.garam.sh`, `rbac.gaon.garam.sh`, `apiextensions.gaon.garam.sh`, `admissionregistration.gaon.garam.sh`, `admission.gaon.garam.sh` or `coordination.gaon.garam.sh`, or any later group the core serves. `apps.gaon.garam.sh` is allowed.
  - `names`: `kind` and `listKind`, each starting with an uppercase letter and holding letters and digits, and different from each other; `plural` and `singular`, each a lowercase DNS label.
  - `scope`: `Cluster`. `Namespaced` and any other value are `Invalid` until namespaces exist.
  - `versions`: exactly one entry, which is served and stored. Its `name` has the form `v1`, `v1alpha1` or `v2beta3`. It has a `schema.openAPIV3Schema`, and optionally `subresources.status: {}`. More than one version needs conversion and is `Invalid`.
- **Immutable after creation:** `group`, `names` and the version `name`. The schema and the subresources may change.
- **The schema** is a structural subset of OpenAPI v3. A node has a `type` of `object`, `array`, `string`, `integer`, `number` or `boolean`, except a node with `x-kubernetes-preserve-unknown-fields: true`, which may omit it. The keywords are `type`, `description`, `properties`, `required`, `items`, `enum`, `minimum`, `maximum`, `pattern` and `x-kubernetes-preserve-unknown-fields`; a definition using any other keyword is `Invalid`.
  - The root is an object that names only `spec` and `status` as properties and does not preserve unknown fields. `apiVersion`, `kind` and `metadata` are the same for every kind and are not in the schema.
  - `properties` and `required` belong to an object; every name in `required` is in `properties`, unless the node preserves unknown fields. `items` belongs to an array, and an array has it. `enum` belongs to a scalar type, and each value has that type. `minimum` and `maximum` belong to `integer` and `number`, and `minimum` is not above `maximum`. `pattern` belongs to a `string` and compiles as a Go regular expression.
  - **Pruning.** A write drops the fields its schema does not name, except under a node that preserves unknown fields. A write that fails the schema is `Invalid`.
- **`status`,** written by the API server: `conditions`. `NamesAccepted` is true when no other definition of the same group has any of the same `kind`, `listKind`, `plural` or `singular`; the older definition keeps its names, and the later one is `NamesAccepted` false with a reason, is not `Established` and serves no path. `Established` is true once the paths are served. Any other condition type is refused.
- **Objects.** An object has `apiVersion` (`<group>/<version>`), `kind`, `metadata`, `spec` and `status`, the last two JSON objects its schema describes. The paths are in §Paths. Compare-and-swap, `generation`, finalizers, `deletionTimestamp` and the watch follow §Common rules and §Watch. With the status subresource enabled, a `PUT` to the object ignores changes to `status`, and a `PUT` to `/status` ignores changes to anything but `status`.
- **Deletion.** `DELETE` of a definition, by compare-and-swap, sets its `deletionTimestamp` and sets `Established` false. The API server then refuses a create of an object of the kind as `Conflict`, deletes each object of the kind as a `DELETE` by a user would, and removes the definition when none is left. A watcher of the objects sees a `Modified` for each object with its `deletionTimestamp` set, a `Deleted` once its finalizers are gone, and then its stream ends; a watcher of definitions sees `Modified` and then `Deleted`. A definition created again starts empty.
- **Types and client.** In `lib/api/`: `CustomResourceDefinition` and its parts, `JSONSchemaProps`, `CustomObject` and `CustomResourceType`. `CustomResourceClient` reads and writes the objects of one kind, and holds `spec` and `status` as JSON, so no Go type is needed per kind.

Wake, exec, file API and console are data-plane features and are not recorded in the Store.

### Admission webhooks

A user registers webhooks the API server calls on a write with a `MutatingWebhookConfiguration` or a `ValidatingWebhookConfiguration` in `admissionregistration.gaon.garam.sh/v1alpha1`, root `/apis/admissionregistration.gaon.garam.sh/v1alpha1`. Both are cluster-scoped and have `apiVersion`, `kind`, `metadata` and `webhooks`, and no `spec` or `status`. Their semantics follow Kubernetes' and the rules below are gaon's own ([ADR 0010](./adr/0010-a-user-registers-admission-webhooks-with-webhook-configurations-the-api-server-calls-them-on-every-accepted-write.md)).

- **A webhook:**
  - `name`: unique in its configuration, a domain of at least three lowercase DNS labels, at most 253 characters.
  - `clientConfig`: `url`, an `https` URL with a host and no user, password, query or fragment; `caBundle`, a PEM bundle of at least one certificate, base64 in JSON, required. The bundle is the only trust the API server places in the webhook.
  - `rules`: at least one; each has non-empty `operations` (`CREATE`, `UPDATE`, `DELETE` or `*`), `apiGroups`, `apiVersions` and `resources`. A group or version is a name or `*`. A resource is `*` (every resource, no subresource), `*/*` (every resource and subresource), `resource`, or `resource/subresource`, with `*` for either part.
  - `failurePolicy`: `Fail`, the default, or `Ignore`. `timeoutSeconds`: 1 to 30, default 10.
  - There is no namespace selector, object selector, `sideEffects` or `reinvocationPolicy`.
- **The call.** `POST` of an `AdmissionReview` (`admission.gaon.garam.sh/v1alpha1`) with `request`: `uid`, `kind`, `resource`, `subResource`, `name`, `operation`, `userInfo` (`username`, `groups`), `object` and `oldObject`. The webhook answers `200` with an `AdmissionReview` whose `response` has the same `uid`, `allowed`, optionally `status` (`code`, `message`), and, from a mutating webhook that allows, `patchType` `JSONPatch` and a base64 RFC 6902 `patch`. No path serves an `AdmissionReview`.
- **Which writes are sent.** Every write accepted after authorization: a `PUT` without a version is `CREATE`, with one `UPDATE`; a write to `/status` is `UPDATE` with `subResource` `status`; `POST /microvms/{name}/binding` is `CREATE` on `microvms/binding`; `DELETE` is `DELETE`, with `oldObject` and no `object`. Writes to the two configuration kinds, `POST /serviceaccounts/{name}/token` (its answer is a credential) and every read are never sent.
- **Order of a write.** Authentication, authorization, the mutating webhooks, the API server's own validation (with a custom resource's schema and pruning), the validating webhooks, the Store. Configurations are called by name and the webhooks of one in their order; each mutating webhook sees the object as the previous patch left it. A patch that changes `apiVersion`, `kind`, `metadata.name` or `metadata.resourceVersion` is a failed call.
- **Failure.** `allowed: false` is `Forbidden` whatever `status.code` says, with a message that names the webhook and carries `status.message`. A failed call is a timeout, a TLS or connection error, a status other than 200, a body that does not decode, a `uid` that is not the request's, or a bad patch; under `Fail` the write is refused as `InternalError`, and under `Ignore` the webhook is skipped.
- **Types and client.** In `lib/api/`: `MutatingWebhookConfiguration`, `ValidatingWebhookConfiguration`, their lists, `Webhook`, `WebhookClientConfig`, `WebhookRule`, and `AdmissionReview` with `AdmissionRequest` and `AdmissionResponse`. `Client` has `Put`, `Get`, `List`, `Watch` and `Delete` for each kind.

### Watch

- A watch resumes from a given `resourceVersion`; the request carries `watch=true` and `resourceVersion`, and the version is required. It may carry `labelSelector` (§Metadata).
- Each line of the stream is one JSON event with a `type` and an `object`.
- Event types, PascalCase as every enum is:
  - `Added`, `Modified` and `Deleted` carry the resource being watched (a MicroVM, a Volume, a Node, a Lease, an Event, a CertificateSigningRequest, a ClusterRole, a ClusterRoleBinding, a CustomResourceDefinition, a MutatingWebhookConfiguration, a ValidatingWebhookConfiguration or an object of a registered kind) as it is after the change. `Deleted` carries its last state, sent when the last finalizer is removed.
  - `Error` carries the error object, and is the last event of its stream.
- A watch with a `labelSelector` is judged on the object before and after each change. A `Modified` change whose previous value the Store has compacted cannot be judged and ends the stream with `Expired`.
- The error object has `code` (the HTTP status), `reason` and `message`. It is the body of a failed response as well. The reasons and their HTTP status are `Conflict` and `AlreadyExists` 409, `NotFound` 404, `Invalid` 422, `Expired` 410, `Unauthorized` 401, `Forbidden` 403 and `InternalError` 500. `Unauthorized` is a request with no client certificate and a missing or invalid bearer token; `Forbidden` is a valid token on any request other than creating a CertificateSigningRequest or reading one it created, and, in the `RBAC` mode, a request by certificate that neither the Node authorizer nor a bound role allows (§Authorization), or a validating or mutating webhook refuses (§Admission webhooks); `InternalError` is a write refused because a call to a webhook whose `failurePolicy` is `Fail` failed.
- An expired `resourceVersion` gets an `Error` event whose reason is `Expired`; the client lists again and watches from the list's version. The server reports it as an event, never as the status of the response, because compaction can also pass a version while the stream is open.
- The Lease of the coordination group is watched the same way, and so is every custom resource.
- The types are in `lib/api/`: `WatchEvent`, `EventType`, `APIError` and `Reason`.

## Rationale

- A MicroVM's process, files, probes, ports and service account, the Secret, ConfigMap and ServiceAccount kinds, the token request and its claims, and the Node authorizer's rule for them: [ADR 0020](./adr/0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md), which changes `ports` in place as ADR 0017 changed a kind and adds to ADR 0008's Node authorizer.
- Boundary, one-way dependencies and the fencing obligation: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- Writers, Lease, watch, OnDemand, group, transport, naming: [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md).
- Authorization, the `rbac` group, the Node authorizer and the bootstrap roles: [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md), which amends ADR 0005, Decision 4.
- Resource definitions, custom resources, their schema, the deletion order and the client: [ADR 0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md), which uses ADR 0008's rules for authorization.
- Metadata, label selectors, owner references, the coordination Lease, the Event and the label-selector rule: [ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md), which adds a field to ADR 0008's rules and a group to ADR 0009's reserved ones.
- Webhook configurations, `AdmissionReview`, the order of a write and the failure of a call: [ADR 0010](./adr/0010-a-user-registers-admission-webhooks-with-webhook-configurations-the-api-server-calls-them-on-every-accepted-write.md), which places the webhooks after ADR 0008's authorization and around ADR 0009's schema.
- Volume creation, the `local` class, pinning at the binding, deletion and the Volume's writers: [ADR 0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md), which adds `volumes` to ADR 0008's `gaon:scheduler` role and widens its Node authorizer.
- Bearer-token access and the CertificateSigningRequest: [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), which amends ADR 0005.
- How `resourceVersion`, compare-and-swap and watch map onto the Store: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- The Lease validity is measured by the observer because a renew time written by one machine cannot be compared with another machine's clock, which can be stepped.

## Open questions

- **Namespace in the path:** whether the paths carry the namespace and in what form.
- **Admitting a Node:** whether anyone besides a user may set `spec.admitted`.
- **Unacknowledged renewals:** how the Node Agent bounds the expiry of an mvmd renewal whose acknowledgement never arrived; left to the Node Agent's document.
- **Data-plane route:** how exec, file and console traffic reaches a node.
- **Numbers:** measuring the provisional values in §Lease and fencing, the restart backoff and the `lastActiveTime` minimum interval (Discussion #2 section 6 item 13).
- **Store growth and compaction interval:** [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- **Placement of Namespace** in an API group.
- **The schemas of the default extensions** (MicroVMSet, MicroVMDeployment, Job, Service, Gateway, Autoscaler) in `apps.gaon.garam.sh`: none is written yet.
- **More than one version of a custom resource,** which needs conversion, and the `scale` subresource and the schema keywords outside §CustomResourceDefinition's list.
- **Webhook selection and calling:** selectors by label or namespace, a reinvocation policy, a service reference for `clientConfig` and a status code from the webhook wait for a need and, for the last, for a Service resource; the API server's calling of webhooks is the next issue.
- **Namespaced roles:** `Role` and `RoleBinding` wait for the namespace decision above; only the cluster-scoped kinds exist.
- **Event expiry:** how long an Event stays, and whether the count of Events is limited.
- **Out of the contract by decision:** Discussion #2 section 6 items 5, 6 and 12 (tenancy, the immediate-bind default, deployment features).
