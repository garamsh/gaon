# ADR 0033: A DNS server run as a Deployment answers a Service at `<service>.default.svc.<zone>`, and its Service address is the resolver mvmd gives every MicroVM

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Goal #117, decision D6: "DNS names for Services are served by a DNS server delivered as a workload, and MicroVMs are given its address as their resolver." garam's deployment contract waits on the form of a Service's name as a MicroVM resolves it (issue #187, item 2): its settings carry internal addresses, and the policy service's certificate must name the host its callers use.

[ADR 0027](./0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) gives a Service a `clusterIP`, or none when headless, and gives Endpoints the addresses of its ready MicroVMs. Every kind is cluster-scoped, so a Service has no namespace, and Kubernetes' name has one. A reader who knows Kubernetes should resolve a Service as expected, so the model is Kubernetes' DNS specification, `docs/specification.md`, schema version 1.1.0, in `kubernetes/dns` at commit `4f2becc8aeb54116398e6a33e34af756db5b9fe7`, and each departure needs a reason.

How a resolver reaches a MicroVM is mvmd's. At commit `223e3eca764caf60b8dd72c7399b83ff6b437d49` of garamsh/mvmd, the node configuration has `network.resolvers` (`daemon/internal/app/config.go`), mvmd sends them to the guest with the boot description, and the Guest Agent writes them as `nameserver` lines to `/etc/resolv.conf` and writes nothing else there (`guest-agent/crates/guest-agent/src/network.rs`, `docs/architecture/guest.md` "The network"). `CreateMachineRequest` of runtime API `v1alpha2` has no field for a resolver. The node configuration is naru's to write, as `network.no_masquerade` is ([deployment.md](../deployment.md) §MicroVM network).

Alternatives for the resolver's address were an address of a Node, which ties every MicroVM to one Node, and the address of one DNS MicroVM, which changes when it is replaced. Alternatives for the name were `<service>.svc.<zone>`, which has no namespace label, and Kubernetes' `<service>.<namespace>.svc.<zone>` with the label fixed to `default`. The first is shorter and says nothing false today, but it is not the form of §2.3.1 and §2.4.1, so configuration written for Kubernetes, Helm-rendered configuration among it, does not resolve, and when namespaces arrive (Namespace is already a core resource) the names would have to move. The second keeps answering unchanged when Services have namespaces, with every existing Service in `default`.

## Decision

**A new unit, `dns/`, binary `gaon-dns`, serves DNS for Services. It is run as a Deployment of MicroVMs behind a `ClusterIP` Service whose address is the resolver of every MicroVM, set in mvmd's node configuration by naru.** The contract is stated in [dns.md](../dns.md).

1. **The name of a Service** is `<service>.default.svc.<zone>`, Kubernetes' form with the namespace label fixed to `default` until Services have namespaces. `<zone>` is the cluster domain, `cluster.local` unless the unit is given another. `<service>.svc.<zone>` is not answered. Names compare case-insensitively.
2. **A Service with a `clusterIP`,** of type `ClusterIP` or `LoadBalancer`, answers that address: an `A` record for an IPv4 one, an `AAAA` record for an IPv6 one, and no data for the other type.
3. **A headless Service** answers one `A` or `AAAA` record for each address in its Endpoints, and `NXDOMAIN` when there is none. An address with a `targetRef` whose MicroVM name is a DNS label also answers at `<microvm>.<service>.default.svc.<zone>`.
4. **A `LoadBalancer` Service** answers its `clusterIP` inside the cluster, never its LAN address.
5. **SRV, PTR and the `dns-version` record are not served.**
6. **The TTL** of every answer, and of the start of authority of a negative one, is 5 seconds.
7. **The resolver.** The Service of the DNS Deployment requests its `clusterIP`, and naru writes the same address to `network.resolvers` of every Node's mvmd. gaon passes nothing per MicroVM, and a MicroVM cannot choose another resolver. No search domain is delivered, so a short name does not resolve.
8. **Forwarding.** A name under `<zone>` is answered from the Services and never forwarded: one that is not a Service is `NXDOMAIN`. Any other name is forwarded unchanged to the upstream resolvers the unit is given, and their answer is returned; with none given it is `REFUSED`.
9. **The unit's state** is a list and watch of Services and Endpoints through the public API under its own ServiceAccount. It answers `SERVFAIL` for a name under `<zone>` until its first list completes, and after that serves the last state it read while the API server is unreachable.
10. **Departures from Kubernetes' DNS specification 1.1.0,** each for the reason given:
    - The `<ns>` label is the constant `default` (§2.3.1, §2.4.1), because Services have no namespace yet.
    - No `SRV` (§2.3.2, §2.4.2), no `PTR` (§2.3.3, §2.4.3) and no `dns-version` `TXT` (§2.2), because no consumer in #117 or #187 needs them and each is one more answer to keep right; a client reads a port from the Service.
    - No `CNAME` for `ExternalName` (§2.5), because the type is not offered (ADR 0027).
    - A per-host name is the MicroVM's name from `targetRef`, not an endpoint `hostname` field, because Endpoints has none and a StatefulSet's replicas already have stable names ([ADR 0030](./0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md)).
    - "Ready" is membership of `subsets[].addresses`, with no `tolerate-unready-endpoints`, because `publishNotReadyAddresses` is not offered.
    - No search domain, so Kubernetes' short names (`<service>`, `<service>.<ns>`) do not resolve, because mvmd writes `nameserver` lines only.
    - A single resolver address with no per-MicroVM `dnsPolicy` or `dnsConfig`, because mvmd's resolvers are a node's.

## Consequences

- garam names `<service>.default.svc.cluster.local` in settings and in the certificate of a Service's callers.
- Every Service is one name in the cluster, under `default`. Two Services cannot share a name in two scopes until namespaces exist.
- When namespaces arrive, existing Services belong to `default` and their names stay valid. A new ADR decides how a Service of another namespace is named and whether `default` stays the namespace of a Service created without one.
- A caller that writes `<service>.svc.<zone>` gets `NXDOMAIN`.
- Every MicroVM's resolution, outside names included, depends on the DNS Service. mvmd takes a list of resolvers, so a second address is possible, but this decision names one.
- A MicroVM reaches the resolver through the Service address the network plugin implements (issue #196) and only if its network policy allows that address on port 53. The default network plugin's egress prefixes must include the service prefix.
- A headless Service answers nothing until its Endpoints has an address, which for a selector Service is the endpoints controller's (issue #192).
- The serving issue creates `dns/` and adds it to [structure.md](../structure.md); this ADR creates none of it.
- The points left to the serving issue are listed in [dns.md](../dns.md) §Open questions.

## Signs the decision was wrong

- A caller or a certificate needs `<service>.svc.<zone>`, and it has to be answered beside the form taken.
- Namespaces arrive with a different namespace for existing Services than `default`, and the names have to move.
- A client needs `SRV` for a named port, or a library refuses a Service name with no `PTR`.
- A workload expects short names, and operators add a search domain by hand.
- A cluster needs two resolvers, or a resolver per MicroVM, and mvmd's node list is too coarse.
- An outage of the DNS Deployment stops MicroVMs that need no Service name, which forwarding was meant to serve.
