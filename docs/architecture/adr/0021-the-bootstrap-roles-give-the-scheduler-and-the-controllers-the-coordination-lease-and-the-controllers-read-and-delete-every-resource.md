# ADR 0021: The bootstrap roles give the scheduler and the controllers the coordination Lease, and the controllers read and delete every resource

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#119, step D3 of goal garamsh/gaon#117. It supersedes [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md) as to the rules of `gaon:scheduler` and `gaon:controllers` in Decision 6, and in no other part: identity, the Node authorizer, the escalation rule and the refusal stay as ADR 0008 states them.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **Leader election needs a Lease the scheduler and the controllers may write.** [ADR 0018](./0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md), Decision 6, adds the Lease of `coordination.gaon.garam.sh` for it. The rules of ADR 0008 name no resource of that group.
- **A garbage collector must find and remove dependents of any kind.** The collector of garamsh/gaon#120 reads every kind to find the objects that name an owner, including kinds an operator defines, and deletes a dependent whose owner is gone. ADR 0008 gave the controllers no `delete`, "because the table of writers gives `DELETE` to users", and `get`, `list` and `watch` on four named resources only.
- **A rule can name a custom resource only by its group and plural,** and a definition can appear after the role is written. `*` in `apiGroups` and `resources` ([ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md), Context) is the one way to cover a kind not yet defined.
- **Bootstrap never overwrites** a role that exists (ADR 0008, Decision 6), so the change reaches a cluster only where the role is missing.

## Decision

**`gaon:scheduler` and `gaon:controllers` each gain `get`, `create` and `update` on `leases` in group `coordination.gaon.garam.sh`, and `gaon:controllers` gains `get`, `list`, `watch` and `delete` on every resource of every group. The other rules of ADR 0008, Decision 6, stand.**

1. **The Lease rule.** `get`, `create` and `update`, which are what taking, renewing and reading a Lease take, and no `delete`, `list` or `watch`. The scheduler and the controllers each use their own Lease name.
2. **The controllers' read and delete.**
   - One rule: `apiGroups: ["*"]`, `resources: ["*"]`, `verbs: ["get", "list", "watch", "delete"]`, with no label selector.
   - It replaces the rule that gave `get`, `list` and `watch` on `microvms`, `nodes`, `leases` and `certificatesigningrequests` of group `compute.gaon.garam.sh`, which it covers.
   - `*` in `resources` covers a subresource, written `resource/sub`, for the verbs the rule lists.
3. **What the controllers still lack.** `create` and `update` on anything but the rules ADR 0008 gave: `microvms` `create` and `update`, and the `status` of `microvms`, `nodes`, `volumes` and `certificatesigningrequests`. `escalate` and `bind` are not granted, and the escalation rule ([ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md), Decision 7) counts the new rule like any other, so the controllers cannot grant a role of their own more than they hold.
4. **The scheduler's rules** are those of ADR 0008, with the Lease rule added. It gains no `delete`, and no read outside the resources ADR 0008 listed.
5. **Event.** Neither role gets a rule on `events` of `compute.gaon.garam.sh` beyond what the controllers' read and delete give. An operator binds a role to a component that reports Events.

## Consequences

- **The controllers can read every object in the Store,** including every kind a definition adds, and delete any of them. The escalation rule is the only limit on what role they hold, and a compromised controllers certificate can remove any resource. ADR 0008 gave that certificate a smaller reach; the collector needs this one.
- **A cluster that bootstrapped before this ADR keeps its old roles.** The API server writes only a role that is missing. An operator who wants the Lease rules or the collector on such a cluster deletes the two roles and restarts the API server, or adds the rules by hand.
- **`gaon:admin` is unchanged,** and already covers every rule here.
- **A selector rule held with the controllers' rule does not narrow it.** Rules are judged one at a time (ADR 0018, Decision 8), and a rule without a selector allows every object.
- **The table of ADR 0008, Decision 6, is read with this ADR** for the two roles it names. [resource-api.md](../resource-api.md) §RBAC states the table as it stands now.

## Signs the decision was wrong

- The controllers' certificate is found to be reachable by a caller that should not remove objects, and a narrower `delete` is wanted, by group or by kind.
- The collector needs `update` on every kind, to remove a finalizer, and a second wildcard rule is added for it.
- A deployment edits the roles after every upgrade to restore what bootstrap would not write.
- The scheduler needs to read an object of a group it does not hold.

## Rejected alternatives

- **Naming the kinds the collector reads.** A custom resource defined later is not covered, and the role would need an edit with each definition.
- **A separate role for the collector.** The collector runs inside the controllers and shares its certificate, so a second role bound to the same group is the same grant with another name.
- **`delete` on `leases` for the scheduler and the controllers.** A holder releases by an update that empties `holder`, and an expired Lease is taken over by an update. Nothing needs a delete.
- **Reconciling the bootstrap roles at every start.** ADR 0008 rejected it: it overwrites an operator's edit.
