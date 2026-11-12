# ADR 0013: A Volume is created through its own path, a local Volume is a disk its Node's agent makes, and a binding pins it to a Node

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#91, under goal garamsh/gaon#74 (step 5). The design is the PM's, taken under the project owner's decisions in #74 (Kubernetes semantics, a gaon-native implementation, additive only, no namespaces) and recorded in #91 §Design. It completes the Volume of [ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md), which gave a Volume a status path and no create path, and uses the authorization of [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md). This ADR is the contract; serving it in the API server, scheduling by it and creating the disk are later issues of #74.

## Context

- **A Volume cannot come into being.** [resource-api.md](../resource-api.md) §Volume names a status path only, and [apiserver.md](../apiserver.md) §Open questions records that whoever creates Volumes needs a path.
- **A Machine names its Volumes** in `spec.volumes`, and invariant 2 lets one (Machine, epoch) write a Volume. Nothing says where a Volume's disk lives, who makes it, or what happens to it when the Volume is deleted.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12:**
  - "Persistent Volumes", <https://kubernetes.io/docs/concepts/storage/persistent-volumes/>. The footer reads "Last modified June 17, 2026"; the text names Kubernetes 1.37 ("For Kubernetes 1.37, only nfs and hostPath volume types support recycling"). The page's text, extracted from the fetched HTML, is 44376 characters in 918 lines, and all of it was read.
  - "Storage Classes", <https://kubernetes.io/docs/concepts/storage/storage-classes/>. The footer reads "Last modified October 29, 2026"; the text names Kubernetes 1.37 ("Local volumes do not support dynamic provisioning in Kubernetes 1.37"). The extracted text is 21831 characters in 479 lines, and all of it was read.
  - What was taken from them:
    - a volume is an object of its own with a capacity, access modes, a storage class and a phase; the phases are `Available`, `Bound`, `Released` and `Failed`, and a new volume starts `Pending`;
    - a class names the kind of storage, and a volume of a class is served by the provisioner or plugin of that class;
    - `ReadWriteOnce` means the volume can be mounted read-write by a single node;
    - a volume that is in use is protected from deletion: deleting it sets its `deletionTimestamp`, a finalizer (`kubernetes.io/pv-protection`) holds it, and it is removed once nothing is bound to it;
    - a finalizer can also hold a volume whose reclaim policy is `Delete` until the backing storage is deleted (`kubernetes.io/pv-controller`, `external-provisioner.volume.kubernetes.io/finalizer`);
    - the reclaim policy `Delete` removes the object and the storage asset, and is the default of a class;
    - a local volume must say which nodes it can be reached from, `nodeAffinity` of the volume, and a pod using it is scheduled only to a selected node;
    - binding of a local volume waits for the consumer: the `WaitForFirstConsumer` mode delays binding until a Pod using the claim is scheduled, so the scheduler sees all of the Pod's constraints.
  - What the pages do not state, and so is not taken as Kubernetes' behaviour:
    - which of the two finalizers, or whether either, a volume that was never bound carries, and so whether deleting it is immediate;
    - what a pod using a local volume does when its node is lost for good; the pages say a pod is scheduled only to a node the affinity selects, and nothing about moving the data;
    - the error a scheduler or API server answers for a pod bound to a node the volume's affinity excludes; the Storage Classes page says only that a pod given `nodeName` bypasses the scheduler and its claim stays pending;
    - the HTTP status of a write that changes an immutable field of a volume.
  - Where gaon departs from Kubernetes on purpose:
    - there is no claim. A Machine names a Volume directly in `spec.volumes`, so one object does the work of a PersistentVolume and its PersistentVolumeClaim, and `Bound` means attached to a Machine, where Kubernetes' means bound to a claim;
    - a local Volume is provisioned: its Node's agent makes the disk. Kubernetes' local volumes are not provisioned, and a StorageClass there only delays binding;
    - there is no StorageClass object, no `volumeBindingMode` and no `reclaimPolicy`. `spec.storageClass` is a name, and the reclaim policy is `Delete` always. `Retain` and `Recycle` are not designed;
    - the pin is the binding's side effect: `spec.nodeName` is set by `/binding` when a Machine first uses the Volume, which is `WaitForFirstConsumer` for the one class that has a node. Kubernetes' node affinity is set by whoever creates the volume and is a selector, where `nodeName` is one Node;
    - `nodeName` never changes once set. Kubernetes' page describes a mutable node affinity, an alpha feature since v1.35, off by default;
    - `local` has one access mode, `ReadWriteOnce`, and no `ReadOnlyMany`, `ReadWriteMany` or `ReadWriteOncePod`;
    - there is no expansion (`allowVolumeExpansion`), no mount options, no data source for `local` and no `Recycle`.

