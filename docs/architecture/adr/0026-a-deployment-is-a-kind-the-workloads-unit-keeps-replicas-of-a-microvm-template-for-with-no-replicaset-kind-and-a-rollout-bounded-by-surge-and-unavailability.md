# ADR 0026: A Deployment is a kind the workloads unit keeps replicas of a MicroVM template for, with no ReplicaSet kind and a rollout bounded by surge and unavailability

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#171.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A MicroVM runs once and stays where it is.** A user who wants three of them, or wants them replaced when one fails or is deleted, writes the loop. Changing the image means deleting and creating by hand, with no bound on how many are down at once.
- **The workloads unit registers kinds, runs them as a client of the public API and acts only while it holds a Lease** ([ADR 0024](./0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md), [ADR 0025](./0025-a-cronjob-is-a-kind-the-workloads-unit-runs-by-creating-a-job-from-its-template-at-each-scheduled-time-only-while-it-leads.md), [ADR 0023](./0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)). A third kind is the same mechanism.
- **The schema subset of a definition has no int-or-string and no `additionalProperties`** ([ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md)). A field that is a number or a percentage has to be one type.
- **A MicroVM being deleted stays stored until its node agent has released it**, so it still holds its place and its resources for a while after the delete.
- **The unit keeps no state of its own and has no fencing beyond the election margin** (ADR 0023, ADR 0024): what it does must follow from the Store, and a repeated create must be harmless.

## Decision

**The unit registers a third definition, `Deployment`, and keeps `spec.replicas` MicroVMs of `spec.template` running, replacing them one bounded step per sweep when the template changes. There is no ReplicaSet kind: the Deployment owns its MicroVMs directly, and a label records which template each was made from.**

1. **The kind.** `Deployment`, plural `deployments`, group `workloads.gaon.garam.sh`, version `v1alpha1`, cluster scoped, with a `status` subresource.
   - `spec.replicas` (required, at least 0), `spec.selector.matchLabels` (required, must not be empty), `spec.template` (required, a MicroVM spec; `runStrategy` is forced to `Always` and the binding is cleared), `spec.strategy`.
   - `spec.strategy.type` is `RollingUpdate` (absent is this) or `Recreate`. `rollingUpdate.maxSurge` and `maxUnavailable` are each a whole number or a whole number of percent, written as a string and checked by the schema's `pattern`; each defaults to `25%`. A surge percentage rounds up and an unavailability percentage rounds down; when both come to zero one replica may be unavailable, so that a rollout can move. `Recreate` is a surge of 0 and an unavailability of `replicas`.
   - `status`: `observedGeneration` (the object's `metadata.generation` when the counts were taken), `replicas`, `readyReplicas`, `updatedReplicas`, `availableReplicas`.
2. **The replicas are MicroVMs the unit creates as a client of the public API**, each named `<deployment>-<index>` (the first free index), owned by the Deployment with its `uid`, and labeled `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/deployment=<name>`, `workloads.gaon.garam.sh/template-hash=<hash>` and the selector's labels. The hash is the first ten hexadecimal characters of the SHA-256 of the replica template as JSON. `AlreadyExists` counts as created.
3. **A replica is ready** when its Ready condition is True in a status of the spec's epoch, and **available** when ready and not being deleted; there is no minimum ready time, so available equals ready. **A replica is failed** when its effective phase is Failed. **Updated** means its hash label is the current template's.
4. **One sweep, one step.** For each Deployment the unit derives the fleet from the owned MicroVMs listed, plans, then acts:
   - It deletes the Failed replicas.
   - It deletes the replicas above the allowance: `replicas`, or `replicas + maxSurge` while a replica of another template is present. It takes those not ready first, then those of another template, then the highest names.
   - It deletes the replicas of another template that are not ready, and those that are ready while that leaves no fewer than `replicas - maxUnavailable` ready.
   - It creates replicas of the current template, up to `replicas`, while no more than `replicas + maxSurge` MicroVMs exist, **those being deleted and those failed included**: each still holds its place until it is gone.
5. **The bounds hold at every write**, not only when a rollout ends. A replica counts toward the surge until it is gone, and counts toward availability only while ready and not being deleted, so the plan never takes the Ready count below `replicas - maxUnavailable` nor the MicroVM count above `replicas + maxSurge`. `Recreate` follows: with no surge, nothing is created until every replica of the old template is gone.
6. **Status** is the counts taken from the fleet before the step, with the generation read, written only when it differs from the stored one. It is one sweep behind the Store by design.
7. **An unusable Deployment** (an empty selector, a strategy type or bound the pattern should have refused, a spec that does not decode) creates and deletes nothing, is logged, and does not stop the others.
8. **Election and role.** The unit is leader-only as before. Because every decision is read from the Store and a name is deterministic, a new leader continues a rollout where the old one stopped, and a create the old leader got in just before its term ended answers `AlreadyExists`. The role `gaon:workloads` gains `delete` of MicroVMs carrying the unit's label, `list` of `deployments` and `update` of `deployments/status`.

## Consequences

- **A rollout is as fast as the replicas become ready.** A new replica that never becomes ready holds the rollout, with the surge replica kept, until a user changes the spec; there is no progress deadline and no rollback.
- **There is no rollout history and no `kubectl rollout undo`.** The previous template is not kept anywhere; to go back, a user writes it into the spec.
- **A change of the selector does not adopt or release MicroVMs.** Ownership is by owner reference and `uid`; the selector only decides the labels new replicas carry.
- **The status is a sweep behind**, so `observedGeneration` equal to the generation says the counts were taken for this spec, and not that the rollout is done.
- **A bound is a string.** `maxSurge: 1` is refused by the schema's type check; a user writes `"1"`. This follows from the schema subset and is a gap to close in `lib/api`, not here.
- **A percentage of a large fleet rounds as the integers do**, so a 10% surge of 5 replicas is 1.
- [deployment.md](../deployment.md) is unchanged: the unit, its flags and certificate are the same.

## Signs the decision was wrong

- Users need to keep the old replicas of a failed rollout, pause one, or roll back, and a second owner level (a ReplicaSet kind) would carry that.
- Two Deployments' MicroVMs are confused because the owner name and `uid` are the only separation, or users adopt MicroVMs by label.
- A rollout stalls on a template that never becomes ready often enough that a progress deadline is wanted.
- Reading the template hash from a label breaks when the template schema gains a field the hash does not cover.

## Rejected alternatives

- **A ReplicaSet kind between the Deployment and the MicroVMs.** It is how Kubernetes keeps history and does rollback: a Deployment makes one ReplicaSet per template and moves the counts between them. Here the history and rollback are not asked for, a second kind needs its own definition, status, role and ownership chain, and the MicroVM already carries the hash of its template. Dropping the kind keeps the rollout one pass over one list. If history or rollback is wanted, this decision is superseded by one that adds the kind.
- **An int-or-string `maxSurge`.** The schema subset has no such type; a string with a `pattern` is what it allows.
- **Counting only live MicroVMs toward the surge.** A MicroVM being deleted still holds its place on its node, so a rollout that counted only live ones would exceed the surge in fact.
- **Treating unready MicroVMs as available.** It would let a rollout remove the old replicas while the new ones are still starting, and the Ready count would fall below the bound.
- **A watch per Deployment.** The unit already sweeps on an interval.
