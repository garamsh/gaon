# ADR 0030: A StatefulSet is a kind the workloads unit keeps named replicas of a MicroVM template for, each with a Volume it keeps when the replica goes

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#172.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A Deployment's replicas are interchangeable**: each is named by the first free index, holds nothing, and is replaced by any other ([ADR 0026](./0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md)). A database or a queue needs the opposite: a replica that keeps its name and its disk when it is replaced, and an order in which replicas start and stop.
- **The workloads unit registers kinds, runs them as a client of the public API and acts only while it holds a Lease** ([ADR 0024](./0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md), [ADR 0023](./0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)). It keeps no state of its own, and a repeated create must be harmless.
- **A MicroVM being deleted stays stored until its node agent has released it**, and it holds its name until then.
- **A Volume is a kind of the compute group** with a class, access modes and a size, created by a user and kept until deleted ([ADR 0013](./0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md)). Its spec is immutable and it is attached to at most one (MicroVM, epoch). Nothing in the Volume kind needs to change for a replica to get one.
- **The garbage collector deletes the objects an owner reference points at when the owner is gone.**
- **The schema subset has no int-or-string and no `additionalProperties`** ([ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md)).

## Decision

**The unit registers a fourth definition, `StatefulSet`, and keeps `spec.replicas` MicroVMs named `<name>-<ordinal>` from `spec.template`, each with a Volume of its own per claim template that the unit creates once and never deletes.**

1. **The kind.** `StatefulSet`, plural `statefulsets`, group `workloads.gaon.garam.sh`, version `v1alpha1`, cluster scoped, with a `status` subresource.
   - `spec.replicas` (required, at least 0), `spec.selector.matchLabels` (required, must not be empty), `spec.template` (required, a MicroVM spec; `runStrategy` is forced to `Always` and the binding is cleared).
   - `spec.volumeClaimTemplates`: a list of `{name, spec}`, `spec` being a Volume spec (`storageClass` required). The entry of `spec.template.volumes` of the same name is the mount; each name is set, unique, and mounted by exactly such an entry.
   - `spec.podManagementPolicy`: `OrderedReady` (absent is this) or `Parallel`.
   - `spec.updateStrategy.type`: `RollingUpdate` (absent is this) or `OnDelete`; `rollingUpdate.partition` (at least 0; absent is 0).
   - `status`: `observedGeneration`, `replicas`, `readyReplicas`, `updatedReplicas`, counted as a Deployment's are.
2. **Names and identity.** Replica `i` is the MicroVM `<name>-<i>`, for `i` from 0 to `replicas - 1`. Its owner reference carries the StatefulSet's `uid` and it is labeled `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/statefulset=<name>`, `workloads.gaon.garam.sh/ordinal=<i>`, `workloads.gaon.garam.sh/replica-name=<name>-<i>`, `workloads.gaon.garam.sh/template-hash=<hash>` and the selector's labels. The ordinal and the name are the identity a DNS service reads; serving DNS is not this decision. A MicroVM the set owns whose name is not `<name>-<n>` with `n` written without sign or leading zero is not a replica and is left alone.
3. **Volumes.** Replica `i` mounts the Volume `<claim>-<name>-<i>`: the unit renames the template's volume entry of the claim's name to it. Before it creates a replica the unit creates each missing Volume, labeled `managed-by=gaon-workloads` and `workloads.gaon.garam.sh/statefulset=<name>`, with the claim's spec. A Volume is created only when none of its name is listed and `AlreadyExists` counts as created. **The unit never deletes a Volume and gives it no owner reference**, so neither a replacement, a scale-down, nor the deletion of the StatefulSet removes one; a user deletes the Volumes.
4. **A replacement keeps its name and waits for the release.** A replica is missing when no MicroVM of its name exists. One being deleted still exists, so nothing is created under its name until its node agent has released it and it is gone; the Volume's single attachment is released by the same agent. The replacement then mounts the same Volume. The Volume's binding to a Node, which the Volume kind keeps, decides where it runs.
5. **`OrderedReady`.**
   - Creation: the lowest missing ordinal is created only when every lower ordinal exists and is Ready (not being deleted). One is created per sweep.
   - Scale-down: the highest ordinal above `replicas` is deleted, one per sweep, the next only once it is gone.
   - A failed replica is deleted and made again under its name.
