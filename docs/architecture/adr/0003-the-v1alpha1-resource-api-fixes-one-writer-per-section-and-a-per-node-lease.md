# ADR 0003: The v1alpha1 resource API fixes one writer per section and a per-node Lease

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#2. The contract basis is section 4 of garamsh/discussions Discussion #2 ([comment](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288320)), decided by the project owner in the gaon PM session of 2026-11-08. The decisions that fix its open points are the project owner's, taken in the gaon PM session of 2026-11-11 and recorded on garamsh/gaon#2 (the issue's Context and the PM comment of that date).

## Context

- **Section 4 is a draft by an AI author**, adopted as the basis. Its node-side writer is now gaon's own Node Agent ([ADR 0002](./0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)), and node liveness is decided to be a Lease resource.
- **Two items were open:** how OnDemand momentary state reaches the Store, and what happens when an OnDemand process exits by itself (section 6 item 7, [comment](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19288322)). They decide what `/status` carries.
- **The API group was a placeholder** (`example.io`), and transport and authentication were not stated.

## Decision

**The `v1alpha1` contract is section 4's, with the following fixed.**

1. **One writer per section of a resource.**
   - `spec`: users and controllers.
   - `/binding`: the Scheduler.
   - `/status`: the Node Agent for what runs on its node, controllers for the rest.
   - Every write is a compare-and-swap on `resourceVersion`.
2. **A Volume's `status.attachment` is set by the Node Agent by compare-and-swap.** It succeeds only if `attachment` is empty or its epoch is lower than the Machine's current epoch.
3. **A node's liveness is a Lease resource, separate from `Node`.** It holds a holder, a renew time and a duration, and is renewed by compare-and-swap. It is not a heartbeat in `Node.status`.
4. **Watch resumes from a `resourceVersion`.** When that version has expired the server answers with an "expired" error, and the client lists again and watches from the list's version. How the Store produces this is in [ADR 0004](./0004-the-store-is-etcd-in-one-cell-of-one-member.md).
5. **Deletion** sets `deletionTimestamp`. The owner of each finalizer removes it once its work is done; the Node Agent removes its own after the Machine is stopped and its volumes released.
6. **OnDemand, what the Store holds:** the coarse phase (`Running`, `Suspended`) and `status.lastActiveTime`. The Node Agent writes the phase on each transition and `lastActiveTime` no more often than a configured minimum interval. Momentary activity is not in the Store; the node serves it on the data plane.
7. **OnDemand, when the process exits by itself:** the Machine goes to `Suspended` and keeps its disk. The exit code and time are recorded in status, and the next demand signal starts it again. Exits that repeat without demand back off before the next start.
8. **API group:** `gaon.garam.sh`, keeping section 4's area prefixes: `compute.gaon.garam.sh/v1alpha1`, `apps.gaon.garam.sh`, `apiextensions.gaon.garam.sh`. An API group is a DNS subdomain of a domain its owner holds, and `garam.sh` is the organisation's registered domain (`dig +short garam.sh NS` returned Cloudflare name servers on 2026-11-11, as recorded on the issue).
9. **Transport:** HTTP with JSON bodies on the paths in [resource-api.md](../resource-api.md) §Paths. A watch is a streaming HTTP response of newline-delimited JSON events.
10. **Authentication is deferred.** Until decided, the API server listens only on a local address.
11. **Naming:** `Machine` is the microVM. A physical machine is the core resource `Node`, and a machine naru has discovered but gaon has not admitted is a pre-registration state of `Node`. There is no separate Host resource.
12. **Numbers are parameters, not values:** Lease duration, renew period, δ and the OnDemand backoff (section 6 item 13).

## Consequences

- A stale or foreign writer is refused by the server, so a dead node's status cannot overwrite the new binding's.
- `/status` stays small: phase changes and a throttled `lastActiveTime`, so OnDemand does not load the Store with every wake.
- A Machine that exits by itself under `OnDemand` is not `Failed`; a crash loop shows as repeated exit records and growing backoff.
- Every consumer must handle the "expired" error on watch.
- Nothing authenticates callers until the deferral is lifted, so the API server is reachable only locally.
- Changing a point fixed above within `v1alpha1` is a new ADR; the numbers in 12 are fixed nowhere yet.

## Signs the decision was wrong

- A client cannot tell from `/status` whether an OnDemand Machine exited by itself or was suspended for idleness.
- The throttled `lastActiveTime` is too coarse for a controller that needs it, and the controller works around it.
- Two writers are found changing one section of a resource in practice.
- The Lease's compare-and-swap rate cannot be sustained by one etcd member at the planned node count.
