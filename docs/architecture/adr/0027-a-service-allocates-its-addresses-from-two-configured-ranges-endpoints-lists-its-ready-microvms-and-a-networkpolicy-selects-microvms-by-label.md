# ADR 0027: A Service allocates its addresses from two configured ranges, Endpoints lists its ready MicroVMs and a NetworkPolicy selects MicroVMs by label

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Goal #117 decides a stable address for a set of MicroVMs (D6), the traffic limits of an operator's workloads (D17, issue #165) and a LAN address a gateway holds (D18). None has a kind in `lib/api`, and `resource-api.md` lists Service and NetworkPolicy as extensions. A reader who knows Kubernetes should read these kinds without surprise, so the model is Kubernetes' and each departure needs a reason.

Three API servers share one three-member etcd (ADR 0023), so an address handed out by one must be refused by the others, and must survive a restart of any of them. Every other kind is cluster-scoped, because the namespace is an open question of `resource-api.md`.

Alternatives for Endpoints were Kubernetes' `EndpointSlice` and its `Endpoints`. For a NetworkPolicy peer they were a selector in the text form `Selector` already parses and Kubernetes' structured `LabelSelector`.

## Decision

Service, Endpoints and NetworkPolicy are cluster-scoped kinds of `compute.gaon.garam.sh/v1alpha1`, served by the core, with the fields, validation and semantics of `resource-api.md` §Service, Endpoints and NetworkPolicy. They are core, not extensions, because the core must guarantee that one address has one holder across three API servers, which a controller outside the write path cannot.

1. **Service** has a `ClusterIP` or `LoadBalancer` type. The API server allocates `clusterIP` from `--service-cluster-ip-range` and, for a `LoadBalancer`, a LAN address from `--load-balancer-pool`, in the write that creates the Service. Each held address is a key in the Store created in the same transaction as the Service, with a compare that it is absent, and removed in the transaction that removes the Service. A conflict retries with another address, at most 5 times, then answers `Conflict`. A requested address outside its range is `Invalid`. `type`, `clusterIP` and `loadBalancerIP` never change: an update that leaves `clusterIP` or `loadBalancerIP` empty keeps the stored value, as Kubernetes does, and a different non-empty value is `Invalid`.
2. **A gateway** holds a specific LAN address through a `LoadBalancer` Service whose `loadBalancerIP` names it. The pool refuses a second holder.
3. **Endpoints**, not `EndpointSlice`. The endpoints controller writes it for a Service with a selector, under a coordination Lease, and lists the MicroVMs that are `Ready`, not deleting and have an address. The user writes it for a Service without one.
4. **NetworkPolicy** takes Kubernetes' semantics. A peer's and the policy's `podSelector` is the structured `LabelSelector`, which `LabelSelector.Selector` turns into the `Selector` already used for lists, so one matcher serves both.
5. **Departures from Kubernetes,** each for the reason given:
   - No namespace and no `namespaceSelector`, because the namespace is undecided; it returns with namespaces.
   - Types `ClusterIP` and `LoadBalancer` only, and `TCP` and `UDP` only, because nothing in #117 needs `NodePort`, `ExternalName` or `SCTP`, and mvmd's ports are the two.
   - `loadBalancerIP` is the request for a LAN address, not deprecated, because the API server grants the address from its own pool and no cloud controller does.
   - No `endPort` and no named port in a NetworkPolicy port, because the network plugin matches numbers.
   - The other Kubernetes Service fields (`sessionAffinity`, `externalIPs`, `externalTrafficPolicy`, `ipFamilies`, `publishNotReadyAddresses`, `loadBalancerClass`) are absent until a decision needs them.

## Consequences

- Service, Endpoints and NetworkPolicy move from the default extensions into the core in `resource-api.md` §Naming and groups. Network stays an extension a Driver interprets, and the network plugin enforces NetworkPolicy.
- The Store gains an `/allocations/` prefix that the API server writes. Anyone who lists or backs up the Store sees it. A key left without its Service, by a write that bypassed the API server, holds the address until it is removed by hand.
- The three API servers must be started with the same two ranges. The contract does not detect a difference; it is an open question.
- A client that creates a Service sees `spec.clusterIP` filled in on the answer.
- Endpoints is one object per Service, and a large Service rewrites the whole object at each change. The bound of 1000 addresses is Kubernetes' for the same reason; a Service beyond it needs `EndpointSlice`, a later decision.
- A reader who knows Kubernetes' NetworkPolicy reads this one the same, except for the namespace and the port forms above.
- The serving issues settle what this ADR leaves open, listed in `resource-api.md` §Open questions: the Node authorizer's and the bootstrap roles' rules for the three kinds, CLI short names, dual-stack Services, whether Endpoints lists a MicroVM that is not `Ready`, more than one pool or prefix, a flag mismatch between API servers, and a NetworkPolicy exemption for traffic between a MicroVM and its own Node.

## Signs the decision was wrong

- A Service with more than 1000 ready MicroVMs is wanted, and Endpoints is the bottleneck of the watch.
- Two Services are observed holding one address, or an address stays held after its Service is gone: the transaction did not do what this ADR says.
- A user needs the same name in two scopes, and a Service name that is a label cannot be moved into a namespace.
- The three API servers disagree on a range in a running cluster, and nothing detected it.
- Operators ask for `endPort`, named ports or `SCTP` in a policy often enough that the departures cost more than they saved.
