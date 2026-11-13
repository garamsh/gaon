# ADR 0040: A Volume's storage may grow when its class allows it, the provisioner grows the disk, and a growth still open shows as a Resizing condition

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#230, part of goal #117, whose decision D7 is the PM's. It changes [ADR 0013](./0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md) Decision 1, which made `spec` immutable and left resizing undesigned, and its list of what the first scope lacks, which named expansion. It keeps ADR 0013's writers and finalizer, and the plugin boundary of [ADR 0019](./0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md). The class a growth needs is [ADR 0041](./0041-a-storageclass-names-the-provisioner-that-serves-its-volumes-says-whether-they-may-grow-and-the-local-class-is-one-the-api-server-creates.md)'s. This ADR is the contract; serving it in the API server, the node agent and `storage-local` are later issues of #117.

## Context

- **A Volume's size cannot change.** ADR 0013 made every field of `spec` immutable after creation, and a larger disk is a new Volume with the data copied by the user.
- **mvmd can now grow a disk.** The statements in this list hold for garamsh/mvmd at commit `20150e3ba4dcc7bd860c3d80cb94ee44fd2227ed`, read as its ADR 0055 (`docs/architecture/adr/0055-a-node-local-disk-grows-offline-or-online-on-a-driver-that-reports-disk-resize-and-the-guest-grows-the-filesystem.md`) and `lib/runtimeapi/proto/mvmd/runtime/v1alpha2/runtime.proto`:
  - `ResizeDisk {disk_id, size_bytes}` grows a disk to a larger size and answers an empty response. An unknown disk is not found, a size below the disk's is invalid, and an equal size succeeds and changes nothing.
  - A disk no Machine holds, or one a Created or Stopped Machine holds, is grown offline: the file is grown, and the guest grows the ext4 filesystem at the Machine's next Start.
  - A disk a Running Machine holds is grown live only on a Driver that reports `CAPABILITY_DISK_RESIZE`: Cloud Hypervisor does, Firecracker does not. Cloud Hypervisor refuses a size that is zero or not a multiple of 512 before the request.
  - A Paused or Faulted holder, a Running holder on a Driver without the capability, and a Machine's root disk are precondition failed.
  - A snapshot taken before a growth is not restorable after it, because a restore refuses a disk whose size differs from the snapshot's record.
- **The semantics follow Kubernetes' documentation, read on 2026-11-13:**
  - "Persistent Volumes", <https://kubernetes.io/docs/concepts/storage/persistent-volumes/>. The footer reads "Last modified June 17, 2026". The section "Expanding Persistent Volumes Claims" was read, with its subsection "Recovering from Failure when Expanding Volumes"; the rest of the page was not.
  - "Storage Classes", <https://kubernetes.io/docs/concepts/storage/storage-classes/>. The footer reads "Last modified October 29, 2026". The section "Volume expansion" and the introduction to "StorageClass objects" were read; the rest of the page was not.
  - What was taken from them:
    - a volume grows only when its storage class has `allowVolumeExpansion` true;
    - the user asks by editing the claim to a larger size, and "A new PersistentVolume is never created to satisfy the claim. Instead, an existing volume is resized";
    - a file system is grown when a pod starts or, where the file system supports it, while the pod runs;
    - a size too big for the storage is "continuously retried until user or cluster administrator takes some action", and a request may be lowered to a value that is still above `.status.capacity`;
    - "Kubernetes does not support shrinking a PVC to less than its current size";
    - progress is watched in `.status.allocatedResourceStatuses` and events.
  - What the pages do not state, and so is not taken as Kubernetes' behaviour: the HTTP status of a refused growth or shrink, whether a claim with no bound volume may be resized, whether a volume being deleted may, and whether a condition reports a growth in progress.
  - Where gaon departs from Kubernetes on purpose:
    - there is no claim. The user edits the Volume's `spec.storage`, and `status.capacity` is the size of the disk, so one object carries what a claim and its volume carry in Kubernetes;
    - a pending growth is reported in one condition of the Volume's `status`, where Kubernetes reports it in `allocatedResourceStatuses` and events; gaon has no field of that name, and `status.conditions` is how the Node and the MicroVM report detail ([resource-api.md](../resource-api.md) §Common rules);
    - gaon grows only a disk, and shows nothing of the filesystem: there is no counterpart of the pending-filesystem state, because the guest grows it and mvmd does not report that to gaon;
    - gaon accepts a growth that its Node cannot do yet and waits, where Kubernetes' controller also retries; a growth is refused only for a reason the API server can see.

