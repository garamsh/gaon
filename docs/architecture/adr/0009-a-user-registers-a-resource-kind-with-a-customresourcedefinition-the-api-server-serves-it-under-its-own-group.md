# ADR 0009: A user registers a resource kind with a CustomResourceDefinition, and the API server serves it under its own group

> Status: accepted; the schema keywords of Decision 5 are amended by ADR 0031 (an integer-or-string node)
> Date: 2026-11-12

Drafted for garamsh/gaon#80, under goal garamsh/gaon#74. The design is the PM's, taken under the project owner's decisions in #74 (Kubernetes semantics, a gaon-native implementation, additive only, no namespaces) and recorded in #80 §Design. It uses the group [ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md), Decision 8, reserved for resource definitions, and the authorization of [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md). This ADR is the contract; serving it in the API server is a later issue of #74.

## Context

- **The default extensions are not written** (MachineSet, MachineDeployment, Job, Service, Gateway, Autoscaler), and [resource-api.md](../resource-api.md) §Naming and groups says they are built on resource definitions. Nothing defines how a kind is registered, so none can be added without a change to the API server.
- **The compute kinds have a Go type each** in `lib/api`. A kind registered at run time has none, so its client cannot be a method per kind.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12:**
  - "Extend the Kubernetes API with CustomResourceDefinitions", <https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/>. The footer reads "Last modified October 20, 2026"; its links name v1.37, and its version selector lists v1.33 to v1.37. The page is 169842 characters long; it was read through fetches that each returned a summary of the text, the first 100000 characters and then the sections on structural schema, pruning, subresources and finalizers, and the text between those was not read in full.
  - "Custom Resources", <https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/>. Characters 0 to 100000 of its 118942 were read; the rest was not. No footer date and no stated version were seen, and its version selector lists v1.33 to v1.37.
  - What was taken from them:
    - the definition's name is `<plural>.<group>`; `spec.group` is the REST path `/apis/<group>/<version>`; each version has `served` and `storage`, and exactly one is the storage version; `spec.names` has `plural`, `singular` and `kind`; a definition is not namespaced;
    - the endpoint is served once the `Established` condition is true;
    - deleting a definition removes its endpoint and deletes every object stored under it, and a definition created again starts empty;
    - a structural schema is mandatory, with a non-empty `type` for the root, each object field and each array item, except under `x-kubernetes-preserve-unknown-fields: true`;
    - a field the schema does not name is pruned before the object is stored, unless a subtree opts out with `x-kubernetes-preserve-unknown-fields: true`;
    - with the status subresource, a `PUT` to `/status` ignores everything but `status`, and a `PUT` to the object ignores `status`;
    - a first `DELETE` of an object with finalizers sets `deletionTimestamp`, and the object is removed when the last finalizer is.
  - What the pages do not state, and so is not taken as Kubernetes' behaviour:
    - the condition `NamesAccepted` was not on the text read, nor was a rule for which of two colliding definitions keeps its names;
    - the HTTP status of a write that fails a schema (the page gives only the error text);
    - what a watcher of the objects sees while a definition is deleted;
    - whether a create of an object is refused while its definition is being deleted.
  - Where gaon departs from Kubernetes on purpose: it has no namespaces, so `scope` has one value; it takes one version, because conversion is not designed; it takes a smaller set of schema keywords than Kubernetes' structural schema; and it limits the root of a schema to `spec` and `status`.

## Decision

**A `CustomResourceDefinition` in `apiextensions.gaon.garam.sh/v1alpha1` registers a cluster-scoped kind with one version, a structural-subset schema and an optional status subresource, and the API server serves the kind's objects under `/apis/<group>/<version>` with the forms of the compute kinds.**

