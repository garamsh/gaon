# Resource API

The `v1alpha1` resource API contract of gaon: who writes what, the state a Machine moves through, the Lease and fencing rule, the invariants and the paths.

Basis: section 4 of garamsh/discussions Discussion #2 ([comment](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288320)), with the decisions of [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). The mvmd statements below hold for garamsh/mvmd at commit `2288ea94810b8151abece78b47f2618db2a645d1`.

## Current decisions

### Naming and groups

- **`Machine` is the microVM.** A physical machine is the core resource `Node`. A machine naru has discovered but gaon has not admitted is a `Node` whose state is `Discovered`. There is no Host resource. The `Node` schema is in §Node.
- **API group suffix `gaon.garam.sh`:** `compute.gaon.garam.sh/v1alpha1` holds the core resources named here, `apps.gaon.garam.sh` the default extensions, `apiextensions.gaon.garam.sh` the resource definitions.
- **Core and extension.** Core resources carry an invariant the core must guarantee: Namespace, Node, Lease, Machine, Volume, roles and bindings, resource definitions, the Driver interface. MachineSet, MachineDeployment, Job, Service, Gateway and Autoscaler are default extensions built on resource definitions and controllers; Network and NetworkPolicy are extensions a Driver interprets. The core keeps one network rule: no Machine runs before its network policy is applied, and the default network blocks all outside traffic.

### Transport

- HTTP with JSON bodies on the paths in §Paths.
- **The root is `/apis/compute.gaon.garam.sh/v1alpha1`** on the API server's address, the Kubernetes `/apis/<group>/<version>` form, which leaves room for the `apps` and `apiextensions` groups beside it. §Paths are relative to it. A client is configured with the full root URL.
- A watch is a streaming HTTP response of newline-delimited JSON events.
- No authentication yet: the API server listens only on a local address.

### Common rules

- Every resource has `apiVersion`, `kind`, `metadata`, `spec` and `status`.
- Each section has one writer: `spec` users and controllers, `/binding` the Scheduler, `/status` the Node Agent for what runs on its node and controllers for the rest.
- Every write is a compare-and-swap on `resourceVersion`. A change of `spec` is tracked by `generation` and `observedGeneration`.
- Deletion sets `deletionTimestamp`; the owner of each finalizer removes it when its work is done. Ownership and garbage collection use `ownerReferences`.
- **A finalizer's owner adds and removes it through `PUT /machines/{name}`**, by compare-and-swap, changing only its own entry of `metadata.finalizers`. The Node Agent's finalizer is `compute.gaon.garam.sh/node-agent`: it adds it before it first starts a Machine bound to its Node, and removes it once a Machine with `deletionTimestamp` set is released on mvmd. The API server removes a Machine on the write that clears its last finalizer.
- A `PUT` without `metadata.resourceVersion` creates the object and answers `AlreadyExists` if it exists; a `PUT` with one is the compare-and-swap. A `DELETE` carries the version in the query `resourceVersion`.
- Enum values are PascalCase, fields camelCase, quantities in `500m` and `4Gi` form, and detail is carried in `conditions`.

### Machine

- **`spec`:** image, kernel, resources (requests and limits for cpu and memory), root disk size, volumes and mounts, networks, ports, `runStrategy`, `idleTimeoutSeconds`, `nodeSelector`, `requiredCapabilities`.
- **`runStrategy`:** `Always`, `RerunOnFailure`, `Once`, `OnDemand`, `Halted`.
- **Set by `/binding` only:** `spec.nodeName` and `spec.bindingEpoch`. The first binding sets the epoch to 1, and each binding sets it to the current epoch plus one.
- **Mutable:** `runStrategy`, `idleTimeoutSeconds`, `resources` and `volumes`, as far as the Driver supports a change while running. Everything else is replaced by a new Machine; MachineDeployment does rolling updates.
- **`status`:** `phase` (`Pending`, `Running`, `Suspended`, `Succeeded`, `Failed`, `Stopped`), `bindingEpoch`, `observedGeneration`, `conditions`, `restartCount`, `addresses`, `lastActiveTime`, and the exit code and time of the last exit.
- **A status whose `bindingEpoch` differs from `spec.bindingEpoch` is void** and read as `Pending`, so no one rewrites a dead node's status.

### State transitions

Every transition below is decided and written by the Node Agent except the rebinding.

