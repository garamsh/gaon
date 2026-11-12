# ADR 0008: The API server authorizes certificate callers with cluster roles, bindings and a Node authorizer

> Status: superseded by ADR-0021
> Date: 2026-11-12

Drafted for garamsh/gaon#75, under goal garamsh/gaon#74. The design is the PM's, taken under the project owner's decisions in #74 (Kubernetes semantics, a gaon-native implementation, additive only, no namespaces) and recorded in #75 §Design. It amends [ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md), Decision 4. Enforcement in the API server is garamsh/gaon#76.

## Context

- **Any authenticated caller may write any path** ([ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md), Decision 4), so a compromised Node Agent can write other Nodes' resources, and [ADR 0007](./0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) lets a token obtain a certificate for any Node name.
- **The identity is the certificate's CN** (ADR 0005, Decision 2). A bootstrap token's identity is `bootstrap:<id>` and is limited to one create and one read (ADR 0007, Decision 2).
- **Who writes what is already fixed** by the one-writer-per-section rule ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md), Decision 1) and the table of writers in [resource-api.md](../resource-api.md) §Paths. Nothing enforces it.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12:**
  - "Using RBAC Authorization", <https://kubernetes.io/docs/reference/access-authn-authz/rbac/>. The page body states no version; its footer reads "Last modified August 26, 2026", and its version selector lists v1.33 to v1.37, with v1.37 the one on the main site at the reading.
  - "Using Node Authorization", <https://kubernetes.io/docs/reference/access-authn-authz/node/>, which states that the Node authorizer is stable since Kubernetes v1.34.
  - What was taken from them: permissions are purely additive, there are no deny rules; `*` in `apiGroups`, `resources` and `verbs` refers to all of them; a subresource is written with a slash, as in `pods/log`; a role is created or updated only by a caller that already holds all of its permissions, or may perform `escalate`; a binding only by a caller that holds all permissions of the referenced role, or may perform `bind`; a binding's `roleRef` cannot change after creation; and the Node authorizer limits a kubelet to its own Node and the objects related to it.
  - What the pages do not state, and so is not taken: a wildcard inside a name (`pods/*`, `*/scale`).

## Decision

**The API server, started in the `RBAC` authorization mode, authorizes every request made with a client certificate by the Node authorizer and then by roles bound to the caller's identity, and refuses what neither allows.**

1. **Identity.**
   - The user is the certificate's CN, and the groups are the Organization values of its subject.
   - A bootstrap token keeps `bootstrap:<id>` and the narrowed rule of ADR 0007, Decision 2. Neither the Node authorizer nor RBAC is consulted for it, and RBAC does not widen what it may do.
   - The prefix `gaon:` is reserved for groups, roles and bindings that gaon creates, and `bootstrap:` for users, as Kubernetes reserves `system:`. Nothing but the escalation rule (7) stops a caller from creating a name with the prefix.
2. **Mode flag.**
   - `gaon-apiserver --authorization-mode` is `AlwaysAllow` or `RBAC`; any other value stops the server at start.
   - The default is `AlwaysAllow`, which is ADR 0005's behaviour, so existing deployments, naru's harness and the end-to-end test are unchanged.
   - `RBAC` requires the TLS flags of ADR 0005, because a request with no client certificate has no identity to authorize; the server stops at start without them.
   - In `AlwaysAllow` the roles and bindings can be written and read and decide nothing.
3. **Resources.** The group `rbac.gaon.garam.sh/v1alpha1` is new beside the groups of ADR 0003, Decision 8, and has its own root, `/apis/rbac.gaon.garam.sh/v1alpha1`.
   - `ClusterRole` holds `rules`. Each rule has `apiGroups`, `resources` and `verbs`, and each is a non-empty list of names. A subresource is `machines/status` or `machines/binding`. `*` is a name that matches every value, and only as the whole name.
   - The verbs are `get`, `list`, `watch`, `create`, `update`, `delete` and `*`.
   - `ClusterRoleBinding` holds one `roleRef`, of kind `ClusterRole` and a name, and `subjects`, each of kind `User` or `Group` with a name. `roleRef` cannot change after creation.
   - The paths are `PUT` (a create without a version, an update with one), `GET` of one and of a list, `GET ?watch=true` and `DELETE`, on `clusterroles` and `clusterrolebindings`, with the watch event shape of every other kind.
4. **Rule matching.** A request is allowed when any rule of any ClusterRole bound to the caller's user or one of its groups has its API group, its resource (the subresource included, as `machines/status`) and its verb listed or matched by `*`. There are no deny rules. A request is described by:
   - its verb: `PUT` without a version is `create` and with one is `update`, `DELETE` is `delete`, `GET` of one object is `get`, of a collection is `list`, with `watch=true` is `watch`, and `POST /machines/{name}/binding` is `create` on `machines/binding`;
   - its API group and its resource, from the path.
5. **Node authorizer, ahead of RBAC in `RBAC` mode.** It allows a request from a caller whose CN equals a Node's name, or the name of the Node it is creating, for these and only these:
   - create its own Node, and read it and write its `status`;
   - read and write its own Lease, which is named as its Node;
   - read Machines, and write a Machine bound to it: its `status`, and its own finalizer through `PUT /machines/{name}`;
   - write `status.attachment` of the Volumes its Machines use;
   - create CertificateSigningRequests, and read one it created, for renewal.

   It never denies. A request it does not allow goes to RBAC, as in Kubernetes. "Bound to it" is `spec.nodeName` equal to the CN.
