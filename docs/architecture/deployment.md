# Deployment

Which gaon services run on which Node, what a Node's config carries for them, how certificates reach them and which commit of gaon is run. How each service behaves is in its own document.

Basis: garamsh/gaon at commit `819ccd4af66fcb805e1602ff6d2caef17aa17ad9`. The Node OS that delivers the config is naru, whose side is naru's to design ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).

## Current decisions

### Where services run

| Where | Runs |
|---|---|
| Every Node | `gaon-node-agent`, beside mvmd ([node-agent.md](./node-agent.md)), and the plugin processes the Node's config names: `gaon-network-default` and `gaon-storage-local` for the built-ins, and any third party's ([node-agent.md](./node-agent.md) §Plugins) |
| Each of three control-plane Nodes | etcd (one member of three), `gaon-apiserver`, `gaon-scheduler`, `gaon-controllers` |

- **Three control-plane Nodes, one instance of each service on each.** `gaon-scheduler` and `gaon-controllers` each run on all three but act only on the Node that holds their coordination Lease, `gaon-scheduler` and `gaon-controllers`; the others poll it and do nothing else. A holder stopped cleanly releases it and another takes it on its next poll; after a crash another takes it between 15s and 19s after the last renewal with the default times. The flags are `--leader-elect` (on by default), `--leader-election-name`, `--leader-election-identity`, `--leader-election-duration` (15s), `--leader-election-renew-deadline` (10s) and `--leader-election-retry-period` (2s); `-leader-elect=false` is for a deployment with one instance. [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md).
- **The Nodes' etcd members form one cluster.** Each `gaon-apiserver` gets all three members' client endpoints, the same list on every Node, in `--etcd-endpoints`; it starts if any one answers. The cluster keeps its quorum with one Node down.
- **Clients reach the API server through one address,** naru's virtual IP (garamsh/naru#59). The CLI's `--server`, the scheduler's and the controllers' `-api` and every Node Agent's `--api-server` name it; no client takes a list of addresses. The scheduler and the controllers each present their own client certificate, the same on every Node.
- **The etcd version follows the API server's.** `gaon-apiserver` is built against etcd 3.6.16 and naru's etcd image takes its version from `apiserver/go.mod`, so a bump of one is a bump of both.
- **etcd is dialled over plain HTTP.** `gaon-apiserver` has no etcd TLS flag (`apiserver/config.go`), and its default endpoint is `http://127.0.0.1:2379`. With three members the client and peer ports are on the Node network, which naru's configuration is to keep closed to everything but the Nodes of the control plane.
- **etcd's data persists across a reboot and an upgrade.** The Store holds every resource, so losing it loses them all ([ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md), its single member superseded by [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)).
- **The CLI is not a Node service.** `gaon` runs wherever an operator uses it ([cli.md](./cli.md)).

### Plugins

- **A plugin is a Node service, installed by naru.** The Node Agent does not start plugins ([ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md)). naru installs a plugin as a system extension of the Node and starts it, which is garamsh/naru#57; until that lands there is no gaon-side installation, and an operator or test starts the binary beside the agent.
- **One socket directory.** The plugins serve `<dir>/<kind>-<name>.sock` in `/run/gaon/plugins`, and the agent names the ones it uses with `--plugin-dir`, `--network-plugin` and `--storage-plugin`. The sockets are mode 0600, so a plugin runs as the user the agent runs as. The order of start does not matter: an agent whose plugin is not yet running leaves MicroVMs `Pending` and serves them once it is.
- **The Node's config sets the plugins' flags.** `gaon-network-default` takes `--plugin-dir`, `--egress-prefix` and `--ingress-prefix`; `gaon-storage-local` takes `--plugin-dir`, `--api-server`, `--node-name`, `--mvmd-socket`, `--resync-period` and the TLS flags, each with a `GAON_` variable ([node-agent.md](./node-agent.md) §Plugins).
- **A plugin acts as the Node.** `gaon-storage-local` presents the Node Agent's certificate: the same `--cert-dir` and `--tls-ca` as the agent, so it reads the `node-agent.pem` the agent keeps and renews, or the same `--tls-cert` and `--tls-key`. The plugin therefore needs read access to the agent's certificate directory and has the Node's authority under the Node authorizer. A certificate subject for a plugin is for garamsh/gaon#122.
- **`make e2e` is unchanged.** `tooling/e2e` starts `gaon-node-agent` with no plugin flags, which names no plugin, so its MicroVMs are created isolated with no disk; its `GAON_SERVICES` does not list the plugin binaries.

### Certificates

