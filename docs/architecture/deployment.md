# Deployment

Which gaon services run on which Node, what a Node's config carries for them, how certificates reach them and which commit of gaon is run. How each service behaves is in its own document.

Basis: garamsh/gaon at commit `819ccd4af66fcb805e1602ff6d2caef17aa17ad9`. The Node OS that delivers the config is naru, whose side is naru's to design ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).

## Current decisions

### Where services run

| Where | Runs |
|---|---|
| Every Node | `gaon-node-agent`, beside mvmd ([node-agent.md](./node-agent.md)) |
| One control-plane Node | etcd, `gaon-apiserver`, `gaon-scheduler`, `gaon-controllers` |

- **One instance of each control-plane service.** The scheduler and the controllers have no leader election: no Go file outside a test mentions one, and `gaon-scheduler` and `gaon-controllers` each start their loop unconditionally (`scheduler/cmd/gaon-scheduler/main.go`, `controllers/cmd/gaon-controllers/main.go`). A second instance of either is not supported.
- **etcd is on the API server's loopback.** `gaon-apiserver` dials etcd over plain HTTP and has no etcd TLS flag (`apiserver/config.go`), its default endpoint being `http://127.0.0.1:2379`.
- **etcd's data persists across a reboot and an upgrade.** The Store holds every resource, so losing it loses them all ([ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md) for the single member).
- **The CLI is not a Node service.** `gaon` runs wherever an operator uses it ([cli.md](./cli.md)).

### Certificates

- **M1: an operator or tooling issues the files** (a CA, the API server's certificate and key, and one client certificate and key per caller), and naru delivers them as files in the Node's config. A Node Agent's certificate has its Node name as CN. What each side's flags take is in [apiserver.md](./apiserver.md) §Running and [node-agent.md](./node-agent.md) §Configuration.
- **Renewal is the operator's.** Each service reads its files at start, so a replaced file takes effect at the next start of that service.
- **End state: gaon's own CA with a bootstrap and CSR flow.** Its design is not decided; see open questions.

### Config shape

- **argv plus files.** Every gaon service binary takes flags, and the certificate files are named by the TLS flags. The API server's flags are in [apiserver.md](./apiserver.md) §Running and the Node Agent's in [node-agent.md](./node-agent.md) §Configuration. The scheduler takes `-api`, `-interval` and the TLS flags of `lib/api`, and the controllers take `-api`, two lease durations and the same TLS flags (`scheduler/cmd/gaon-scheduler/main.go`, `controllers/cmd/gaon-controllers/main.go`).
- **Environment variables are a Node Agent fallback only.** Of the four services, only `gaon-node-agent` reads `GAON_*` variables, and a flag given wins over its variable ([node-agent.md](./node-agent.md) §Configuration). A Node's config sets flags for every service.

### Versioning

- **A deployment pins gaon by commit** until gaon cuts releases; the repository has no tags at the commit above.
- **No skew promise** between the Node Agent and the API server before `v1beta1`. The API is `v1alpha1` ([resource-api.md](./resource-api.md)).

## Rationale

- Services, topology, certificates, config shape and versioning as decided: [ADR 0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md).
- Who starts gaon's services and how gaon relates to naru: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- One etcd member, and what its loss costs: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- Mutual TLS, one certificate per caller, and issuance outside gaon: [ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md).

## Open questions

- **The CSR flow.** How a Node obtains its first certificate from gaon's CA, who approves it, and how it fits a node OS that delivers certificates as files.
- **Growth beyond one member.** More etcd members, and a control plane that is not one Node, wait for their own ADR; the scheduler and the controllers then need leader election or another way to run once.
- **Skew policy.** Which Node Agent and API server versions work together is left to `v1beta1`.
- **Node API.** How gaon calls naru's node API to apply a config is not decided ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
