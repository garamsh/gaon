# ADR 0033: A DNS server run as a Deployment answers a Service at `<service>.svc.<zone>`, and its Service address is the resolver mvmd gives every MicroVM

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Goal #117, decision D6: "DNS names for Services are served by a DNS server delivered as a workload, and MicroVMs are given its address as their resolver." garam's deployment contract waits on the form of a Service's name as a MicroVM resolves it (issue #187, item 2): its settings carry internal addresses, and the policy service's certificate must name the host its callers use.

[ADR 0027](./0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) gives a Service a `clusterIP`, or none when headless, and gives Endpoints the addresses of its ready MicroVMs. Every kind is cluster-scoped, so a Service has no namespace, and Kubernetes' name has one. A reader who knows Kubernetes should resolve a Service as expected, so the model is Kubernetes' DNS specification, `docs/specification.md`, schema version 1.1.0, in `kubernetes/dns` at commit `4f2becc8aeb54116398e6a33e34af756db5b9fe7`, and each departure needs a reason.

How a resolver reaches a MicroVM is mvmd's. At commit `223e3eca764caf60b8dd72c7399b83ff6b437d49` of garamsh/mvmd, the node configuration has `network.resolvers` (`daemon/internal/app/config.go`), mvmd sends them to the guest with the boot description, and the Guest Agent writes them as `nameserver` lines to `/etc/resolv.conf` and writes nothing else there (`guest-agent/crates/guest-agent/src/network.rs`, `docs/architecture/guest.md` "The network"). `CreateMachineRequest` of runtime API `v1alpha2` has no field for a resolver. The node configuration is naru's to write, as `network.no_masquerade` is ([deployment.md](../deployment.md) §MicroVM network).

Alternatives for the resolver's address were an address of a Node, which ties every MicroVM to one Node, and the address of one DNS MicroVM, which changes when it is replaced. Alternatives for the name were Kubernetes' `<service>.<namespace>.svc.<zone>` with a fixed namespace label, and the form without one.

## Decision

**A new unit, `dns/`, binary `gaon-dns`, serves DNS for Services. It is run as a Deployment of MicroVMs behind a `ClusterIP` Service whose address is the resolver of every MicroVM, set in mvmd's node configuration by naru.** The contract is stated in [dns.md](../dns.md).

1. **The name of a Service** is `<service>.svc.<zone>`. `<zone>` is the cluster domain, `cluster.local` unless the unit is given another. There is no namespace label because there is no namespace. Names compare case-insensitively.
2. **A Service with a `clusterIP`,** of type `ClusterIP` or `LoadBalancer`, answers that address: an `A` record for an IPv4 one, an `AAAA` record for an IPv6 one, and no data for the other type.
3. **A headless Service** answers one `A` or `AAAA` record for each address in its Endpoints, and `NXDOMAIN` when there is none. An address with a `targetRef` whose MicroVM name is a DNS label also answers at `<microvm>.<service>.svc.<zone>`.
4. **A `LoadBalancer` Service** answers its `clusterIP` inside the cluster, never its LAN address.
5. **SRV, PTR and the `dns-version` record are not served.**
6. **The TTL** of every answer, and of the start of authority of a negative one, is 5 seconds.
7. **The resolver.** The Service of the DNS Deployment requests its `clusterIP`, and naru writes the same address to `network.resolvers` of every Node's mvmd. gaon passes nothing per MicroVM, and a MicroVM cannot choose another resolver. No search domain is delivered, so a short name does not resolve.
8. **Forwarding.** A name under `<zone>` is answered from the Services and never forwarded: one that is not a Service is `NXDOMAIN`. Any other name is forwarded unchanged to the upstream resolvers the unit is given, and their answer is returned; with none given it is `REFUSED`.
9. **The unit's state** is a list and watch of Services and Endpoints through the public API under its own ServiceAccount. It answers `SERVFAIL` for a name under `<zone>` until its first list completes, and after that serves the last state it read while the API server is unreachable.
10. **Departures from Kubernetes' DNS specification 1.1.0,** each for the reason given:
    - No `<ns>` label in a name (§2.3.1, §2.4.1), because Services have no namespace.
    - No `SRV` (§2.3.2, §2.4.2), no `PTR` (§2.3.3, §2.4.3) and no `dns-version` `TXT` (§2.2), because no consumer in #117 or #187 needs them and each is one more answer to keep right; a client reads a port from the Service.
    - No `CNAME` for `ExternalName` (§2.5), because the type is not offered (ADR 0027).
    - A per-host name is the MicroVM's name from `targetRef`, not an endpoint `hostname` field, because Endpoints has none and a StatefulSet's replicas already have stable names ([ADR 0030](./0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md)).
    - "Ready" is membership of `subsets[].addresses`, with no `tolerate-unready-endpoints`, because `publishNotReadyAddresses` is not offered.
    - No search domain, so Kubernetes' short names (`<service>`, `<service>.<ns>`) do not resolve, because mvmd writes `nameserver` lines only.
    - A single resolver address with no per-MicroVM `dnsPolicy` or `dnsConfig`, because mvmd's resolvers are a node's.

## Consequences

- garam names `<service>.svc.cluster.local` in settings and in the certificate of a Service's callers.
- Every Service is one flat name in the cluster. Two Services cannot share a name in two scopes until namespaces exist.
- When namespaces arrive, the form `<service>.svc.<zone>` needs a new ADR that says where existing names go. A certificate that names the old form keeps working only if that ADR keeps answering it.
- Every MicroVM's resolution, outside names included, depends on the DNS Service. mvmd takes a list of resolvers, so a second address is possible, but this decision names one.
- A MicroVM reaches the resolver through the Service address the network plugin implements (issue #196) and only if its network policy allows that address on port 53. The default network plugin's egress prefixes must include the service prefix.
- A headless Service answers nothing until its Endpoints has an address, which for a selector Service is the endpoints controller's (issue #192).
- The serving issue creates `dns/` and adds it to [structure.md](../structure.md); this ADR creates none of it.
- The points left to the serving issue are listed in [dns.md](../dns.md) §Open questions.

## Signs the decision was wrong

- A certificate or a setting needs a namespaced name, and `<service>.svc.<zone>` has to be answered beside another form.
- A client needs `SRV` for a named port, or a library refuses a Service name with no `PTR`.
- A workload expects short names, and operators add a search domain by hand.
- A cluster needs two resolvers, or a resolver per MicroVM, and mvmd's node list is too coarse.
- An outage of the DNS Deployment stops MicroVMs that need no Service name, which forwarding was meant to serve.
