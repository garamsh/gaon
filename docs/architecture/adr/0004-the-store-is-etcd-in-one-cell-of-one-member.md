# ADR 0004: The Store is etcd, starting as one cell of one member

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#2. The decisions are the project owner's, taken in the gaon PM session of 2026-11-11 and recorded on that issue. They answer item 4 of the open questions in garamsh/discussions Discussion #2 ([section 6](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288322)) in part.

## Context

- **The contract needs three things of the Store:** a version on every object that a write can be conditioned on, a way to watch changes from a given version, and an atomic conditional write ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md)).
- **The Store technology, the size of a cell and the global tier were open** (section 6 item 4).

## Decision

**The Store is etcd, and gaon starts with one cell holding a single etcd member.**

1. **`resourceVersion` is the etcd revision at which the object was last modified.**
2. **A compare-and-swap is an etcd transaction that compares that revision** and writes only if it is unchanged.
3. **A watch from a `resourceVersion` is an etcd watch** that delivers the changes made after that revision.
4. **A `resourceVersion` older than etcd's compaction is answered with an "expired" error.** The client lists again and watches from the list's version.
5. **Left open:** the compaction interval, and how the cell grows (more members, more cells, the global tier).

## Consequences

- One member has no replication, so its loss makes the Store unavailable. Nodes then cannot renew their Leases, and mvmd stops each node's Machines when its deadline lapses ([ADR 0002](./0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
- `resourceVersion` is one counter for the whole cell. Two versions of one object are not consecutive, so clients treat the value as opaque and compare it only for equality.
- The compaction interval bounds how long a client may be disconnected before it must list again.
- A second member or cell changes what the Store guarantees and needs its own ADR.

## Signs the decision was wrong

- The single member's availability is shown to dominate the cell's outages before the cell has a way to grow.
- A Store other than etcd is needed, and the contract's wording assumed etcd's revision.
- Clients cannot recover from the "expired" error within the Lease duration at the chosen compaction interval.
