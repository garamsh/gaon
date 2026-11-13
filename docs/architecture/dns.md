# DNS

What a MicroVM resolves for a Service: the name form, what the DNS server answers, how that server is delivered and how its address reaches every MicroVM.

Basis: [ADR 0033](./adr/0033-a-dns-server-run-as-a-deployment-answers-a-service-at-service-svc-zone-and-its-service-address-is-the-resolver-mvmd-gives-every-microvm.md). Kubernetes' statements hold for `kubernetes/dns` at commit `4f2becc8aeb54116398e6a33e34af756db5b9fe7`, `docs/specification.md`, schema version 1.1.0. The mvmd statements hold for garamsh/mvmd at commit `223e3eca764caf60b8dd72c7399b83ff6b437d49`, runtime API `v1alpha2`. The gaon statements hold for garamsh/gaon at commit `34d75c3170c239a1b2b96f2efd41ef3eb6b7318b`. This is a contract: no code serves it yet.

## Current decisions

### Names

- **Form:** `<service>.svc.<zone>`. `<zone>` is the cluster domain, `cluster.local` by default and set in the unit's configuration. A Service has no namespace ([resource-api.md](./resource-api.md) §Service, Endpoints and NetworkPolicy), so a name has no namespace label.
- **Comparison** of a query with a name is case-insensitive.
- **Short names do not resolve.** mvmd's Guest Agent writes `nameserver` lines to `/etc/resolv.conf` and no `search` line, so a MicroVM resolves the full name only. A caller names `<service>.svc.<zone>`.
- **A name under `<zone>` that is not a Service,** or a per-host name below, is `NXDOMAIN`.

### Answers

| Service | Query | Answer |
|---|---|---|
| `ClusterIP` or `LoadBalancer`, `clusterIP` IPv4 | `A` | the `clusterIP` |
| `ClusterIP` or `LoadBalancer`, `clusterIP` IPv6 | `AAAA` | the `clusterIP` |
| Either of those | `AAAA` for an IPv4 `clusterIP`, `A` for an IPv6 one | no data (`NOERROR`, no records) |
| Headless (`clusterIP: None`) | `A`, `AAAA` | one record for each address of the matching family in `subsets[].addresses` of its Endpoints; `NXDOMAIN` when its Endpoints has none or does not exist |
| Headless | `A`, `AAAA` at `<microvm>.<service>.svc.<zone>` | the address of the Endpoints address whose `targetRef` names that MicroVM, for a MicroVM name that is a DNS label |

- **A `LoadBalancer` Service answers its `clusterIP`,** which every Node's network plugin implements, not its LAN address, which only a caller outside the cluster uses. Such a Service always has a `clusterIP`.
- **A ClusterIP Service with no ready MicroVM** still answers its `clusterIP`, as in Kubernetes.
- **An Endpoints address is ready:** `subsets[].addresses` lists only ready MicroVMs for a Service with a selector ([resource-api.md](./resource-api.md) §Service, Endpoints and NetworkPolicy), and a user's Endpoints is served as written.
- **Not served:** `SRV` records for named ports, `PTR` records, the `dns-version` `TXT` record and `ExternalName`'s `CNAME`. A query for them under `<zone>` is `NOERROR` with no data where the name exists and `NXDOMAIN` where it does not.
- **TTL:** 5 seconds on every answer, and on the start of authority of a negative answer.
- **Transport:** UDP and TCP on port 53.

### Forwarding

- A name under `<zone>` is answered from the Services and never forwarded.
- Any other name is forwarded unchanged to the upstream resolvers the unit is configured with, and the upstream's answer is returned. The unit recurses and caches nothing itself.
- With no upstream configured, a name outside `<zone>` is `REFUSED`.
- The unit's own `/etc/resolv.conf` names the Service address like every MicroVM's, so the upstreams are always given explicitly and never read from there.
- A reverse query is a name outside `<zone>`, and is forwarded.

### The unit and its delivery

