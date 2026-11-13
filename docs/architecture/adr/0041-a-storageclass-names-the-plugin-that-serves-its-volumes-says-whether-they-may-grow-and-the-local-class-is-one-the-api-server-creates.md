# ADR 0041: A StorageClass names the plugin that serves its Volumes, says whether they may grow, and the local class is one the API server creates

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#230, part of goal #117, whose decision D7 is the PM's. It amends [ADR 0013](./0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md), where `local` was a name `lib/api` knew and no object, and it adds to [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)'s Node authorizer. It agrees with [ADR 0019](./0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md) and supersedes none of it. The growth a class allows is [ADR 0040](./0040-a-volumes-storage-may-grow-when-its-class-allows-it-the-plugin-on-its-node-grows-the-disk-and-a-growth-still-open-shows-as-a-resizing-condition.md)'s. This ADR is the contract; serving the kind, creating the `local` class and the plugin's reads are later issues of #117.

## Context

- **A class was a string.** `spec.storageClass` of a Volume names a class, `lib/api` validates the one value `local`, and any other value is accepted and waits `Pending` for a plugin that serves it (ADR 0013). There is no object to say that a class exists, which plugin serves it, or whether its Volumes may grow, and ADR 0040 needs the last of these.
- **The plugin boundary is by name.** ADR 0019 Decision 3: a storage plugin is a process whose socket is `<dir>/storage-<name>.sock`, `name` matching `[a-z0-9]([a-z0-9-]*[a-z0-9])?`, and an agent names the one it uses by `--storage-plugin <name>`, so one agent has one storage plugin. A plugin acts as the Node whose agent it serves, through the Node authorizer (ADR 0013, ADR 0019), and the agent's `StorageService.Attach(microvm)` is how the plugin learns that a MicroVM on its Node uses a Volume.
- **Kubernetes' StorageClass** ("Storage Classes", <https://kubernetes.io/docs/concepts/storage/storage-classes/>, footer "Last modified October 29, 2026", the introduction and "Volume expansion" read) is an object with a `provisioner`, `parameters`, `reclaimPolicy`, `allowVolumeExpansion`, `mountOptions`, `volumeBindingMode`, and `allowedTopologies`; it has no `spec`, and a class's `provisioner` and parameters are immutable after creation, while `allowVolumeExpansion` may change.
- **mvmd's statements** in this ADR hold for garamsh/mvmd at commit `20150e3ba4dcc7bd860c3d80cb94ee44fd2227ed`: a disk is a file mvmd's Driver makes and grows, and nothing in mvmd knows of a class. `local` is the class gaon's `storage-local` serves with those disks.

## Decision

**A StorageClass is a cluster-scoped object with a `provisioner`, the name of the storage plugin that serves its Volumes, and `allowVolumeExpansion`; a Volume names it by `spec.storageClass`; the class `local` is created by the API server at start; and a plugin learns of a Volume of its class when the Volume is attached on its Node.**

1. **The kind.** `StorageClass` of `compute.gaon.garam.sh/v1alpha1`, cluster-scoped, with the fields of Kubernetes' that gaon uses and no `spec` and no `status`, as a ClusterRole has none:
   - `provisioner`, required: the name of the storage plugin that serves the class, with the form of a plugin name of ADR 0019 Decision 3. It is **immutable**: an update that changes it is `Invalid`. Deleting a class and creating another of the name is the way to change it.
   - `allowVolumeExpansion`, a boolean, false when omitted. It may change, and ADR 0040 says what it allows. Turning it off leaves a growth already written standing, as in Kubernetes; the plugin finishes it.
