# ADR 0018: Metadata gains uid, labels and owner references, lists take a label selector, Lease and Event are new kinds, and a role rule may carry a label selector

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#118, step D3 of goal garamsh/gaon#117. D4 of that goal, no namespaces, holds: every kind here is cluster-scoped.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **Controllers and operators need what Kubernetes' metadata gives them.** A controller finds the objects it manages by label, tells a recreated object from the one it replaced by `uid`, and ties a dependent to its owner by an owner reference. Leader election needs a lock any controller can hold, and an operator needs a record of what happened to an object. gaon has none of these ([resource-api.md](../resource-api.md) §Common rules lists `ownerReferences` and nothing behind it).
- **The need is stated by two consumers.** garamsh/codehub#136 and garamsh/gaon#116 each need a role that grants access to the objects carrying a label and to no others.
- **The Kubernetes pages read for this decision** are Labels and Selectors, Owners and Dependents, and Leases, of kubernetes.io. The semantics of `uid` and of the Event kind are taken from Kubernetes' API as the author knows it; their pages were not opened for this ADR.
- **`Lease` and `Event` already name something in `lib/api`.** `Lease` is the Node's lease in `compute.gaon.garam.sh` ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md)), and `Event` is the Go type of one change in a watch.
- **Existing objects exist.** A Store holds objects written without the new fields, and the change may not alter what they mean.

## Decision

**`ObjectMeta` gains `uid`, `labels` and `annotations`, and `OwnerReference` gains `uid`, `controller` and `blockOwnerDeletion`. A list and a watch take the query parameter `labelSelector`. `Lease` of `coordination.gaon.garam.sh/v1alpha1` and `Event` of `compute.gaon.garam.sh/v1alpha1` are new kinds. A rule of a `ClusterRole` may carry a `labelSelector`. Each follows Kubernetes' semantics except where an item below says otherwise.**

1. **`metadata.uid`** is a version 4 UUID, in lowercase hexadecimal with hyphens.
   - The server sets it at create and replaces any value the request carries.
   - It never changes: an update that carries a different uid is `Invalid`, and one that carries none keeps the stored one. An object deleted and created again has another uid.
   - An object stored before this ADR has no uid, and takes one on its next write. Nothing migrates the Store.
2. **`metadata.labels`** follow Kubernetes' syntax.
   - A key is an optional prefix and a name, joined by `/`. The prefix is a DNS subdomain of at most 253 characters, in lowercase. The name is 1 to 63 characters, alphanumerics, `-`, `_` and `.`, beginning and ending with an alphanumeric.
   - A value is empty, or at most 63 characters of the same form as a name.
   - A key or a value outside these is `Invalid`.
3. **`metadata.annotations`** have the same keys as labels and any string as a value. Keys and values together are at most 256 KiB, and more is `Invalid`.
4. **Label selectors** have the equality and the set-based forms of Kubernetes, in the query parameter `labelSelector` of a list and of a watch.
   - The forms are `key=value`, `key==value`, `key!=value`, `key in (v1,v2)`, `key notin (v1,v2)`, `key` (the label exists) and `!key` (it does not). Requirements are joined by commas, and every one must hold. A comma inside parentheses separates values.
   - `key!=value` and `key notin (...)` hold for an object that has no such label, as in Kubernetes. `key=value` and `key in (...)` do not.
   - An empty or absent selector selects every object. Text that does not parse, a key or a value that is not valid in a label, and an empty list in parentheses are `Invalid`.
   - The operators `gt` and `lt` of Kubernetes' set-based syntax are not supported, because no use needs them. They are `Invalid`.
   - A watch with a selector sends `Added` when an object starts to match, including one that is created matching, and `Deleted` when it stops matching or is removed, as Kubernetes does. Either carries the object as it is after the change. A change to an object that matches before and after is `Modified`, and one that matches neither is not sent.
   - A list answers only the objects the selector selects, and its `resourceVersion` is the Store's, as without a selector.
