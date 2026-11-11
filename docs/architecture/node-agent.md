# Node Agent

What `gaon-node-agent` does on a node: it registers the node, keeps its Lease and mvmd's fencing deadline renewed, runs the Machines bound to it on mvmd, and reports their status.

Basis: [resource-api.md](./resource-api.md) for the contract it keeps, [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md) for the boundary. The mvmd statements below hold for garamsh/mvmd at commit `1b08e5e04273b8839f66d9e154bf96086fbc3d23`, runtime API `v1alpha2`, which the agent reaches only through mvmd's `lib/runtimeapi` module over mvmd's Unix socket.

## Current decisions

### Configuration

Every value is a flag with an environment variable as its fallback. A flag given wins over the variable, and the variable over the default. The agent refuses to start when a required value is missing or the durations do not fit each other.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--api-server` | `GAON_API_SERVER` | none, required | root URL of the resource API, up to the group and version |
| `--node-name` | `GAON_NODE_NAME` | none, required | name of this node's Node and Lease |
| `--mvmd-socket` | `GAON_MVMD_SOCKET` | none, required | path of mvmd's runtime API socket |
| `--delta` | `GAON_DELTA` | none, required | δ, how long a Machine can run past mvmd's recorded deadline expiry |
| `--lease-duration` | `GAON_LEASE_DURATION` | 30s | validity of the Lease after a renewal, a whole number of seconds |
| `--renew-period` | `GAON_RENEW_PERIOD` | 5s | time between renewal rounds |
| `--deadline-length` | `GAON_DEADLINE_LENGTH` | 15s | length each deadline renewal asks mvmd for |
| `--rpc-timeout` | `GAON_RPC_TIMEOUT` | 2s | bound on each call of a renewal round |
| `--resync-period` | `GAON_RESYNC_PERIOD` | 10s | time between two reconciliations of the Machines and two reports of the Node's status |

δ has no default because it is not measured, in mvmd either; the operator states the value they accept. The durations must satisfy two inequalities, checked at start: the deadline length exceeds the renew period plus two call timeouts, so the deadline never lapses between rounds, and the Lease duration is at least the deadline length plus δ plus two call timeouts, so a round can always renew the deadline.

### Registration

- The agent creates its Node with an empty `spec`, by a `PUT` without a version, when the API has none, and never writes `spec` again.
- It writes the Node's `status` by compare-and-swap on every resync: `state` is `Admitted` when a user has set `spec.admitted` and `Discovered` otherwise, `capacity` comes from mvmd's runtime info (cpus as a whole number, memory and storage in bytes, storage being mvmd's state total), and the `Ready` condition is true while mvmd answers. When mvmd's runtime info fails, the agent writes `Ready` false with reason `RuntimeUnreachable` and the error as the message, keeps the stored `capacity`, and keeps the condition's transition time while its status and reason are unchanged.
- It creates the Lease named as the Node on its first round and renews it by compare-and-swap, writing the node as holder.

### The fencing obligation

- **One loop** renews the Lease first and then, in the same round, mvmd's deadline.
- **Validity of the Lease** is counted from the start of the latest acknowledged renewal call on the agent's monotonic clock, as [resource-api.md](./resource-api.md) states.
- **A deadline renewal is sent only if, were mvmd to receive it as late as the call timeout allows, its expiry plus δ is within that validity.** That is the clock reading now, plus the call timeout, the deadline length and δ, no later than the validity. This bounds a renewal whose acknowledgement never arrives, which resource-api.md leaves to this document: mvmd may have recorded it, and it was recorded no later than the call timeout after it was sent.
- **When the Lease cannot be renewed**, validity stops growing, the check refuses every further deadline renewal once the margin is gone, and mvmd's deadline lapses on its own. The Machines are fenced by mvmd, not by the agent's survival. A failed Lease call makes the agent read the Lease again before the next write.
- **Every call of a round is bounded** by the call timeout, including the read of mvmd's last applied number, so a runtime that does not answer ends the round with an error and the next round renews the Lease.
- **The renewal number** sent to mvmd increases monotonically; the agent reads mvmd's last applied number once at start, so a restarted agent continues the sequence.
- mvmd refuses to start or restore a Machine while its deadline is not armed or has expired; the agent treats that refusal as a failure to retry on the next resync, and the Machine stays `Pending`.

### Machines

- **Watched set:** the agent lists the Machines, watches from the list's version and reconciles on every change and every resync. A failed or expired watch is followed by a new list.
- **Identity in mvmd:** a Machine is held in mvmd as its name with the binding epoch appended. A binding at a later epoch is therefore a different mvmd machine, and the earlier one is removed, never reused.
- **Epoch rule:** the agent keeps the newest epoch it has seen for each Machine and ignores an event, a list entry or a re-read carrying an older one.
- **Run:** a Machine bound to this node, not being deleted and not `OnDemand`, has its image pulled by digest, is created in mvmd and started, according to `runStrategy`: `Halted` is stopped and not created, `Once` is not started again after it stopped, the others are started whenever mvmd holds them stopped or created.
- **Remove:** every mvmd machine the agent named that is not a Machine bound here now, whether unbound, bound elsewhere, bound at an older epoch, being deleted or no longer in the API, is stopped and deleted. This also clears what an earlier run left. An mvmd machine whose identifier the agent did not form is left alone.
- **Status:** the agent writes `phase`, `bindingEpoch` equal to the spec's, `observedGeneration`, `addresses` and a `Ready` condition, only when they differ from what is stored.

| mvmd state | Phase | Ready |
|---|---|---|
| created | `Pending` | false |
| running | `Running` | true |
| paused | `Suspended` | false |
| stopped, `Halted` | `Stopped` | false |
| stopped, `Once` | `Succeeded` | false |
| stopped, other | `Pending` | false, started again on the next resync |
| faulted | `Failed` | false, the reason is mvmd's last failure reason |

- **Failures the agent settles itself** go to `Failed` with the `Ready` condition false and a reason: `InvalidSpec` for an image that is not `<repository>@sha256:<digest>` or resources it cannot convert, and `Rejected` for a create mvmd refuses, such as one above a Driver's `max_vcpus`, with mvmd's reason as the message. The Driver's limit is not on the Node's status.
- **Outcome kind `IN_PROGRESS`** is treated as never produced by mvmd (garamsh/mvmd#307), and no exec output is read (garamsh/mvmd#308).

### What the agent does not do yet

- `OnDemand` Machines, wake and suspend.
- Volumes and attachment, and the network policy: a Machine is created with none, so invariant 6 is not kept by the agent.
- Restart with backoff: a faulted Machine stays `Failed`, `restartCount` and the last exit are not written, and a Machine that stopped under `Always` is started again at the next resync.
- Removing the agent's finalizer from a deleted Machine, and the Node's `addresses`.
- Calling naru.

## Rationale

- One loop for the Lease and the deadline keeps the obligation checkable in one place: the deadline is never renewed outside a round that has just seen the Lease's validity.
- The call-timeout margin pays for not knowing whether an unacknowledged call reached mvmd, instead of asking mvmd afterwards, which a failing path may not allow.
- The epoch in the mvmd identifier makes rebinding to the same node a replacement rather than a reuse, so state of an older epoch cannot carry into a newer one.

## Open questions

- **API root:** whether `--api-server` carries the group and version or the agent appends them, pending the API server's path decision.
- **δ:** the value to run with, until mvmd measures it.
- **Finalizer:** how the agent removes its finalizer from a Machine, since the API has no path for it yet.
- **Restart policy:** the backoff and the exit record for `RerunOnFailure` and `Always`.
- **Node addresses:** which addresses the agent reports.