2. **How a Volume names it.** `spec.storageClass` is the class's name, as now, and is required. A Volume whose class does not exist is accepted at creation, as now, and waits `Pending`; the class may be created later. The class is not read to validate a creation, because that would make creation order a rule between objects that are written independently. A growth reads it (ADR 0040 Decision 3).
3. **The `local` class.** The API server creates, at its start and when it is missing, the StorageClass `local` with `provisioner` `local` and `allowVolumeExpansion` true. `local` has the one provisioner `local` and no other class may have it: a class named `local` with another provisioner, or another class with the provisioner `local`, is `Invalid`, because `Volume.Validate` holds the rules of a local Volume by the name. An administrator who deletes `local` has the API server create it again at its next start, and one who sets `allowVolumeExpansion` to false on it has that kept, since the server creates only a missing class.
4. **How a plugin is told that a Volume of its class exists.** Nothing is added to the plugin API of ADR 0019, and no registry of classes is kept anywhere but the API server's.
   - A plugin reads StorageClasses and Volumes through the resource API as its Node, so the Node authorizer allows a Node to `get`, `list` and `watch` StorageClasses, as it allows it for Nodes' other reads.
   - A Volume of a class whose `provisioner` is the name of the plugin an agent runs is the plugin's when a MicroVM bound to that agent's Node names it: the agent's `Attach` call carries that MicroVM, and the plugin readies the Volume the way `local` readies a disk, and writes the Volume's `status`. A Volume that no MicroVM on the Node uses is not the plugin's, and is `Pending`. This is Kubernetes' `WaitForFirstConsumer` binding mode, and the only one: the Volume is provisioned where it is first used, and gaon has no `Immediate`.
   - `local` additionally pins the Volume to the Node (ADR 0013). A class of another provisioner is not pinned by the API server: pinning is a decision of a node-local plugin, and a plugin of a shared backend needs none.
   - A Volume of a class with no plugin on the Node, or no class at all, stays `Pending` and its MicroVM's `StoragePluginUnavailable` condition says why (ADR 0019 Decision 5).
5. **Rights.** Users write StorageClasses under the rule that lets them write other kinds: `gaon:admin`'s `*`. A Node reads them and writes none. `gaon:scheduler` gets no rule, since it places by Volumes and not by their class.
6. **Client.** `lib/api` has `StorageClass` and `StorageClassList` with `Validate` and `ValidateUpdate`, and the constants `KindStorageClass` and `KindStorageClassList`. `StorageClassLocal` is the name and the provisioner of the built-in class.

## Consequences

- **A cluster has a list of what its storage can do.** `gaon get storageclasses` says which classes exist and whether they grow.
- **`local` is no longer special in `lib/api` alone.** It is an object, so the rule that a growth needs `allowVolumeExpansion` has one reading for it and for a third party's class.
- **An existing cluster gets the `local` class at the API server's next start**, with no step for the administrator. Until then a growth is refused for lack of a class, which is `Invalid`.
- **A third-party storage system is supplied by a binary and an object**: a plugin named `ceph`, run by the agent with `--storage-plugin ceph`, and a StorageClass with `provisioner: ceph`. Nothing is rebuilt.
- **A Volume of a third party's class is provisioned only when first used.** A user who wants a Volume ready before its MicroVM exists cannot have it. Whether a plugin should be able to claim a Volume without an attach is an open question in [resource-api.md](../resource-api.md).
- **A growth of such a Volume with no MicroVM attached waits**, since no Node's plugin has been given it. This too is an open question.
- **Rejected: `parameters`, `reclaimPolicy`, `mountOptions`, `volumeBindingMode` and `allowedTopologies`.** None has a reader yet. `local` takes no parameters, and a plugin has no call that would pass them. A class that needs them adds them with the plugin that does.
- **Rejected: a `StorageClass.spec`.** Kubernetes' has none, and the fields are the whole of the class, so a `spec` would add a level no one needs.
- **Rejected: leaving `local` a name with no object, and a growth refused on it by a rule in `lib/api`.** It would make the one class that exists a special case in every reader of the rule, and a user could not read from the cluster that `local` grows.
- **Rejected: validating `spec.storageClass` against the classes at creation.** It makes the creation of a Volume depend on another object being written first, and Kubernetes accepts such a claim and leaves it pending.
- **Rejected: a call in `StorageService` that tells a plugin of a Volume of its class.** The plugin reads Volumes as its Node (ADR 0013), and the agent's `Attach` already names the MicroVM that uses one. A call would give the agent a second way to say what the plugin can read.
- **Rejected: the API server choosing a plugin by class and writing it into the Volume.** The Node's agent runs one plugin, so the Node's plugin is the plugin of the Volume that Node uses.

## Signs the decision was wrong

- A plugin needs a parameter of the class, such as a pool name, and has none to read.
- Users want a Volume of a third party's class ready before it is used, and a Volume of the class cannot be.
- An agent needs to run more than one storage plugin, and the one-plugin-per-agent rule of ADR 0019 blocks a class.
- Administrators delete `local` or turn off its expansion and are surprised that the server keeps it.
