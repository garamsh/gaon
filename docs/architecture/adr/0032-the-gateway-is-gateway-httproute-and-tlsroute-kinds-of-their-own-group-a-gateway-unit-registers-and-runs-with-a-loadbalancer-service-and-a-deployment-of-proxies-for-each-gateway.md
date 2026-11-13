# ADR 0032: The gateway is Gateway, HTTPRoute and TLSRoute kinds of their own group, which a gateway unit registers and runs, with a LoadBalancer Service and a Deployment of proxies for each Gateway

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#194.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **Goal #117 decides a gateway** (D6): a TLS-terminating workload kind with its controller, delivered as an extension. D14 has it read a certificate from a `Secret`; D16 and D18 give what it serves: several listeners at one LAN address that is plain HTTP, TLS-terminating or TLS passthrough, routes by host and path prefix, an opt-in prefix replacement, `X-Forwarded-For` and `X-Forwarded-Proto`, and ACME HTTP-01 on port 80. D18 fixes the model: the Kubernetes Gateway API's.
- **The consumers need names.** garam's deployment contract (#187 item 1) and aurora's platform services (#159 item 2) wait for kind and field names, and for `Host` and the path to reach a backend as sent.
- **The pieces exist.** A `LoadBalancer` Service holds a LAN address that the pool grants to one holder, and the network plugin announces it; an Endpoints lists a Service's ready MicroVMs ([ADR 0027](./0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md)). A Deployment keeps replicas of a MicroVM template ([ADR 0026](./0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md)). A MicroVM authenticates with a ServiceAccount token ([ADR 0020](./0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md)).
- **A custom resource's schema is a subset of OpenAPI** ([ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md), [ADR 0031](./0031-a-schema-node-may-hold-an-integer-or-a-string-with-x-kubernetes-int-or-string-and-no-type.md)): it has no array length bounds and no cross-field rules, and there are no namespaces.
- Alternatives for the model were Kubernetes' `Ingress`, which has no TLS passthrough and no prefix replacement, and a Gateway of our own design, which D18 rejects for the reader who knows the Gateway API.

## Decision

**Three kinds of the group `gateway.gaon.garam.sh`, version `v1alpha1`, with the fields of `gateway.md`, registered as custom resources by a new unit `gateway/` that also runs the controller and the proxy. The controller gives each Gateway a `LoadBalancer` Service that holds its address and a Deployment of proxy MicroVMs behind it.**

1. **Kinds.** `Gateway`, `HTTPRoute` and `TLSRoute`, cluster scoped, with a `status` subresource. Their names and fields are those of the Gateway API at v1.2.0 (`Gateway` and `HTTPRoute` of `gateway.networking.k8s.io/v1`, `TLSRoute` of `v1alpha2`) less the departures below.
2. **One unit, two binaries.** `gateway/` registers the definitions, as `workloads/` does, and holds `gaon-gateway-controller` and `gaon-gateway-proxy`. The controller is a client of the public API with its own certificate and acts only while it holds a coordination Lease ([ADR 0023](./0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)). The proxy runs as the MicroVMs of the Deployment, not as a host service, and reads the API with its ServiceAccount token.
3. **The controller decides attachment, the proxy serves it.** The controller validates Gateways and routes and writes their `status`. The proxy forwards for a route only where the route's status says it is attached, so the rules of attachment have one site.
4. **A Gateway's address and replicas.** For each Gateway the controller creates a `LoadBalancer` Service whose `loadBalancerIP` is the Gateway's requested address, and a Deployment of the proxy that the Service selects. Both are owned by the Gateway. The Gateway's address is the Service's `status.loadBalancer.ingress`.
5. **Departures from the Gateway API,** each with its reason:
   - No `GatewayClass`, no `gatewayClassName` and no `controllerName`: one implementation.
   - No namespaces, so no `namespace` in a reference, no `allowedRoutes` and no `ReferenceGrant`: a route attaches to any Gateway it names.
   - Listener protocols `HTTP`, `HTTPS` and `TLS` only, and `TLS` only with mode `Passthrough`: no TCP or UDP route is needed, and terminated TLS is `HTTPS`. One protocol per port.
   - One certificate reference per listener: several hostnames on a port are several listeners.
   - At most one address.
   - An HTTPRoute matches the path prefix only and has one filter, `URLRewrite` with `ReplacePrefixMatch`. It has no exact or regular-expression path, header, query or method match, no redirect, no header or mirror filter, no timeouts and no weights: #117 needs none, and each is added by the decision that needs it.
   - One backend per rule, a Service and a port.
   - `GRPCRoute`, `TCPRoute` and `UDPRoute` are absent.
   - The schema subset cannot bound an array or relate two fields, so the controller checks them and reports in `status` where Kubernetes refuses at write.
   - `v1alpha1` for all three: the project's kinds are alpha.
   - No tie-break by creation time, as a gaon object has no creation timestamp: name order decides.

## Consequences

- A reader who knows Gateway, HTTPRoute and TLSRoute reads these kinds, and garam and aurora can name fields.
- A gateway reuses the Service, Endpoints, Deployment and ServiceAccount contracts and adds no write path to the core. The network plugin announces the address and forwards to the proxies without a gateway-specific rule.
- A connection reaches a proxy through the Service, so a client's address reaches the proxy only if the plugin forwards without changing the source. `gateway.md` leaves that to the serving issue.
- A Gateway's address cannot change after create, because a Service's `loadBalancerIP` cannot; the Gateway is deleted and created again.
- A route is served after the controller has written its status, so a change takes effect a status write later than the write of the route.
- A new unit, `gateway/`, is added to `structure.md` by the issue that creates it.
- `resource-api.md` §Open questions and §Naming and groups still name a `Gateway` of `apps.gaon.garam.sh` as an unwritten default extension. The group is `gateway.gaon.garam.sh`.

## Signs the decision was wrong

- A user needs a match or filter outside the list above often enough that the departures cost more than they saved, or a listener on one port with two protocols.
- The proxy and the controller disagree on attachment, which one decision site was to prevent.
- A single address on one Service cannot carry the traffic, or the plugin's forwarding hides every client's address and `X-Forwarded-For` is useless.
- A change of address is wanted without an outage.
- Users read `v1alpha1` kinds named like the Gateway API's and expect a field the contract left out.
