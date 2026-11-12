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

- **One cluster CA, gaon's own.** Its certificate is the API server's client CA, and its certificate and key are files that `gaon-controllers` signs with. An operator or tooling creates them and naru delivers them as files in the control-plane Node's config, with the API server's certificate and key and one client certificate and key for each caller that is not a Node Agent (the scheduler, the controllers and the CLI). What each side's flags take is in [apiserver.md](./apiserver.md) §Running and [node-agent.md](./node-agent.md) §Configuration.
- **A Node Agent gets its certificate by a CertificateSigningRequest.**
  - Its config carries a bootstrap token as a file and a writable directory for the agent's key and certificate: `--bootstrap-token-file` and `--cert-dir`. garamsh/naru#37 wires them as `/run/gaon/bootstrap-token` and `/var/lib/naru/gaon-node-agent`.
  - The API server learns the valid tokens from a file, `--bootstrap-token-file`, and accepts a token only to create a request and read one it created.
  - The signer in `gaon-controllers` approves a request whose CN is a Node name and whose requester is a token or that Node's own certificate, and signs it for `--signing-duration`, 8760h by default.
  - The agent renews at two thirds of the validity, over its own certificate, and swaps the pair without a restart.
  - The flow is in [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) and the resource in [resource-api.md](./resource-api.md) §CertificateSigningRequest.
- **A token can obtain a certificate for any Node name.** A token is revoked by removing it from the file and restarting the API server; certificates already issued stay valid until they expire.
- **Operator-supplied files still work.** A Node Agent given `--tls-cert` and `--tls-key` uses them and does not bootstrap, and the two sets of flags are mutually exclusive. The scheduler, the controllers and the CLI keep operator-issued certificates signed by the same CA.
- **Authorization is a flag of the API server.** `--authorization-mode` is `AlwaysAllow`, the default and today's behaviour, or `RBAC`, which needs the TLS flags ([resource-api.md](./resource-api.md) §Authorization, [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)). A deployment that sets `RBAC` issues the operator-supplied certificates with these subjects:
  - the scheduler: Organization `gaon:schedulers`;
  - the controllers: Organization `gaon:controllers`;
  - an administrator, including the CLI's: Organization `gaon:admins`; with none, no one can bind another role.

  Each CN is a name that is not a Node's name. A Node Agent's certificate has no Organization, and the signer must refuse a request whose subject has one.
- **Renewal of an operator-supplied certificate is the operator's.** Each service reads its files at start, so a replaced file takes effect at the next start of that service.

### Config shape

- **argv plus files.** Every gaon service binary takes flags, and the certificate files are named by the TLS flags. The API server's flags are in [apiserver.md](./apiserver.md) §Running and the Node Agent's in [node-agent.md](./node-agent.md) §Configuration. The scheduler takes `-api`, `-interval` and the TLS flags of `lib/api`, and the controllers take `-api`, `-lease-margin`, `-default-lease-duration`, `-signing-cert`, `-signing-key`, `-signing-duration` and the same TLS flags (`scheduler/cmd/gaon-scheduler/main.go`, `controllers/cmd/gaon-controllers/main.go`).
- **Environment variables are a Node Agent fallback only.** Of the four services, only `gaon-node-agent` reads `GAON_*` variables, and a flag given wins over its variable ([node-agent.md](./node-agent.md) §Configuration). A Node's config sets flags for every service.

### Versioning

- **A deployment pins gaon by commit** until gaon cuts releases; the repository has no tags at the commit above.
- **No skew promise** between the Node Agent and the API server before `v1beta1`. The API is `v1alpha1` ([resource-api.md](./resource-api.md)).

## Rationale

- Services, topology, certificates, config shape and versioning as decided: [ADR 0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md).
- Who starts gaon's services and how gaon relates to naru: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- The authorization mode, the roles and the certificate subjects: [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md).
- One etcd member, and what its loss costs: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md).
- Mutual TLS, one certificate per caller, and issuance outside gaon: [ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md).
- The bootstrap token, the signing request and renewal, which narrow ADR 0005 for Node Agents: [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md).

## Open questions

- **Garbage collection of CertificateSigningRequests.** The contract has no delete path, so requests stay in the Store ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)).
- **Growth beyond one member.** More etcd members, and a control plane that is not one Node, wait for their own ADR; the scheduler and the controllers then need leader election or another way to run once.
- **Skew policy.** Which Node Agent and API server versions work together is left to `v1beta1`.
- **Node API.** How gaon calls naru's node API to apply a config is not decided ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