## Decision

**`spec.storage` of a Volume may be raised by a user while the Volume's class allows expansion; the provisioner of the class grows the disk and writes the new `status.capacity`; a growth that has not finished shows as a `Resizing` condition in `status`; a size below `status.capacity` is `Invalid`; and a growth the Node cannot make yet waits.**

1. **What may change.** In a `PUT /volumes/{name}` of an existing Volume, `spec.storage` may differ from the stored one, and no other field of `spec` may, `nodeName` included (ADR 0013 Decision 1 otherwise stands). The API server stamps a new `generation` for it as for any change of `spec`. The rules of the change are in item 3.
2. **Who may change it.** Users, under the rule that lets them `update` Volumes: `gaon:admin`'s `*`, with no role added. A Node does not: its write to a Volume stays the finalizer entry only (ADR 0013 Decision 6), so a Node that changes `spec.storage` is refused as it is for any other change of `spec`. The plugin writes `status`, never `spec`.
3. **The rules of a change of `spec.storage`.**
   - **A growth** is a size above the stored `spec.storage`. It needs a StorageClass of the Volume's `spec.storageClass` that has `allowVolumeExpansion` true; with the class absent or not allowing it, the write is `Invalid`.
   - **A size below `status.capacity` is `Invalid`**, which is what a shrink is: a Volume does not shrink. A size equal to it is accepted, and so is any size at or above it when the stored `spec.storage` is higher: a request for more than the disk holds, which a plugin has not been able to meet, is lowered down to `status.capacity` to cancel it. A Volume with no `status.capacity`, because its disk does not exist yet, has no floor.
   - **A Volume with `deletionTimestamp` set** refuses any change of `spec.storage` as `Conflict`, because its disk is being deleted, as it refuses a binding and an attach.
   - A size in another unit of the same amount is no change, and passes under any class.
   - Kubernetes' pages do not state the status of a refusal (Context), so this ADR chooses: `Invalid` for what the request asks and `Conflict` for the state of the Volume.
4. **What grows the disk.** The provisioner of the Volume's class compares `spec.storage` with `status.capacity` and, while the first is above the second, grows the disk and writes `status.capacity` through `PUT /volumes/{name}/status`.
   - **`local`:** `storage-local`, on the Volume's Node under the Node's identity, asks mvmd to `ResizeDisk` the Volume's disk to `spec.storage` bytes. It acts at each resync until the two are equal, so a repeat after a failure finishes the growth, which mvmd's equal-size rule allows. A Volume with no disk yet gets one of the current `spec.storage`.
   - **Any other class:** the cluster-side provisioner of [ADR 0041](./0041-a-storageclass-names-the-provisioner-that-serves-its-volumes-says-whether-they-may-grow-and-the-local-class-is-one-the-api-server-creates.md) Decision 4, under its own identity, grows the Volume however its storage system does and writes the same `status` fields.
   - No call is added to the plugin API of ADR 0019: a provisioner learns of a growth from the Volume it reads through the resource API.
5. **How a pending or refused growth shows.**
   - `status.capacity` is the size of the disk. A growth is open exactly while `spec.storage` is above it.
   - While it is open the provisioner keeps a condition in `status.conditions` of `type` `Resizing` and `status` `True`, with a `reason` and a `message`. The provisioner removes it, in the status write that raises `status.capacity` to `spec.storage`. For `local` the reasons are:
     - `InProgress`: the provisioner has taken the request and has not finished it;
     - `HolderNotResizable`: mvmd answered precondition failed (item 7);
     - `Failed`: mvmd answered with any other error, with its message in the condition's `message`; a size mvmd refuses, such as one that is not a multiple of 512 on Cloud Hypervisor, is one.
   - A reason of another provisioner is its own.
   - Every state is retried at each resync, as Kubernetes retries a growth. The way out of a `Failed` request is to lower it (item 3).
   - **`status.phase` does not change.** `Failed` is for a disk that cannot be made, and a disk whose growth failed is still the disk the Machines use.