- **One cluster CA, gaon's own.** Its certificate is the API server's client CA, and its certificate and key are files that `gaon-controllers` signs with. An operator or tooling creates them and naru delivers them as files in the control-plane Node's config, with the API server's certificate and key and one client certificate and key for each caller that is not a Node Agent (the scheduler, the controllers and the CLI). What each side's flags take is in [apiserver.md](./apiserver.md) §Running and [node-agent.md](./node-agent.md) §Configuration.
- **A Node Agent gets its certificate by a CertificateSigningRequest.**
  - Its config carries a bootstrap token as a file and a writable directory for the agent's key and certificate: `--bootstrap-token-file` and `--cert-dir`. garamsh/naru#37 wires them as `/run/gaon/bootstrap-token` and `/var/lib/naru/gaon-node-agent`.
  - The API server learns the valid tokens from a file, `--bootstrap-token-file`, and accepts a token only to create a request and read one it created.
  - The signer in `gaon-controllers` approves a request whose subject is a CN that is a Node name and the Organization `gaon:nodes`, and whose requester is a token or that Node's own certificate, and signs it for `--signing-duration`, 8760h by default.
  - The agent renews at two thirds of the validity, over its own certificate, and swaps the pair without a restart.
  - The flow is in [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), the subject in [ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md) and the resource in [resource-api.md](./resource-api.md) §CertificateSigningRequest.
- **A token can obtain a certificate for any Node name.** A token is revoked by removing it from the file and restarting the API server; certificates already issued stay valid until they expire.
- **Operator-supplied files still work.** A Node Agent given `--tls-cert` and `--tls-key` uses them and does not bootstrap, and the two sets of flags are mutually exclusive. The scheduler, the controllers and the CLI keep operator-issued certificates signed by the same CA.
- **Authorization is a flag of the API server.** `--authorization-mode` is `AlwaysAllow`, the default and today's behaviour, or `RBAC`, which needs the TLS flags ([resource-api.md](./resource-api.md) §Authorization, [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)). A deployment that sets `RBAC` issues the operator-supplied certificates with these subjects:
  - the scheduler: Organization `gaon:schedulers`;
  - the controllers: Organization `gaon:controllers`;
  - an administrator, including the CLI's: Organization `gaon:admins`; with none, no one can bind another role.

  Each CN is a name that is not a Node's name.
- **A Node Agent's certificate has the subject `CN=<node name>`, `O=gaon:nodes`.** The Organization lets a verifier outside gaon tell a Node from the other identities the CA signs. The signer refuses a request whose Organization is another, missing or repeated, and `gaon:nodes` is bound to no role ([ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md)). A certificate issued before that decision has no Organization until the agent renews it. A Node Agent and a signer from before it cannot be mixed: the signer denies the old agent's request.
- **Renewal of an operator-supplied certificate is the operator's.** Each service reads its files at start, so a replaced file takes effect at the next start of that service.

### Config shape

- **argv plus files.** Every gaon service binary takes flags, and the certificate files are named by the TLS flags. The API server's flags are in [apiserver.md](./apiserver.md) §Running and the Node Agent's in [node-agent.md](./node-agent.md) §Configuration. The scheduler takes `-api`, `-interval` and the TLS flags of `lib/api`, and the controllers take `-api`, `-lease-margin`, `-default-lease-duration`, `-signing-cert`, `-signing-key`, `-signing-duration` and the same TLS flags (`scheduler/cmd/gaon-scheduler/main.go`, `controllers/cmd/gaon-controllers/main.go`).
- **Environment variables are a fallback of the Node's own services only.** Of the services, only `gaon-node-agent` and the two plugin binaries read `GAON_*` variables, and a flag given wins over its variable ([node-agent.md](./node-agent.md) §Configuration and §Plugins). A Node's config sets flags for every service.

### Versioning

- **A deployment pins gaon by commit** until gaon cuts releases; the repository has no tags at the commit above.
- **No skew promise** between the Node Agent and the API server before `v1beta1`. The API is `v1alpha1` ([resource-api.md](./resource-api.md)).

## Rationale

- Services, topology, certificates, config shape and versioning as decided: [ADR 0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md).
- Who starts gaon's services and how gaon relates to naru: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- The authorization mode, the roles and the certificate subjects: [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md).
- Plugins as separate processes of a Node, their socket directory and the identity they use: [ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md).
- etcd as the Store and what its loss costs: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md). Three Nodes, three members, the election and the VIP: [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md).
- Mutual TLS, one certificate per caller, and issuance outside gaon: [ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md).
- The bootstrap token, the signing request and renewal, which narrow ADR 0005 for Node Agents: [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md).

## Open questions

- **Garbage collection of CertificateSigningRequests.** The contract has no delete path, so requests stay in the Store ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)).
- **Growth beyond three members.** More members, or more than one cell, wait for their own ADR.
- **etcd TLS.** The API server has no flag for etcd's client TLS, so the three members' client ports carry plain HTTP between the Nodes.
- **Skew policy.** Which Node Agent and API server versions work together is left to `v1beta1`.
- **Node API.** How gaon calls naru's node API to apply a config is not decided ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
