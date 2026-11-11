# Resource API

The `v1alpha1` resource API contract of gaon: who writes what, the state a Machine moves through, the Lease and fencing rule, the invariants and the paths.

Basis: section 4 of garamsh/discussions Discussion #2 ([comment](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288320)), with the decisions of [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). The mvmd statements below hold for garamsh/mvmd at commit `2288ea94810b8151abece78b47f2618db2a645d1`.

## Current decisions

### Naming and groups

- **`Machine` is the microVM.** A physical machine is the core resource `Node`. A machine naru has discovered but gaon has not admitted is a `Node` in the pre-registration state `Discovered`. There is no Host resource. The `Node` schema beyond this is not decided.
- **API group suffix `gaon.garam.sh`:** `compute.gaon.garam.sh/v1alpha1` holds the core resources named here, `apps.gaon.garam.sh` the default extensions, `apiextensions.gaon.garam.sh` the resource definitions.
- **Core and extension.** Core resources carry an invariant the core must guarantee: Namespace, Node, Lease, Machine, Volume, roles and bindings, resource definitions, the Driver interface. MachineSet, MachineDeployment, Job, Service, Gateway and Autoscaler are default extensions built on resource definitions and controllers; Network and NetworkPolicy are extensions a Driver interprets. The core keeps one network rule: no Machine runs before its network policy is applied, and the default network blocks all outside traffic.

### Transport

- HTTP with JSON bodies on the paths in §Paths.
- A watch is a streaming HTTP response of newline-delimited JSON events.
- No authentication yet: the API server listens only on a local address.

### Common rules

- Every resource has `apiVersion`, `kind`, `metadata`, `spec` and `status`.
- Each section has one writer: `spec` users and controllers, `/binding` the Scheduler, `/status` the Node Agent for what runs on its node and controllers for the rest.
- Every write is a compare-and-swap on `resourceVersion`. A change of `spec` is tracked by `generation` and `observedGeneration`.
- Deletion sets `deletionTimestamp`; the owner of each finalizer removes it when its work is done. Ownership and garbage collection use `ownerReferences`.
- Enum values are PascalCase, fields camelCase, quantities in `500m` and `4Gi` form, and detail is carried in `conditions`.

### Machine

- **`spec`:** image, kernel, resources (requests and limits for cpu and memory), root disk size, volumes and mounts, networks, ports, `runStrategy`, `idleTimeoutSeconds`, `nodeSelector`, `requiredCapabilities`.
- **`runStrategy`:** `Always`, `RerunOnFailure`, `Once`, `OnDemand`, `Halted`.
- **Set by `/binding` only:** `spec.nodeName` and `spec.bindingEpoch`.
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

- **OnDemand, what the Store holds:** the phase on each transition, and `lastActiveTime` no more often than a configured minimum interval. Momentary activity is not in the Store; the node serves it on the data plane.
- **OnDemand, a process that exits by itself:** the Machine goes to `Suspended`, keeping its disk. The exit code and time go into status, and the next demand signal starts it. Exits that repeat without demand back off before the next start.
- **Without snapshot support,** `Suspended` means the disk is kept and the process ended; only resume speed differs.

### Volume

- **`spec`:** storage class, access modes, requested storage, data source, `nodeName` (node-local classes only, set by `/binding`).
- **`status`:** `phase` (`Pending`, `Available`, `Bound`, `Released`, `Failed`), `attachment` (machine name and binding epoch), capacity.
- **Attachment is a compare-and-swap by the Node Agent** on `status.attachment`. It succeeds only if `attachment` is empty or its epoch is lower than the Machine's current epoch.
- A node-local volume pins its Machine to that node; moving between nodes is out of the first scope.

### Lease and fencing

- **A Lease per node, separate from `Node`.** It holds a holder, a renew time and a duration, and the Node Agent renews it by compare-and-swap.
- **Validity is measured by the observer.** A Lease is expired when its duration, plus a margin, has elapsed on the observer's monotonic clock since the observer first saw its current `resourceVersion`. The renew time the node wrote is not compared with another machine's clock.
- **The Node Agent's validity bound.** After the Store acknowledges a renewal, the Lease is valid for the duration counted from the start of that renewal call on the Node Agent's own monotonic clock, because the observer sees it no earlier.
- **The fencing obligation.** The Node Agent renews mvmd's node deadline only so that, for each renewal acknowledged at monotonic time `t` with length `L`, `t + L + δ` is no later than the end of validity of the latest acknowledged Lease renewal. mvmd's runtime API gives the bound: a recorded expiry is never later than the acknowledgement time plus the length, and a Machine runs at most δ past it. This keeps invariant 1 when the Node Agent dies.
- **Rebinding waits for expiry.** The epoch increases only after the node's Lease expiry is confirmed.
- **Parameters, no values yet:** Lease duration, renew period, the expiry margin, the deadline length, δ (not measured in mvmd either) and the clock-rate error the margin must cover.

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

Relative to the group and version root; where the namespace appears is an open question.

| Path | Writer | Use |
|---|---|---|
| `PUT /machines/{name}` | users, controllers | create and change `spec` |
| `POST /machines/{name}/binding` | Scheduler | bind and set the epoch |
| `PUT /machines/{name}/status` | Node Agent, controllers | report status, epoch checked |
| `DELETE /machines/{name}` | users | set `deletionTimestamp`; the Node Agent removes its finalizer after release |
| `GET /machines?watch=true` | all | subscribe to changes |
| `PUT /volumes/{name}/status` | Node Agent, controllers | attachment and volume status |
| `PUT /leases/{name}` | Node Agent | renew the node's Lease |

Wake, exec, file API and console are data-plane features and are not recorded in the Store.

### Watch

- A watch resumes from a given `resourceVersion`.
- An expired `resourceVersion` gets an "expired" error; the client lists again and watches from the list's version.

## Rationale

- Boundary, one-way dependencies and the fencing obligation: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- Writers, Lease, watch, OnDemand, group, transport, naming: [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md).
- How `resourceVersion`, compare-and-swap and watch map onto the Store: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- The Lease validity is measured by the observer because a renew time written by one machine cannot be compared with another machine's clock, which can be stepped.

## Open questions

- **Authentication** of API callers is deferred; see Transport.
- **Namespace in the path:** whether the paths carry the namespace and in what form.
- **Event shape of a watch:** the names of event types and the error object that carries "expired", left to the first PR of `lib/api` (issue 3).
- **The pre-registration state:** the `Node` schema, who creates a `Node` for a discovered machine, and what admits it.
- **Unacknowledged renewals:** how the Node Agent bounds the expiry of an mvmd renewal whose acknowledgement never arrived; left to the Node Agent's document.
- **Which unit confirms Lease expiry:** the Scheduler, or the node lifecycle controller; left to those units' documents.
- **Data-plane route:** how exec, file and console traffic reaches a node.
- **Numbers:** Lease duration, renew period, expiry margin, deadline length, δ, clock-rate error, OnDemand backoff and the `lastActiveTime` minimum interval (Discussion #2 section 6 item 13).
- **Store growth and compaction interval:** [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- **Placement of other core resources** (Namespace, roles, resource definitions) in an API group, and the extension resources' schemas.
- **Out of the contract by decision:** Discussion #2 section 6 items 5, 6 and 12 (tenancy, the immediate-bind default, deployment features).
