# Garbage collector

How the garbage collector deletes an object once every owner named in its `ownerReferences` is gone, and which objects it leaves alone. The collector is in `controllers/` and runs inside the binary `gaon-controllers`.

Basis: [ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md) item 5 for `metadata.uid` and `ownerReferences`, and [resource-api.md](./resource-api.md) §Metadata and §Paths. It depends on the `gaon:controllers` grant of `get`, `list`, `watch` and `delete` on every resource.

## Current decisions

### Responsibility

- **The collector deletes background only.** An object is deleted after its owners are gone. An owner is never held back until its dependents are deleted, and `blockOwnerDeletion` is not read.
- **An object is deleted when it has at least one owner reference and every one of them is gone.** One owner still present keeps the object. An object with no owner reference is never deleted.
- **It works for every kind**: the built-in kinds the API server serves, and each kind of a CustomResourceDefinition whose condition `Established` is true. An owner reference is matched to a kind by its `apiVersion` and `kind`, because the Lease of the compute group and the Lease of the coordination group share a kind name.
- **It writes only DELETE**, at the `resourceVersion` it listed. A conflict or a missing object is not an error: the next sweep decides again.
- It uses the public resource API through `lib/api` and imports no other unit.

### When an owner is gone

- **An owner is gone** when its kind was listed and either no object of that name is stored, or the stored object has a different `uid` from the reference. A recreated object has a new `uid`, so a reference to the old one names an owner that is gone.
- **An owner missing from the list is read once before it counts as gone**, so an owner created after the list is not taken for a missing one.
- **A reference without a `uid`** (one stored before ADR 0018) cannot tell one incarnation from another, so it matches the object of that name: the owner is present when an object of that name exists, and gone when none does. The same holds when the stored owner has no `uid`.
- **An owner of a kind the collector does not know is not gone.** So is an owner whose kind could not be listed in this sweep, or whose read failed. The dependent is kept.
- **An owner with a `deletionTimestamp` is present** until it is removed.

### Which objects are left alone

- **An object with a `deletionTimestamp`**, which is already being deleted.
- **An object of a kind with no DELETE path**: Node, the Lease and CertificateSigningRequest of the compute group. These can own objects and are never collected.
- **A kind whose definition is not Established**, because its objects are not served.

### Timing

- **The collector sweeps**: it lists every kind, decides, deletes, and starts again after `-gc-interval`, which defaults to 30s and must be above zero. An orphan is deleted within one interval plus the time of a sweep.
- **A failure of one kind or one object is logged** and does not stop the rest. When the definitions cannot be listed, the sweep sees only the built-in kinds, and an owner of a custom kind is not gone.
- **The collector runs only when `-api` ends in `/apis/compute.gaon.garam.sh/v1alpha1`**, because the roots of the other groups are derived from it. Otherwise the binary logs that it does not run.

## Rationale

- Background deletion needs no ordering between owner and dependent, so the collector keeps no state between sweeps and a restart loses nothing.
- Counting a `uid` mismatch as gone follows ADR 0018: the `uid` is what separates an object from a later one of the same name.
- Keeping a dependent whose owner kind is unknown or unreadable is the safe direction: a deleted object cannot be restored, and a kept one is collected on a later sweep.
- The read before deleting trades one request per candidate owner for never deleting a dependent whose owner exists.

## Open questions

- **A full sweep lists every kind each interval.** Its cost grows with the number of objects, where a watch would act on a change. The interval is a flag until it is measured.
- **The built-in kinds are a table in the collector**, because `lib/api` has no discovery of the kinds the server serves.