6. **Bootstrap policy.** At start in `RBAC` mode the API server writes to the Store each of the following that is missing, and never overwrites one that exists, so an operator's edit survives a restart:
   - the ClusterRoles `gaon:scheduler`, `gaon:controllers` and `gaon:admin`;
   - a ClusterRoleBinding of each to the group `gaon:schedulers`, `gaon:controllers` and `gaon:admins` respectively.

   The rules, derived from the table of writers in [resource-api.md](../resource-api.md) §Paths and from what the scheduler and the controllers call, in group `compute.gaon.garam.sh`:

   | Role | Rules |
   |---|---|
   | `gaon:scheduler` | `machines` and `nodes`: `get`, `list`, `watch`; `machines/binding`: `create`; `machines/status`: `update` (it writes the `Scheduled` condition, [scheduler.md](../scheduler.md) §A MicroVM that fits no Node) |
   | `gaon:controllers` | `machines`, `nodes`, `leases` and `certificatesigningrequests`: `get`, `list`, `watch`; `machines`: `create`, `update`; `machines/status`, `nodes/status`, `volumes/status` and `certificatesigningrequests/status`: `update` |
   | `gaon:admin` | every API group, resource and verb |

   `gaon:controllers` has no `delete`, because the table of writers gives `DELETE` to users, and no write to `leases`, which belong to the Node Agent.
7. **Escalation rule.** A caller may create or update a ClusterRole only if it already holds every permission the role grants, and a ClusterRoleBinding only if it holds every permission of the role it references. A permission is held when a rule of a role bound to the caller's user or groups covers it, `*` covering any value. Deleting needs only `delete`. The Node authorizer's permissions are not counted.
8. **Refusal.** A request an authorizer denies is `Forbidden`, 403, which exists in the contract.

## Consequences

- **A compromised Node Agent is limited to its own Node's resources** and to reading Machines, instead of writing any path. A token can still obtain a certificate for any Node name (ADR 0007), so it can become any Node, and its limits are that Node's.
- **A certificate whose CN equals a Node's name is that Node to the authorizer**, whatever else it is. The scheduler's, the controllers' and the administrator's certificates carry CNs that are not Node names.
- **The signer must refuse an Organization in a request's subject.** It signs the subject it is given ([csr-signer.md](../csr-signer.md) §What it signs) and a token holder or a Node Agent chooses that subject, so a request for `O=gaon:admins` would return an administrator's certificate. Until the signer refuses a subject that carries an Organization, `RBAC` mode gives a token holder every permission. This ADR does not change the signer; the change is owed to `controllers/` and `csr-signer.md` before #76 is relied on.
- **The Node authorizer cannot narrow a list or a watch of Machines.** The API has no selector, and a Node Agent must see a Machine that is no longer bound to it, to stop it and to leave another Node's finalizer alone. So reads of Machines are open to every Node; only writes are limited to bound Machines. Kubernetes' Node authorizer limits reads of Secrets and similar objects to what a kubelet's Pods use, and gaon has no such objects yet.
- **The authorizer cannot check which part of a Machine a write changes.** That a Node Agent changes only its own finalizer through `PUT /machines/{name}` is the API server's field rule under invariant 3, not the authorizer's.
- **No `escalate` and `bind` verbs.** Kubernetes lets a caller bypass the check with them. Here only a caller that holds a permission can grant it, so only `gaon:admin`, which holds `*`, can grant everything.
- **No lockout recovery in the API.** In `RBAC` mode nothing is allowed to a caller outside the three bootstrap groups until an administrator binds something. The administrator's certificate must carry `O=gaon:admins`, and with none, the mode cannot be used. Deleting a bootstrap role removes the permission until the next start recreates it.
- **A role granting `*` grants a resource added later.** Kubernetes warns of the same.
- **Authorization is by name only.** There are no `resourceNames`, no namespaces and no non-resource URLs, so a role cannot name one Machine. A later step of #74 may add namespaces.
- **Rejected: a Node group in the certificate** (Kubernetes' `system:nodes`). It would put the Node authorizer's rule in the certificate the signer issues, which is the signer's gap above, and #75 binds the rule to the CN.
- **Rejected: RBAC for the bootstrap token.** The narrowed rule of ADR 0007 is a fixed path rule, and a role could widen what a token may mint or read.
- **Rejected: bootstrap roles reconciled at every start.** It would overwrite an operator's edit; creating the missing ones does not.

## Signs the decision was wrong

- Operators bind `gaon:admin` to every caller to get work done, because the six verbs and name-only rules are too coarse for what they need.
- The scheduler or the controllers need a permission the bootstrap roles lack, and each deployment edits the roles.
- A Node Agent needs to read or write something the Node authorizer does not list, and the authorizer's list grows with every feature.
- The Organization-in-subject gap is exploited, or a certificate for a Node name is used to read other Nodes' Machines in a way that matters.
- An operator is locked out in `RBAC` mode with no way back but restarting in `AlwaysAllow`.