5. **`metadata.ownerReferences`** have `apiVersion`, `kind`, `name`, `uid`, `controller` and `blockOwnerDeletion`.
   - `apiVersion`, `kind` and `name` are required. `uid` is the owner's and is required on a reference at create, and on a reference an update adds. A reference the object already held without one, from before this ADR, may stay.
   - At most one reference is `controller: true`: the owner that manages the dependent.
   - `blockOwnerDeletion: true` means the dependent holds up the deletion of its owner when the owner is deleted in the foreground, as in Kubernetes. Garbage collection that acts on these fields is the controllers' issue, garamsh/gaon#120. This ADR fixes the fields and what they mean.
   - Every kind is cluster-scoped, so an owner is never in another scope than its dependent.
6. **`Lease` of `coordination.gaon.garam.sh/v1alpha1`** is a lock any controller can hold, for leader election. Its root is `/apis/coordination.gaon.garam.sh/v1alpha1` and its paths are `PUT /leases/{name}`, `DELETE /leases/{name}`, `GET /leases`, `GET /leases/{name}` and `GET /leases?watch=true`.
   - `spec` has `holder` (empty is a released lease), `renewTime` and `durationSeconds`. `durationSeconds` is positive, and a lease with a holder has a `renewTime`.
   - A candidate takes the lease, or renews it, by a compare-and-swap on the `resourceVersion` it read, as for every write ([resource-api.md](../resource-api.md) §Common rules). Two candidates that write at one version cannot both succeed.
   - Expiry is measured by the observer, as for the Node's Lease: the lease is expired when `durationSeconds` plus a margin has elapsed on the observer's monotonic clock since it first saw the current `resourceVersion`. `renewTime` is not compared with another machine's clock.
   - It is a different kind from the Node's `Lease` of `compute.gaon.garam.sh`. The group is reserved, so a `CustomResourceDefinition` may not take it.
   - Kubernetes' `acquireTime` and `leaseTransitions` are not adopted: nothing in gaon reads them.
7. **`Event` of `compute.gaon.garam.sh/v1alpha1`** records that something happened to an object. Its paths are `PUT /events/{name}`, `DELETE /events/{name}`, `GET /events`, `GET /events/{name}` and `GET /events?watch=true`. It has `metadata` and `spec`, and no `status`.
   - `spec.involvedObject` is the object it is about: `apiVersion`, `kind` and `name` are required, and `uid` is empty when the reporter did not know it.
   - `spec.reason` is a short machine-readable word and is required. `spec.message` is at most 1024 bytes. `spec.type` is `Normal` or `Warning`.
   - `spec.count` is at least 1 and is how many times the same event happened between `spec.firstTimestamp` and `spec.lastTimestamp`. A reporter that sees it again raises `count` and moves `lastTimestamp`, instead of writing another Event. `lastTimestamp` is not before `firstTimestamp`.
   - On an update, `involvedObject`, `reason` and `type` do not change, and `count` does not fall.
   - Kubernetes' `Event` holds these fields at its top level, and gaon's holds them in `spec`, because a gaon resource has `metadata` and `spec`. A reader finds the Events of an object by listing and comparing `spec.involvedObject`.
8. **A rule of a `ClusterRole` may carry `labelSelector`,** a string in the syntax of item 4, empty by default. A rule that does not carry one is unrestricted, as before. A selector that does not parse makes the role `Invalid`. The rule that follows is **the label-selector rule**: a request on an object whose labels fall outside the selector of a rule is not allowed by that rule.
   - **get, update, delete.** The rule allows the request only if the labels of the stored object satisfy its selector. An update is also judged on the labels in the request body, so a write cannot move an object out of the rule's selection, or into it. An object that does not exist is judged as one with no labels.
   - **create.** The labels of the object in the request body are judged. A mutating webhook that changes the labels afterwards is not judged again: a webhook is registered by an administrator.
   - **list and watch.** The rule allows the request only if the request carries a selector, and every object that selector selects is one the rule's selector selects. A list or a watch with no selector, or with one that selects more, is `Forbidden`: the server does not filter the answer to the rule, because an answer that looks complete and is not would mislead the caller. The test is `Selector.Within`, which compares one requirement of the request with one of the rule at a time, so a request that is within the rule only through two of its requirements together is refused.
   - **a subresource,** `microvms/status` and `microvms/binding` included, is judged on the stored object's labels, since the object exists.
   - **Rules are judged one at a time.** Permissions only add and there is no deny ([resource-api.md](../resource-api.md) §Authorization), so a request is allowed when some rule allows it. The selectors of two rules are not combined.
   - **Escalation.** A caller holds a rule with a selector only if it holds a rule for the same groups, resources and verbs that has no selector, or one whose selector the new rule's is within. A rule with no selector is held only through a rule with none.
   - The Node authorizer and the bearer-token access do not use selectors.