6. **`Parallel`.** Every missing ordinal is created in the same sweep and every ordinal above `replicas` is deleted in the same sweep.
7. **Updates.** A replica is updated when its template-hash label is the current template's.
   - `OnDelete` replaces nothing. A replica a user deletes is made again from the current template.
   - `RollingUpdate` considers the ordinals from `replicas - 1` down to `partition`. While any ordinal is missing or above the count it does nothing else. It deletes the highest ordinal that is not updated, and only while every other ordinal below `replicas` exists, is Ready and is not being deleted, so at most one replica is down. Ordinals below `partition` are never deleted for a rollout.
8. **Status** is written as ADR 0026 writes a Deployment's. **An unusable StatefulSet** (an empty selector, a policy, update type or partition the schema should have refused, a claim template mounted by no volume or named twice, a spec that does not decode) creates and deletes nothing, is logged, and does not stop the others.
9. **Election and role.** The unit is leader-only as before; every decision is read from the Store and every name is deterministic, so a new leader continues an ordered creation or a rollout where the old one stopped. The role `gaon:workloads` gains `list` of `statefulsets`, `update` of `statefulsets/status`, and `create` and `list` of Volumes labeled `managed-by=gaon-workloads`, with no `delete`.

## Consequences

- **A replica below `partition` that is deleted is made from the current template**, not the one it ran. No revision history is kept, so there is no older template to make it from. Kubernetes restores the older revision; this deviates, and a user who needs it keeps `partition` and does not delete.
- **Scale-down does not wait for the lower ordinals to be Ready.** It removes the highest ordinal when the count drops, whatever the state below. Kubernetes waits; here the removal of an extra is never made to wait on an unhealthy replica.
- **A rollout stalls on a replica that is not Ready.** It replaces nothing while another replica is down, so a template that never becomes Ready holds the rollout at the replica it replaced, until the spec changes. There is no progress deadline.
- **Volumes accumulate.** Scaling down and deleting a StatefulSet leave its Volumes, which a user deletes. Scaling up again finds the Volume of that ordinal and its data.
- **A Volume's claim spec is read only when it is created.** Changing `volumeClaimTemplates` does not change an existing Volume, whose spec is immutable.
- **The identity is in labels**, not in DNS, a field of the MicroVM spec or a hostname. The compute group is not changed.
- **A mount is a renamed entry.** The template's volume entry of a claim's name is never a Volume name itself; a user does not write a Volume named like a claim.
- [deployment.md](../deployment.md) is unchanged.

## Signs the decision was wrong

- Users want a Volume removed with its StatefulSet or on scale-down often enough that a retention policy is wanted.
- A rollout must restore the template a replica below `partition` ran, which needs a revision kept.
- Something serving DNS needs the identity in the MicroVM spec and not in labels.
- A Volume's pinning to a Node keeps a replacement where its Node is gone, and the replica needs a way to be bound elsewhere.

## Rejected alternatives

- **An owner reference from the StatefulSet to each Volume.** It would make the garbage collector delete the data when the set is deleted, and the scale-down guarantee would depend on the collector not running. Deleting data is a user's act.
- **Deleting a replica's Volume on scale-down.** The next scale-up would start empty, which is not what a stateful replica is for.
- **A claim kind between the template and the Volume**, as Kubernetes has. The Volume kind already is what a claim names; a second kind needs its own definition, status and role.
- **Naming a replica by the first free index**, as a Deployment does. A replacement would take any free name and the Volume would follow the index, not the replica.
- **A revision kind for updates.** It carries history and the restoration of an older template, which is not asked for; the hash label tells an updated replica from another.
- **A watch per StatefulSet.** The unit already sweeps on an interval.
