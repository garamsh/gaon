# Node lifecycle

How the node lifecycle controller confirms that a Node's Lease has expired, the condition it records, and why that keeps a MicroVM from running on two nodes. The controller is in `controllers/` and its binary is `gaon-controllers`.

Basis: [resource-api.md](./resource-api.md) §Lease and fencing and §Node, and [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md) for the fencing obligation.

## Current decisions

### Responsibility

- **The node lifecycle controller confirms Lease expiry.** It is the one unit that does. The Scheduler reads the result from the Node and does not measure a Lease.
- **It writes one thing**: the Node condition `LeaseExpired`, through `PUT /nodes/{name}/status`, by compare-and-swap on the Node it read. It re-reads and tries again when the write conflicts.
- **It never writes `/binding`**, a Node's `spec` or any MicroVM. Only the Scheduler writes `/binding`, and the Scheduler places a MicroVM only on a Node without `LeaseExpired` true ([resource-api.md](./resource-api.md) §Node).
- It uses the public resource API through `lib/api` and imports no other unit.

### Timing

- **The controller is the observer.** It watches `Lease` and `Node` and measures validity on its own monotonic clock. The renew time a Lease carries is not read.
- **A Lease's validity starts at the controller's first sighting of its current `resourceVersion`**, whether in a list or in a watch event, and lasts the Lease's `durationSeconds` plus a margin. A new `resourceVersion` is a renewal and starts the count again. An event that repeats a version it already saw changes nothing.
- **The condition becomes True when the validity has elapsed**, at that instant and not before. The controller sets a timer for the earliest deadline, so the write follows the expiry by the time of the API calls, which is within one renew period.
- **A Lease the controller has no sighting of is not expired by that Lease's rule.** After a restart, listing counts as a first sighting, so validity restarts from the list. This only delays confirmation, which is the safe direction.
- **A Node whose Lease the controller has not seen** counts validity from its first sighting of the Node, in a list or a watch event, for the default Lease duration plus the margin. An event that repeats a Node it already saw changes nothing. A Node's deletion ends the count. The Node Agent never renews mvmd's deadline without an acknowledged Lease renewal, so no MicroVM runs on such a Node and marking it never early keeps invariant 1.
- **Once a Lease is seen**, its rule applies as above: the Lease's own `durationSeconds` plus the margin counts from the first sighting of its version, and the first sighting replaces the Node's count. A Lease cannot be deleted through the API, so "never seen" is the only case without a Lease.
- **The binary reaches the API** over plain HTTP, or over mutual TLS 1.3 when `-tls-ca`, `-tls-cert` and `-tls-key` are all given, with a client `lib/api` builds. Given one or two of them, or a file that cannot be read or does not fit, it exits with a usage error naming what is missing. The flags have no environment variable, and the client sets no timeout because the controller watches.
- **The numbers are flags of the binary**, until they are measured: `-lease-margin` defaults to 5s and `-default-lease-duration` to 30s.
- **A failed write is retried** after a fixed delay (`nodelifecycle.RetryDelay`), and a failed list or watch lists again after the same delay. An `Expired` watch error and a stream that ends take the same path.

### The condition

- **`LeaseExpired` True** while the Lease validity has passed, with reason `LeaseExpired`, or, for a Node whose Lease was not seen within the default validity, with reason `LeaseMissing`.
- **`LeaseExpired` False** once a new version of the Lease is seen, or the Lease is first seen, with reason `LeaseRenewed`. The condition stays in the Node's status with its transition time.
- **A Node with no `LeaseExpired` condition counts as False.** The controller adds the condition only to set it True.
- **At start the controller reads each Node's condition once** and clears a True left by an earlier run while the Lease it has just seen is valid. A condition already at the wanted value is not written again.
- The Node Agent's conditions and fields in the Node's status, `status.state` included, are kept as read.

### Fencing argument

- The obligation: for each mvmd renewal acknowledged at monotonic time `t` with length `L`, the Node Agent keeps `t + L + δ` no later than the end of validity of its latest acknowledged Lease renewal ([resource-api.md](./resource-api.md) §Lease and fencing). After that point the MicroVM's old copy has stopped, even when the Node Agent is dead.
- The controller sees a renewal no earlier than the Node Agent made it, so its deadline, counted from its own first sighting, is no earlier than the end of the validity the Node Agent bounded itself by. The margin covers clock-rate error between the two clocks.
- `LeaseExpired` becomes True no earlier than that deadline, so a MicroVM is rebound, with the epoch plus one, only after its old copy has stopped. That keeps invariant 1.
- The argument fails if the controller's validity ends before the Node Agent's does. The condition is therefore never set early, and the tests fail if it is.

## Rationale

- Lease validity is measured by the observer because a renew time written by one machine cannot be compared with another machine's clock ([resource-api.md](./resource-api.md) §Rationale).
- The controller, not the Scheduler, confirms expiry: the Scheduler writes `/binding` only and reads the Node, and writing a Node's `/status` is a controller's section ([resource-api.md](./resource-api.md) §Common rules).
- It counts from the first sighting of a version and not from the renew time because only that is the observer's own clock.
- A Node with no Lease counts from its first sighting for the same reason, and for a duration the controller holds itself because there is no Lease to carry one.

## Open questions

- **A Lease whose Node does not exist** is retried at the retry delay until the Node appears or the Lease is deleted.