| Transition | Condition |
|---|---|
| created, then `Pending` | API server on creation |
| `Pending` to `Running` | image, volumes and network policy ready, then boot or restore succeeded |
| `Running` to `Suspended` | `OnDemand` and idle for at least `idleTimeoutSeconds` |
| `Suspended` to `Running` | a demand signal arrives, the Machine is not `Halted` and not being deleted, and the epoch is current |
| any to `Stopped` | `runStrategy` is `Halted` |
| `Stopped` to `Pending` | `runStrategy` changes from `Halted` |
| `Running` to `Pending` | exit and `runStrategy` asks for a restart, with backoff |
| `Running` to `Succeeded` or `Failed` | the result of a `Once` Machine |
| any to `Failed` | unrecoverable error: image error, no node with a required capability |
| rebinding, epoch plus one | the node's Lease expiry is confirmed (Scheduler writes `/binding`) |

- **OnDemand, what the Store holds:** the phase on each transition, and `lastActiveTime` no more often than a minimum interval, provisionally 60s. Momentary activity is not in the Store; the node serves it on the data plane.
- **OnDemand, a process that exits by itself:** the Machine goes to `Suspended`, keeping its disk. The exit code and time go into status, and the next demand signal starts it. Exits that repeat without demand back off before the next start.
- **Restart backoff,** provisionally: 10s, doubled per restart, at most 5 minutes, reset after the Machine has run for 10 minutes. Exits that repeat without demand under OnDemand back off the same way.
- **Without snapshot support,** `Suspended` means the disk is kept and the process ended; only resume speed differs.

### Volume

- **`spec`:** storage class, access modes, requested storage, data source, `nodeName` (node-local classes only, set by `/binding`).
- **`status`:** `phase` (`Pending`, `Available`, `Bound`, `Released`, `Failed`), `attachment` (machine name and binding epoch), capacity.
- **Attachment is a compare-and-swap by the Node Agent** on `status.attachment`. It succeeds only if `attachment` is empty or its epoch is lower than the Machine's current epoch.
- A node-local volume pins its Machine to that node; moving between nodes is out of the first scope.

### Node

- **`spec`, written by users:** `admitted` (boolean). A user admits a Node by setting it. Until then the Node is `Discovered`.
- **Creation:** the Node Agent creates its Node on registration, with an empty `spec`, by a `PUT` without a version, and the API server sets `status.state` to `Discovered` on creation. This is the one write to a Node's `spec` path the Node Agent makes, and it makes it only to create.
- **`status`:**
  - `state`: `Discovered` while `spec.admitted` is false, `Admitted` once it is true. The Node Agent writes it. Any other value is refused.
  - `capacity`: cpu, memory and storage, from mvmd's runtime info. The Node Agent writes it.
  - `addresses`: the Node Agent writes it.
  - `conditions`: each has a `status` of `True`, `False` or `Unknown`, and any other value is refused. `Ready` is written by the Node Agent. `LeaseExpired` is written by the node lifecycle controller.
- **Each writer changes only its own fields**, by compare-and-swap on the whole object, as §Common rules states for every write.
- **The Node's Lease** is one Lease per Node, named as the Node.
- **A Node with no Lease.** The node lifecycle controller also watches Nodes. For a Node whose Lease it has not seen, it counts validity from its first sighting of that Node, for the Lease duration plus the expiry margin. When that elapses with no Lease seen, it writes `LeaseExpired` true with reason `LeaseMissing`. Seeing the Lease afterwards applies the Lease rule: a new version makes it false, with reason `LeaseRenewed`. This keeps invariant 1: without an acknowledged Lease renewal the Node Agent never renews mvmd's node deadline, so no Machine runs on that Node. A Lease cannot be deleted, as §Paths has no such path, so a Node never having had one is the only case.
- **Placement:** the Scheduler places a Machine only on a Node that is `Admitted`, is `Ready`, and does not have `LeaseExpired` true.

### Lease and fencing

- **A Lease per node, separate from `Node`.** It holds a holder, a renew time and a duration, and the Node Agent renews it by compare-and-swap.
- **Validity is measured by the observer.** A Lease is expired when its duration, plus a margin, has elapsed on the observer's monotonic clock since the observer first saw its current `resourceVersion`. The renew time the node wrote is not compared with another machine's clock.
- **The Node Agent's validity bound.** After the Store acknowledges a renewal, the Lease is valid for the duration counted from the start of that renewal call on the Node Agent's own monotonic clock, because the observer sees it no earlier.
- **The fencing obligation.** The Node Agent renews mvmd's node deadline only so that, for each renewal acknowledged at monotonic time `t` with length `L`, `t + L + δ` is no later than the end of validity of the latest acknowledged Lease renewal. mvmd's runtime API gives the bound: a recorded expiry is never later than the acknowledgement time plus the length, and a Machine runs at most δ past it. This keeps invariant 1 when the Node Agent dies.
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

### Invariants