9. **Custom objects carry the same metadata** as the core kinds: `CustomObject` uses `ObjectMeta`.
10. **Library.** In `lib/api/`: `UID`, `NewUID`, `ValidateLabels`, `ValidateAnnotations`, `ObjectMeta.Validate`, `ValidateCreate` and `ValidateUpdate`, `Selector`, `Requirement`, `ParseSelector`, `Selector.Matches`, `Selector.Within`, `PolicyRule.AllowsLabels` and `AllowsSelector`, `CoordinationLease`, `ClusterEvent` and their lists, and the client option `WithLabelSelector`, which makes the lists and watches of one `Client` send the selector. `Client` has `Put`, `Get`, `List`, `Watch` and `Delete` for each new kind: `CoordinationLease` is called on a client whose root is the coordination group's. The Go names `ClusterEvent` and `CoordinationLease` keep the types apart from the watch's `Event` and the Node's `Lease`.
11. **Additive.** Every new field is omitted from JSON when empty, so an object written without them reads and writes as it did. The new validation is not part of the validation of the existing kinds: the API server calls it when it serves these fields (garamsh/gaon#119).

## Consequences

- A controller can list and watch by label, tell one incarnation of an object from the next by `uid`, and take a lock. An operator can record and read what happened to an object.
- An administrator can grant access to the objects with a label and to no others, which is what garamsh/codehub#136 and garamsh/gaon#116 need.
- The API server must set `uid` at create, apply item 4 to a list and a watch, judge the label-selector rule in `RBAC` mode, and serve the two new kinds. All of it is garamsh/gaon#119. Until then a request carries these fields and the server stores them as it stores the rest.
- A caller that holds only a selector-bound rule cannot list without a selector. A client written for it passes the selector with `WithLabelSelector`.
- Events accumulate: nothing here expires them. A time to live, or a limit on their number, is a later decision.
- `ObjectMeta` holds maps, so Go code can no longer compare two of them with `==`. No code in this repository does.
- The Go name `ClusterEvent` differs from the kind `Event`, and `CoordinationLease` from `Lease`, for the reason in item 10.

## Signs the decision was wrong

- A controller needs `gt` or `lt` in a selector, or `acquireTime` or `leaseTransitions` on a lease.
- A consumer's role cannot be expressed because two selectors must be combined for one grant, and the rule of judging one rule at a time is worked around with duplicated rules.
- A caller reads the refusal of a list without a selector as a bug and not as the rule, and asks for the server to filter.
- A mutating webhook that changes labels opens a grant the label-selector rule was meant to close.
- Events fill the Store before anyone decides how they expire.

## Rejected alternatives

- **Filtering a list and a watch to the rule's selector.** Kubernetes' RBAC does not filter, and a caller that expects every object gets some, with no sign of the rest.
- **Judging a list on the stored objects one by one.** It costs a read of the whole collection for each list, and gives the caller an answer that depends on the data and not on the request.
- **Combining the selectors of every rule that matches a request.** It cannot be decided by looking at one rule and is not what Kubernetes does.
- **Namespaces and `Role`.** Goal #117 D4.
- **Putting the Lease in the compute group.** The name is taken by the Node's lease, and a controller's lock is not part of a node's.