6. **What a shrink returns.** `Invalid`, with the message naming `status.capacity`, from the API server on the write (item 3). No provisioner ever sees a request below its disk, since `spec.storage` cannot fall below `status.capacity`. The one disk size a plugin could read as smaller is one it made larger than a lowered `spec.storage`, and it does nothing for that: mvmd answers a smaller size as invalid and an equal one as no change, and the plugin calls only when `spec.storage` is above `status.capacity`.
7. **A Driver without live resize while the Volume is attached.** The API server cannot see a Node's Driver, so it does not refuse the growth; the Node does not yet do it.
   - mvmd's answer for a disk that a Running Machine holds, on a Driver without `CAPABILITY_DISK_RESIZE`, and for a Paused or Faulted holder, is precondition failed. storage-local shows `Resizing` with the reason `HolderNotResizable` and tries again at each resync.
   - The growth completes when the holder is Created or Stopped, or when no Machine holds the disk, because mvmd then grows the file offline. On a Driver that reports the capability it completes at once, with the Machine running.
   - Nothing stops a Machine to make a growth possible. A workload's run state is the user's.
   - After an offline growth `status.capacity` reports the grown device, and the filesystem follows at the Machine's next Start, as mvmd's ADR 0055 says. gaon does not show that interval.
8. **Client.** `lib/api` has `Volume.ValidateResize`, which holds the rules of item 3 given the Volume as stored and its StorageClass; `VolumeStatus.Conditions` and the condition type `VolumeConditionResizing`. `Volume.ValidateUpdate` is unchanged and still refuses every change of `spec`; the issue that serves growth moves the API server to `ValidateResize` and removes it.

## Consequences

- **A Volume grows in place**, offline on both of mvmd's Drivers and live on Cloud Hypervisor, and the disk's data stays where it is. A user who wants more space edits one number.
- **Nothing is enforced until the API server serves it.** The contract and the library validate; the API server still refuses every change of `spec` until the serving issue lands, and `storage-local` grows nothing until its own. Until then the documents describe what the server will do, as ADR 0013 said of itself.
- **A growth can wait for ever** on Firecracker under a Machine that never stops, and shows `HolderNotResizable` for as long as it does. The condition is the signal, and the user stops the workload or lowers the request.
- **A snapshot taken before a growth is not restorable after it** (mvmd ADR 0055 at the commit above). `VolumeSnapshot` is not in this ADR, and its interaction with growth is an open question in [resource-api.md](../resource-api.md).
- **A size that is not a multiple of 512 is accepted by the API** and fails in the plugin on a Driver that refuses it, which is the `Failed` reason. Whether `local` should refuse it at creation and growth is an open question.
- **The Volume's file system is ext4 and grows by the guest.** A class that makes another filesystem is not designed here.
- **Rejected: a resize request object, or a `resize` subresource.** The Volume is the one object a user writes, its `spec` is the desired state and `generation` tracks a change, and a second object or path would add a writer and a lifecycle for one number.
- **Rejected: a `Resize` call in `StorageService`.** ADR 0019 left disk creation and deletion to the plugin acting on the Volume it reads, and a call would make growth the one thing the agent pushes. It would need the agent to find the growth first, which means reading the Volume again.
- **Rejected: refusing a growth while a Machine holds the Volume.** It would make the API server decide on a Driver it cannot see, and would refuse on Cloud Hypervisor what mvmd can do.
- **Rejected: the agent stopping the Machine to grow offline.** It would stop a workload the user did not ask to stop.
- **Rejected: refusing any lowering of `spec.storage`.** It would leave a request the storage cannot meet standing for ever, with data on the disk that stops the user deleting the Volume; Kubernetes' recovery is to lower it above the capacity, which is what is taken.
- **Rejected: a condition type for each failure, and a `Failed` phase for a failed growth.** One type carries the reason, and the phase would say the disk is lost.

## Signs the decision was wrong

- Users lower a pending request to cancel it far more often than they grow, and ask for a cancel that does not depend on the capacity.
- Volumes stay `HolderNotResizable` on Firecracker for long, and users ask for the API server to refuse what the Node cannot do, or for the agent to stop the Machine.
- A provisioner needs to be told of a growth by something other than the Volume it reads.
- Plugins report states that the three reasons and the condition's message cannot carry.
- Users ask where the filesystem is, because a Machine they read `status.capacity` from has a smaller one.