1. **Resource.** `CustomResourceDefinition`, root `/apis/apiextensions.gaon.garam.sh/v1alpha1`, cluster-scoped, with `PUT`, `GET` of one and of a list, `GET ?watch=true` and `DELETE`. Its name is `<plural>.<group>` and at most 253 characters; any other name is `Invalid`.
2. **Spec.**
   - `group` is a domain of at least two lowercase DNS labels.
   - `names` has `kind`, `listKind`, `plural` and `singular`. `kind` and `listKind` start with an uppercase letter and hold letters and digits, and differ from each other. `plural` and `singular` are lowercase DNS labels.
   - `scope` is `Cluster`; `Namespaced` and any other value are `Invalid` until namespaces exist.
   - `versions` has exactly one entry, which is served and stored, with a `name` of the form `v1`, `v1alpha1` or `v2beta3`, and a `schema.openAPIV3Schema`. More versions need conversion and wait for a later ADR. `subresources.status`, written `{}`, is optional.
   - `group`, `names` and the version `name` cannot change after creation, because stored objects and every client's path depend on them. The schema and the subresources can.
3. **Reserved groups.** `group` may not be `compute.gaon.garam.sh`, `rbac.gaon.garam.sh` or `apiextensions.gaon.garam.sh`, or any later group the core serves; the contract lists each when it adds one. `apps.gaon.garam.sh` is allowed, because ADR 0003 places the default extensions there.
4. **Paths of a custom resource.** `/apis/<group>/<version>/<plural>`, `/{name}` and, when the status subresource is on, `/{name}/status`, with `PUT`, `DELETE`, `GET`, list and watch. The compare-and-swap, the watch and the error shapes are those of [resource-api.md](../resource-api.md) §Common rules and §Watch, and finalizers and `deletionTimestamp` behave as there. An object has `apiVersion`, `kind`, `metadata`, `spec` and `status`; `spec` and `status` are JSON objects. With the status subresource on, a `PUT` to the object keeps the stored `status` and a `PUT` to `/status` changes only `status`; with it off, a `PUT` to the object writes both and `/status` is `NotFound`.
5. **Schema.** A structural subset of OpenAPI v3.
   - The keywords are `type`, `description`, `properties`, `required`, `items`, `enum`, `minimum`, `maximum`, `pattern` and `x-kubernetes-preserve-unknown-fields`. The types are `object`, `array`, `string`, `integer`, `number` and `boolean`. A definition whose schema carries any other keyword, at any depth, is `Invalid`; `lib/api` records unknown keywords when it decodes a node, so `Validate` can refuse them.
   - Each node has a `type`, except a node with `x-kubernetes-preserve-unknown-fields: true`.
   - The root is an object whose properties are only `spec` and `status`, and it does not preserve unknown fields. `apiVersion`, `kind` and `metadata` are the same for every kind and are not in the schema.
   - `properties` and `required` belong to an object, and each name in `required` is in `properties` unless the node preserves unknown fields, because a write would prune a required field the schema does not name. `items` belongs to an array, and an array has it. `enum` belongs to a scalar type and each value has that type. `minimum` and `maximum` belong to `integer` and `number`, and `minimum` is not above `maximum`. `pattern` belongs to a `string` and compiles as a Go regular expression.
   - A write that fails its kind's schema is `Invalid`. An unknown field is pruned unless an ancestor node preserves unknown fields, and pruning applies to `spec` and `status`.
6. **Status of the definition.** `status.conditions`, written by the API server, has the types `NamesAccepted` and `Established`; any other type is refused.
   - A definition is `NamesAccepted` unless another definition of the same group has the same `kind`, `listKind`, `plural` or `singular`. The older definition, by creation, keeps its names. A later one is `NamesAccepted` false with a reason, is not `Established`, and its paths are not served.
   - `Established` is true once the API server serves the paths.