| # | Invariant | Writer it constrains |
|---|---|---|
| 1 | A Machine runs on at most one node at a time, the one holding the current epoch. | Node Agent: runs only under the current epoch and holds the fencing obligation. Scheduler: rebinds only after confirmed Lease expiry. |
| 2 | A Volume is written by at most one (Machine, epoch). | Node Agent: attaches only by compare-and-swap on `status.attachment`. API server: applies the attachment rule. |
| 3 | `spec`, `/binding` and `/status` are each written by their assigned writer only. | API server: refuses any other writer. Every writer: stays in its section. |
| 4 | A status or request with a stale epoch is ignored. | API server: refuses a status write whose epoch is not the spec's. Readers: treat a void status as `Pending`. |
| 5 | A `Halted` Machine, or one with `deletionTimestamp` set, is woken by no demand signal. | Node Agent: refuses to wake it. |
| 6 | No Machine runs before its network policy is applied. | Node Agent: starts a Machine only after applying the policy. |

### Paths

Relative to the root `/apis/compute.gaon.garam.sh/v1alpha1` (§Transport); where the namespace appears is an open question.

| Path | Writer | Use |
|---|---|---|
| `PUT /machines/{name}` | users, controllers; the Node Agent for its finalizer only | create and change `spec`; add or remove a finalizer |
| `POST /machines/{name}/binding` | Scheduler | bind and set the epoch |
| `PUT /machines/{name}/status` | Node Agent, controllers | report status, epoch checked |
| `DELETE /machines/{name}` | users | set `deletionTimestamp`; the Node Agent removes its finalizer after release |
| `GET /machines` | all | list, with the `resourceVersion` to watch from |
| `GET /machines/{name}` | all | read one Machine |
| `GET /machines?watch=true` | all | subscribe to changes |
| `PUT /volumes/{name}/status` | Node Agent, controllers | attachment and volume status |
| `PUT /nodes/{name}` | users, Node Agent | users create a Node and admit it; the Node Agent only creates it |
| `PUT /nodes/{name}/status` | Node Agent, controllers | report state, capacity, addresses and conditions |
| `GET /nodes` | all | list, with the `resourceVersion` to watch from |
| `GET /nodes/{name}` | all | read one Node |
| `GET /nodes?watch=true` | all | subscribe to changes |
| `PUT /leases/{name}` | Node Agent | create and renew the node's Lease |
| `GET /leases` | all | list, with the `resourceVersion` to watch from |
| `GET /leases/{name}` | all | read one Lease |
| `GET /leases?watch=true` | all | subscribe to changes |

Wake, exec, file API and console are data-plane features and are not recorded in the Store.

### Watch

- A watch resumes from a given `resourceVersion`; the request carries `watch=true` and `resourceVersion`, and the version is required.
- Each line of the stream is one JSON event with a `type` and an `object`.
- Event types, PascalCase as every enum is:
  - `Added`, `Modified` and `Deleted` carry the resource being watched (a Machine, a Node or a Lease) as it is after the change. `Deleted` carries its last state, sent when the last finalizer is removed.
  - `Error` carries the error object, and is the last event of its stream.
- The error object has `code` (the HTTP status), `reason` and `message`. It is the body of a failed response as well. The reasons and their HTTP status are `Conflict` and `AlreadyExists` 409, `NotFound` 404, `Invalid` 422 and `Expired` 410.
- An expired `resourceVersion` gets an `Error` event whose reason is `Expired`; the client lists again and watches from the list's version. The server reports it as an event, never as the status of the response, because compaction can also pass a version while the stream is open.
- The types are in `lib/api/`: `WatchEvent`, `EventType`, `APIError` and `Reason`.

## Rationale

- Boundary, one-way dependencies and the fencing obligation: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- Writers, Lease, watch, OnDemand, group, transport, naming: [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md).
- How `resourceVersion`, compare-and-swap and watch map onto the Store: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- The Lease validity is measured by the observer because a renew time written by one machine cannot be compared with another machine's clock, which can be stepped.

## Open questions

- **Authentication** of API callers is deferred; see Transport.
- **Namespace in the path:** whether the paths carry the namespace and in what form.
- **Admitting a Node:** whether anyone besides a user may set `spec.admitted`.
- **Unacknowledged renewals:** how the Node Agent bounds the expiry of an mvmd renewal whose acknowledgement never arrived; left to the Node Agent's document.
- **Data-plane route:** how exec, file and console traffic reaches a node.
- **Numbers:** measuring the provisional values in §Lease and fencing, the restart backoff and the `lastActiveTime` minimum interval (Discussion #2 section 6 item 13).
- **Store growth and compaction interval:** [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- **Placement of other core resources** (Namespace, roles, resource definitions) in an API group, and the extension resources' schemas.
- **Out of the contract by decision:** Discussion #2 section 6 items 5, 6 and 12 (tenancy, the immediate-bind default, deployment features).
