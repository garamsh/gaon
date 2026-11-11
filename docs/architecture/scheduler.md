# Scheduler

What the Scheduler binds, the placement rule, and when it rebinds: the responsibility of `scheduler/`, binary `gaon-scheduler`.

Basis: [resource-api.md](./resource-api.md) §Machine, §Node, §Lease and fencing and §Paths, with the writers of [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). The Scheduler reaches the API only through `lib/api`, and never reads the Store.

## Current decisions

### What it does

- Each pass lists the Nodes and the Machines, and binds each Machine that needs a binding through `POST /machines/{name}/binding`. It is the only writer of `/binding`.
- It polls: `gaon-scheduler` runs a pass at start and then every `-interval` (default 5 seconds). A pass holds no state from the one before.
- A Machine with `deletionTimestamp` set is left alone.

### Placement rule

- A Node is eligible only if its `status.state` is `Admitted`, its `Ready` condition is `True`, and its `LeaseExpired` condition is not `True`. A missing condition is not `True`.
- A Node's free capacity is `status.capacity` cpu and memory, less the `spec.resources.requests` cpu and memory of every Machine whose `spec.nodeName` is that Node, whatever its phase.
- A Machine fits a Node when its requests are no more than the free capacity in both cpu and memory. Quantities are compared by value (`1Gi` holds `1G`, `1` holds `1000m`). An empty quantity is zero.
- The Machine goes to the first fitting eligible Node by name. A binding made in a pass is charged to its Node for the rest of that pass.
- Nothing else is read: no spreading, affinity, priority, preemption or overcommit.

### Binding and epochs

- A binding names the Node, the Machine's `resourceVersion` as read, and the Machine's current `spec.bindingEpoch` plus one. The first binding of a Machine is epoch 1.
- The binding is a compare-and-swap, so two schedulers racing on one Machine produce one binding. The one that loses gets a `Conflict`, which is not a failure: the next pass reads the Machine as it then is. A `NotFound` is treated the same way.

### Rebinding

- A Machine that has `spec.nodeName` is rebound only when the Node it names has `LeaseExpired` `True`. The node lifecycle controller sets that condition; the Scheduler never reads a Lease and never measures expiry.
- A Machine whose Node is not expired, has no `LeaseExpired` condition, has it `False` or `Unknown`, or no longer exists, is left bound.
- The new epoch is the current one plus one, so it is greater than every earlier binding of that Machine.
- A Machine that fits no other Node stays bound to the expired Node and is tried again next pass. Its status belongs to the dead Node's epoch and is not rewritten.

### A Machine that fits no Node

- It stays unbound. The Scheduler writes `PUT /machines/{name}/status` with a `Scheduled` condition, status `False`, reason `Unschedulable`, whose message gives how many Nodes are eligible of how many exist and the requests that fit none. It sets `phase` to `Pending` when the status has none.
- A pass whose condition is identical writes nothing.

## Rationale

- Rebinding waits for a confirmation made by another unit because Lease expiry is measured by an observer ([resource-api.md](./resource-api.md) §Lease and fencing), and invariant 1 depends on it.
- A pass that rebuilds the view from two lists, instead of watching, needs no cache to keep consistent, and the binding's compare-and-swap makes a stale view harmless.

## Open questions

- **Driver limits.** garamsh/mvmd#306 has merged, but the Node schema carries no Driver limits, so placement stays within Node capacity and a Create mvmd refuses shows in the Machine's status.
- **Node `storage` and the root disk.** `status.capacity.storage` and `spec.rootDiskSize` are not compared; only requests are.
- **`nodeSelector` and `requiredCapabilities`.** The Node schema has no labels or capabilities to match them against, so they are not read.
- **Who writes the unbound Machine's `/status`.** [resource-api.md](./resource-api.md) §Common rules names the Node Agent and controllers; the Scheduler writes the `Scheduled` condition because the issue asks for the reason in the status.
- **A stale `Scheduled` condition.** Binding a Machine does not clear its `Unschedulable` condition.
- **Watching instead of polling,** when the poll interval becomes a cost or a delay worth removing.