7. **Deletion.** `DELETE` of a definition, by compare-and-swap, takes these steps in order.
   1. The API server sets the definition's `deletionTimestamp` and sets `Established` false. From then it refuses a create of an object of the kind as `Conflict`.
   2. It deletes each object of the kind as a user's `DELETE` does: an object with finalizers gets a `deletionTimestamp` and waits for its owners, and one without is removed.
   3. When no object of the kind is left, it removes the definition.
   A watcher of the objects sees `Modified` for each object whose `deletionTimestamp` is set, `Deleted` for each one removed, and then its stream ends, so its next list answers `NotFound`. A watcher of definitions sees `Modified` at step 1 and `Deleted` at step 3. A definition created again starts empty.
8. **Authorization.** Custom resources are authorized by ADR 0008's rules: the API group is the definition's `group`, and the resource is `<plural>` or `<plural>/status`. The definitions are the resource `customresourcedefinitions` in `apiextensions.gaon.garam.sh`. `gaon:admin`'s `*` covers both. The bootstrap roles of ADR 0008 name no custom resource and are unchanged.
9. **Client.**
   - `lib/api` has the definition's types with `Validate`, and `ValidateUpdate` for the immutable fields and `ValidateStatus` for the condition types, and the methods `PutCustomResourceDefinition`, `GetCustomResourceDefinition`, `ListCustomResourceDefinitions`, `DeleteCustomResourceDefinition` and `WatchCustomResourceDefinitions` on `Client`, which is configured with the root of its group as the other groups' clients are.
   - A custom-resource client is a `CustomResourceClient` made from a `CustomResourceType` (group, version, kind and plural). It holds an object as `CustomObject`, whose `spec` and `status` are raw JSON, so no Go type is needed per kind. It has `Put`, `PutStatus`, `Delete`, `Get`, `List` and `Watch`, each with the semantics of the compute kinds' methods, and it refuses a type with an empty field. `CustomObject.Validate` checks the identity against the type and that `spec` and `status` are objects. The schema decides the rest, on the server.

## Consequences

- **A kind can be added without a change to the API server or `lib/api`.** A controller or a CLI reads and writes it through `CustomResourceClient` and decodes `spec` into its own type.
- **The client does not check an object against the schema.** The API server does, and a client that wants the check reads the definition. A typed wrapper per kind is a later choice for the code that owns the kind.
- **The schema is smaller than Kubernetes'.** No `additionalProperties`, `format`, `default`, `nullable`, `oneOf` and the like, so a map-valued field is written as an object that preserves unknown fields, which keeps its values unchecked. A user who needs one of them waits for a later ADR.
- **Rejected: a typed client by generics, one Go type per kind.** It needs code generated or written for each kind, which is what registration is meant to avoid.
- **Rejected: more than one version.** A second version needs a conversion between versions, which no document designs.
- **Rejected: letting `group`, `names` and the version name change.** Stored objects are under the old path and kind, and a change would orphan or reinterpret them. A new definition is the way.
- **Deleting a definition deletes objects, and a finalizer can hold it.** An object whose owner never removes its finalizer keeps the definition in its terminating state, with `Established` false and creates refused. An administrator removes the finalizer.
- **The end of the watcher's stream is gaon's choice, not Kubernetes'.** Its pages do not state what a watcher sees, and a watcher that ignores the end re-lists and gets `NotFound`.
- **The core and any later core group must be added to the reserved list** when it is added, or a user can claim it. The contract states this in item 3.
- **Name collisions are decided by age.** Two writers creating colliding definitions at once are ordered by the Store's version, which orders every write (ADR 0004).
- **Nothing is enforced until the API server serves it.** The contract and the library validate; the API server's serving, its pruning of writes and the order of deletion are the next issue, and until it lands the documents describe what the server will do.

## Signs the decision was wrong

- Users register one definition per version of a kind, because one version per definition is too narrow, and ask for conversion.
- The schema's keyword set is too small, and users put `x-kubernetes-preserve-unknown-fields` on most nodes, so objects are validated by nothing.
- A definition stays terminating because objects keep finalizers whose owners are gone, and operators edit the Store by hand.
- A client needs the schema check before it writes, and each one reimplements it.
- Namespaces arrive and `Cluster` as the one scope has to be reopened, with a migration of stored definitions.