## Decision

**A Volume is created and deleted by users through `PUT` and `DELETE /volumes/{name}`; a Volume of the class `local` is a disk of `spec.storage` bytes on one Node, which that Node's agent makes through mvmd and formats ext4; the binding of the first Machine that uses it pins it to that Node and adds the finalizer that makes the agent delete the disk.**

1. **Paths.** Beside `PUT /volumes/{name}/status`, the compute group serves `PUT /volumes/{name}` (create with no `resourceVersion`, change with one), `GET /volumes/{name}`, `GET /volumes`, `GET /volumes?watch=true` and `DELETE /volumes/{name}`, with the compare-and-swap, the watch and the error shapes of [resource-api.md](../resource-api.md) §Common rules and §Watch. A create takes no `status`: the API server starts a Volume `Pending`. `spec` is immutable after creation: a `PUT` that changes any field of it, `nodeName` included, is `Invalid`. Only a binding sets `nodeName`, and a create that carries one is `Invalid`. A `PUT` may change a finalizer entry, under the rule for Machines (§Authorization). Resizing is not designed, and a larger disk is a new Volume.
2. **Class `local`.** A Volume with `spec.storageClass` `local` is a disk of `spec.storage` bytes on one Node. Its Node's agent creates the disk through mvmd and formats it ext4. For this class `accessModes` is exactly `ReadWriteOnce`, `storage` is positive and `dataSource` is empty, and `Validate` refuses a Volume that breaks any of the three as `Invalid`. A Volume with an empty `storageClass` is `Invalid`. A Volume of another class is not checked beyond that and waits `Pending` for a plugin that serves it; no plugin is designed here, and none is a Node Agent's work.
3. **Pinning.** The binding of a Machine, `POST /machines/{name}/binding`, reads each Volume the Machine names in `spec.volumes` and writes the Machine and the Volumes it changes in one write, so no reader sees one without the other.
   - A `local` Volume with no `nodeName` gets `spec.nodeName` set to the Machine's Node and the finalizer `compute.gaon.garam.sh/node-agent` added, in that write.
   - A Volume that already has a `nodeName` other than the Machine's Node refuses the binding as `Conflict`, because the disk is on the other Node and cannot be reached from this one. A Volume with `deletionTimestamp` set refuses it as `Conflict`, because its disk is being deleted. A name with no Volume refuses it as `Invalid`, because the Machine could not use it and a binding would only move the failure to the Node; the user creates the Volume, and the scheduler binds again. On a refusal, nothing is written.
   - A Machine rebound to the Node its Volumes are pinned to passes. A Machine whose `local` Volume is pinned to an expired Node is not rebound elsewhere: the binding is `Conflict`, the Machine keeps its binding, and the data stays on that Node's disk until the Node returns. The scheduler reads `spec.nodeName` and does not choose another Node for such a Machine.
4. **Deletion.** `DELETE` sets `deletionTimestamp` and removes the Volume once its last finalizer is removed, as for a Machine.
   - The finalizer is `compute.gaon.garam.sh/node-agent`, the name of the Node Agent's Machine finalizer, because the owner is the same. The API server adds it in the pinning write, so a Volume carries it exactly while it has a disk or may have one.
   - The agent of the Volume's Node removes it through `PUT /volumes/{name}`, by compare-and-swap and changing only its own entry, once no Machine has the Volume attached and it has deleted the disk through mvmd. A Volume that a Machine still holds stays terminating, and nothing new attaches to it (`Attach` is `Conflict`).
   - A Volume with no `nodeName` has no disk and no finalizer, so `DELETE` removes it at once. A Machine that names it is then refused at its binding, by item 3.