- **`dns/`, binary `gaon-dns`,** is a client of the public API: it lists and watches `Service` and `Endpoints` and holds the answers in memory. It authenticates with the ServiceAccount token the Node Agent writes into its MicroVM ([ADR 0029](./adr/0029-a-microvms-serviceaccount-token-is-a-file-at-a-fixed-path-and-the-node-agent-replaces-it-and-the-files-that-reference-a-secret-or-configmap-in-place-while-the-microvm-runs.md)), and reads nothing else.
- **State:** until its first list of both kinds completes it answers `SERVFAIL` for a name under `<zone>`. After that it serves the last state it read while the API server is unreachable, and watches again.
- **It is delivered as a workload,** not as a Node service: a `ServiceAccount`, a `Deployment` of MicroVMs of its image that name it, and a `Service` of type `ClusterIP` that selects them, with a UDP port and a TCP port of 53. The manifest is the operator's, like the one in [deployment.md](./deployment.md) §Backup and restore, and the serving issue provides an example.
- **Its Service requests its `clusterIP`.** An address of the service prefix ([ADR 0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md)) that the operator chooses, which is the resolver address below.
- **Its identity** may read Services and Endpoints and nothing else.

### How the resolver reaches a MicroVM

- **It is mvmd's node configuration, `network.resolvers`.** mvmd sends the addresses with the boot description and the Guest Agent writes them to `/etc/resolv.conf` in order. `CreateMachineRequest` has no field for a resolver, so the Node Agent passes none and a MicroVM cannot choose its own.
- **naru writes it.** naru's configuration sets `network.resolvers` of every Node's mvmd to the address its Service requests, as it sets `network.no_masquerade` ([deployment.md](./deployment.md) §MicroVM network). gaon does not compare the two values, so a Node whose list differs resolves through another address.
- **Reaching it is subject to the MicroVM's network policy** like any address (mvmd `docs/architecture/runtime-api.md`, "DNS is an address like any other"): the policy allows the address on UDP and TCP port 53. The default network plugin's `--egress-prefix` list must therefore include the service prefix ([node-agent.md](./node-agent.md) §Plugins).
- **The address is a Service address,** so it works when the network plugin implements Service addresses on each Node (issue #196). Until then, a MicroVM resolves nothing.

### Departures from Kubernetes' DNS specification

Each is a choice of ADR 0033.

- No `<ns>` label in a name (§2.3.1, §2.4.1): a Service has no namespace.
- No `SRV` (§2.3.2, §2.4.2), no `PTR` (§2.3.3, §2.4.3) and no `dns-version` `TXT` (§2.2): no consumer needs them.
- No `CNAME` for `ExternalName` (§2.5): the type is not offered.
- A per-host name is the MicroVM's name from `targetRef`, not an endpoint `hostname` field, which Endpoints does not have.
- "Ready" is membership of `subsets[].addresses`, with no `tolerate-unready-endpoints`: `publishNotReadyAddresses` is not offered.
- No search domain, no `dnsPolicy` and no `dnsConfig`: the resolvers are a node's, and mvmd writes `nameserver` lines only.

## Rationale

- The unit, the name form, the answers, the resolver's path and the departures: [ADR 0033](./adr/0033-a-dns-server-run-as-a-deployment-answers-a-service-at-service-svc-zone-and-its-service-address-is-the-resolver-mvmd-gives-every-microvm.md), which follows decision D6 of goal #117.
- The Service, its addresses and Endpoints: [ADR 0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md).
- Stable names of a StatefulSet's replicas, which a headless Service names: [ADR 0030](./adr/0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md).

## Open questions

- **Serving:** the unit `dns/` is not written, nor is its image, its example manifest or its entry in [structure.md](./structure.md).
- **Who builds and publishes the image,** which the Node Agent pulls by digest ([node-agent.md](./node-agent.md) §MicroVMs).
- **The identity's rules:** whether `gaon:dns` is a bootstrap role or a role the operator applies ([resource-api.md](./resource-api.md) §RBAC), and the name of its ServiceAccount and Service.
- **The resolver address:** whether the operator chooses it, as stated, or the API server reserves one in the service prefix. A Node whose `network.resolvers` differs is not detected.
- **mvmd, garamsh/mvmd:** whether the Guest Agent can also write a `search` line, which would let short names resolve; whether a change of `network.resolvers` reaches running MicroVMs or only those created after an mvmd restart; and whether mvmd's policy at the tap sees a Service address before or after the network plugin translates it.
- **A second resolver,** for a Service address that a failed network plugin does not translate. mvmd takes a list; gaon names one.
- **IPv6 upstreams and dual-stack Services:** one address per Service and one family of `clusterIP` ([resource-api.md](./resource-api.md) §Open questions); a name answers one family.
- **Namespaces:** the name form once a Service has a namespace; a new ADR decides it.
- **Spread and replica count** of the Deployment, which the scheduler's topology spread decides (goal #117, D16).