5. **Writers.**
   - Users write `spec`, at creation only.
   - The Node Agent of the Volume's Node writes `status` through `PUT /volumes/{name}/status`: `Available` with `capacity`, the size of the disk, once the disk exists; `Bound`, with the attachment, while a Machine has it attached; `Released` once no Machine holds it, keeping the attachment of the last holder so that an attach at an older epoch stays `Conflict`; `Failed` when it cannot make the disk. The attachment is the compare-and-swap of invariant 2.
   - The API server writes `status.phase` `Pending` at creation, and writes `nodeName` and the finalizer in a binding.
6. **Authorization.**
   - **The Node authorizer** allows a Node to read Volumes, because a list cannot be filtered by Node; to write `status` of a Volume whose `spec.nodeName` is that Node, or that a Machine bound to it uses; and to write its own finalizer through `PUT /volumes/{name}` on a Volume whose `spec.nodeName` is that Node, the API server checking that only the finalizer entry changed. It replaces the rule that allowed only `status.attachment`.
   - **The bootstrap roles.** `gaon:scheduler` gains `volumes`: `get`, `list`, `watch`, because it reads `spec.nodeName` to choose a Node. `gaon:controllers` already holds `volumes/status`: `update` and is unchanged. A user creates and deletes Volumes through `gaon:admin`'s `*`; no role for users is added.
   - **An existing cluster** keeps the `gaon:scheduler` it has: the API server creates a missing role and never overwrites one (ADR 0008). The scheduler of such a cluster lists Volumes `Forbidden` until an administrator adds the rule to the role, or deletes the role so the next API server start creates it.
7. **Client.** `lib/api` has `VolumeList`, `Volume.Validate`, `ValidateCreate` and `ValidateUpdate`, `Volume.Pin` and `Machine.PinVolumes`, which hold the rules of items 1 to 3, and the methods `PutVolume`, `GetVolume`, `ListVolumes`, `WatchVolumes` and `DeleteVolume` on `Client`.

## Consequences

- **A Volume can be created, listed, watched and deleted through the API,** and the Node Agent and the scheduler have what they need to act on one. Nothing is enforced until the API server serves it: the contract and the library validate, and the API server's checks, the one write of a binding and the Volume's `Pending` start belong to the issue that serves them. Until it lands, the documents describe what the server will do.
- **A Machine is tied to its first Node by its data.** A node lost for good strands its local Volumes and the Machines that use them; the way out is to delete both and make new ones. This is the first scope's choice, as [resource-api.md](../resource-api.md) §Volume already says, and a user who wants a Machine to move needs a class that is not node-local.
- **The finalizer can hold a Volume for ever.** A Node that never returns keeps its Volumes terminating, and an administrator removes the finalizer by hand. The same is true of a Machine's `node-agent` finalizer.
- **The binding writes more than one object.** The API server's binding is a transaction over a Machine and its Volumes, which the Store of ADR 0004 supports and which the serving issue implements.
- **Rejected: a PersistentVolumeClaim.** It is a second object per Volume with its own binding and its own lifecycle, for a cluster with no namespaces and one consumer per disk. The Machine names the Volume.
- **Rejected: a finalizer from the Volume's creation.** A Volume with no disk would wait for an agent to remove a finalizer, and a Volume of a class no agent serves could never be deleted. Adding it in the pin means a finalizer exists only where a disk can.
- **Rejected: `nodeName` set by the user at creation.** It would pin a Volume with no finalizer and bypass the pin; the binding is the one writer.
- **Rejected: moving a Volume between Nodes.** It needs a copy of the disk, and no document designs one.

## Signs the decision was wrong

- Users delete a Machine and its Volume to move a workload off a Node that is still healthy, and ask for a way to move the disk.
- Volumes stay terminating because agents are gone, and operators edit finalizers by hand more often than they delete Volumes through the API.
- Users create a `local` Volume and a Machine that cannot be scheduled together, because the Volume is pinned to a Node that lacks the Machine's capabilities, and ask to choose the Node at creation.
- A second node-served class is added and wants `ReadOnlyMany` or a data source, and the `local` rules turn out to be the rules of a class and not of the kind.
