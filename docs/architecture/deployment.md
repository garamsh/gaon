# Deployment

Which gaon services run on which Node, what a Node's config carries for them, how certificates reach them and which commit of gaon is run. How each service behaves is in its own document.

Basis: garamsh/gaon at commit `819ccd4af66fcb805e1602ff6d2caef17aa17ad9`. The Node OS that delivers the config is naru, whose side is naru's to design ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).

## Current decisions

### Where services run

| Where | Runs |
|---|---|
| Every Node | `gaon-node-agent`, beside mvmd ([node-agent.md](./node-agent.md)), and the plugin processes the Node's config names: `gaon-network-default` and `gaon-storage-local` for the built-ins, and any third party's ([node-agent.md](./node-agent.md) §Plugins) |
| Each of three control-plane Nodes | etcd (one member of three), `gaon-apiserver`, `gaon-scheduler`, `gaon-controllers`, `gaon-workloads`, `gaon-gateway-controller` |

- **Three control-plane Nodes, one instance of each service on each.** `gaon-scheduler` and `gaon-controllers` each run on all three but act only on the Node that holds their coordination Lease, `gaon-scheduler` and `gaon-controllers`; the others poll it and do nothing else. A holder stopped cleanly releases it and another takes it on its next poll; after a crash another takes it between 15s and 19s after the last renewal with the default times. The flags are `--leader-elect` (on by default), `--leader-election-name`, `--leader-election-identity`, `--leader-election-duration` (15s), `--leader-election-renew-deadline` (10s) and `--leader-election-retry-period` (2s); `-leader-elect=false` is for a deployment with one instance. [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md).
- **`gaon-workloads` runs on each control-plane Node under the same election.** It acts only on the Node that holds the Lease `gaon-workloads`, the others poll it; the Lease, its times and its flags are those above, and `-leader-election-name` defaults to `gaon-workloads` ([workloads.md](./workloads.md) §Election).
- **`gaon-gateway-controller` runs on each control-plane Node as a host service, as `gaon-workloads` does.** It is a cluster-side client of the API, so it is not in the Node Agent's image or run as a MicroVM, and it acts only on the Node that holds its Lease `gaon-gateway-controller`; the others poll it. The Lease, its times and its flags are those above, and `-leader-election-name` defaults to `gaon-gateway-controller` ([gateway.md](./gateway.md) §The unit). Its flags and certificate are under §Config shape.
- **The Nodes' etcd members form one cluster.** Each `gaon-apiserver` gets all three members' client endpoints, the same list on every Node, in `--etcd-endpoints`; it starts if any one answers. With one member stopped, a request in flight to it fails within `--etcd-keepalive-time` plus `--etcd-keepalive-timeout` (10s and 5s by default), and the API server then serves through the others ([apiserver.md](./apiserver.md) §Running). The cluster keeps its quorum with one Node down.
- **Clients reach the API server through one address,** naru's virtual IP (garamsh/naru#59). The CLI's `--server`, the scheduler's and the controllers' `-api`, `gaon-workloads`' and `gaon-gateway-controller`'s `-server` and every Node Agent's `--api-server` name it, each in the URL shape §Config shape states; no client takes a list of addresses. The scheduler and the controllers each present their own client certificate, the same on every Node.
- **The etcd version follows the API server's.** `gaon-apiserver` is built against etcd 3.6.16 and naru's etcd image takes its version from `apiserver/go.mod`, so a bump of one is a bump of both.
- **etcd is dialled over TLS with a client certificate.** Each `gaon-apiserver` gets `--etcd-ca-file`, `--etcd-cert-file` and `--etcd-key-file`: the CA that signs etcd's certificate, and the client certificate and key it presents. With them it verifies etcd against the CA and speaks TLS 1.3 or later, so `https://` endpoints work; they are given together or not at all, and without them it dials as before and `http://` endpoints work. naru's configuration is to keep the members' client and peer ports closed to everything but the Nodes of the control plane.
- **Service addresses come from two ranges, given to every API server.** `--service-cluster-ip-range` is the virtual service prefix, which no Node or router carries, and `--load-balancer-pool` is the pool of LAN addresses that naru and the network plugin answer for, each a CIDR. The same two values go to all three API servers, and two ranges that overlap stop an API server at start. Without them an API server answers a Service create that needs an address with 500 ([apiserver.md](./apiserver.md) §Running, [ADR 0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md)).
- **etcd's data persists across a reboot and an upgrade.** The Store holds every resource, so losing it loses them all ([ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md), its single member superseded by [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)).
- **The CLI is not a Node service.** `gaon` runs wherever an operator uses it ([cli.md](./cli.md)).

### Plugins

- **A plugin is a Node service, installed by naru.** The Node Agent does not start plugins ([ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md)). naru installs a plugin as a system extension of the Node and starts it, which is garamsh/naru#57; until that lands there is no gaon-side installation, and an operator or test starts the binary beside the agent.
- **One socket directory.** The plugins serve `<dir>/<kind>-<name>.sock` in `/run/gaon/plugins`, and the agent names the ones it uses with `--plugin-dir`, `--network-plugin` and `--storage-plugin`. The sockets are mode 0600, so a plugin runs as the user the agent runs as. The order of start does not matter: an agent whose plugin is not yet running leaves MicroVMs `Pending` and serves them once it is.
- **The Node's config sets the plugins' flags.** `gaon-network-default` takes `--plugin-dir`, `--lan-interface`, `--lan-pool`, `--egress-prefix` and `--ingress-prefix`; `gaon-storage-local` takes `--plugin-dir`, `--api-server`, `--node-name`, `--mvmd-socket`, `--resync-period` and the TLS flags, each with a `GAON_` variable ([node-agent.md](./node-agent.md) §Plugins).
- **`--lan-interface` and `--lan-pool` of `gaon-network-default`** are the interface the Node answers ARP and NDP on for LAN addresses, and the CIDR of the LAN addresses the plugin owns on it, `--lan-pool` repeatable. They are given for the Node to answer for a LoadBalancer Service's LAN address, and are empty when it answers for none ([node-agent.md](./node-agent.md) §Plugins). `--egress-prefix` must include the service prefix, `--service-cluster-ip-range`, so a MicroVM reaches the DNS Service's address ([ADR 0033](./adr/0033-a-dns-server-run-as-a-deployment-answers-a-service-at-service-default-svc-zone-and-its-service-address-is-the-resolver-mvmd-gives-every-microvm.md)).
- **A plugin acts as the Node.** `gaon-storage-local` presents the Node Agent's certificate: the same `--cert-dir` and `--tls-ca` as the agent, so it reads the `node-agent.pem` the agent keeps and renews, or the same `--tls-cert` and `--tls-key`. The plugin therefore needs read access to the agent's certificate directory and has the Node's authority under the Node authorizer. A certificate subject for a plugin is for garamsh/gaon#122.
- **`make e2e` is unchanged.** `tooling/e2e` starts `gaon-node-agent` with no plugin flags, which names no plugin, so its MicroVMs are created isolated with no disk; its `GAON_SERVICES` does not list the plugin binaries.

### Logs and exec

What a deployer sets for `gaon logs` and `gaon exec` to work; the flow is in [node-agent.md](./node-agent.md) §Serving a MicroVM's log and exec and [apiserver.md](./apiserver.md) §Running. Without any of it a MicroVM's log and exec answer `Unavailable`; no flag is required to start a service.

- **Every Node Agent** is given `--agent-addresses`, the Node's own address literals, comma-separated (the LAN address, which is also written as the Node's `status.addresses`), and `--agent-port` (`GAON_AGENT_ADDRESSES`, `GAON_AGENT_PORT`). The two are given together or not at all, and the agent also needs `--tls-ca` and a certificate to serve with, `--cert-dir` or `--tls-cert` and `--tls-key` ([node-agent.md](./node-agent.md) §Configuration).
- **The port is the deployer's choice.** Open it on each Node to the source addresses of the API servers; gaon sets no firewall rule, so that rule is in the Node's config, which is naru's.
- **Every API server** is given `--agent-client-cert-file` and `--agent-client-key-file`, the pair §Certificates describes, to call an agent, and the Node Agent's certificate carries server auth as §Certificates states.

### MicroVM network

- **A MicroVM keeps its source address inside the cluster.** Each Node's mvmd sets `network.no_masquerade` to the cluster's MicroVM prefixes, all Nodes' together, so a packet from a MicroVM to another Node's prefix leaves with the MicroVM's own address, which a peer's ingress policy names by prefix. Traffic to any other destination is masqueraded as before (mvmd's ADR 0049, garamsh/mvmd#436). naru writes that setting into the Node's mvmd configuration, from the cluster's prefix list, which is garamsh/naru's to design.
- **Routes between the Nodes' prefixes are static.** naru's configuration installs, on each Node, a route to every other Node's MicroVM prefix through that Node's address. gaon installs none and runs no routing protocol. mvmd reads no route table, so a prefix with no route loses its return path without an error.
- **Each Node reports its own prefixes.** The Node Agent writes the `machine_prefixes` of its mvmd's runtime info to the Node's `status.microvmPrefixes` ([node-agent.md](./node-agent.md) §Registration), so the cluster reads which Node owns which prefix from the API. gaon does not compare that list with `network.no_masquerade` or with the routes: whoever writes naru's configuration keeps the three the same.

### Certificates

- **One cluster CA, gaon's own.** Its certificate is the API server's client CA, and its certificate and key are files that `gaon-controllers` signs with. An operator or tooling creates them and naru delivers them as files in the control-plane Node's config, with the API server's certificate and key and one client certificate and key for each caller that is not a Node Agent (the scheduler, the controllers and the CLI). What each side's flags take is in [apiserver.md](./apiserver.md) §Running and [node-agent.md](./node-agent.md) §Configuration.
- **A Node Agent gets its certificate by a CertificateSigningRequest.**
  - Its config carries a bootstrap token as a file and a writable directory for the agent's key and certificate: `--bootstrap-token-file` and `--cert-dir`. garamsh/naru#37 wires them as `/run/gaon/bootstrap-token` and `/var/lib/naru/gaon-node-agent`.
  - The API server learns the valid tokens from a file, `--bootstrap-token-file`, and accepts a token only to create a request and read one it created.
  - The signer in `gaon-controllers` approves a request whose subject is a CN that is a Node name and the Organization `gaon:nodes`, and whose requester is a token or that Node's own certificate, and signs it for `--signing-duration`, 8760h by default.
  - The agent renews at two thirds of the validity, over its own certificate, and swaps the pair without a restart.
  - The flow is in [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), the subject in [ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md) and the resource in [resource-api.md](./resource-api.md) §CertificateSigningRequest.
- **A token can obtain a certificate for any Node name.** A token is revoked by removing it from the file and restarting the API server; certificates already issued stay valid until they expire.
- **The token signing key is one key on every control-plane Node.** The API server signs service account tokens ([ADR 0020](./adr/0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md)) with the ECDSA P-256 private key in `--service-account-signing-key-file` and names `--service-account-issuer` as their `iss`. The issuer is an `https` URL, the stable address of the control plane on its virtual IP, with no path, and it is the same on all three API servers. There are three API servers, so the same key file and issuer are delivered to each; a token signed by one is verified by the key of any. Each serves the discovery document and the key set at that address, so a verifier takes the keys from them with the issuer URL and the CA that signed the API server's certificate. The key is rotated by delivering the old public key to every Node as a PEM file named in `--service-account-verify-key-files`, replacing the signing key file with the new private key, and restarting each API server. A token signed by the old key stays valid, and the old key stays in the key set, until the flag drops the file and the servers restart again. `--api-audiences` names the audiences the API server accepts in a token, and is the issuer where it is not given; a token requested for another audience, such as a vault's, is not accepted by it.
- **Operator-supplied files still work.** A Node Agent given `--tls-cert` and `--tls-key` uses them and does not bootstrap, and the two sets of flags are mutually exclusive. The scheduler, the controllers and the CLI keep operator-issued certificates signed by the same CA.
- **Authorization is a flag of the API server.** `--authorization-mode` is `AlwaysAllow`, the default and today's behaviour, or `RBAC`, which needs the TLS flags ([resource-api.md](./resource-api.md) §Authorization, [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)). A deployment that sets `RBAC` issues the operator-supplied certificates with these subjects:
  - the scheduler: Organization `gaon:schedulers`;
  - the controllers: Organization `gaon:controllers`;
  - an administrator, including the CLI's: Organization `gaon:admins`; with none, no one can bind another role.
  - the API server's client certificate to the Node Agents ([ADR 0043](./adr/0043-a-microvms-log-and-exec-are-subresources-authorized-apart-from-get-and-the-api-server-proxies-them-to-its-nodes-agent-over-mutual-tls.md)): Organization `gaon:apiservers`, a certificate other than its serving one, signed by the same CA and bound to no role. Each of the three API servers is given one in `--agent-client-cert-file` and `--agent-client-key-file`, in every mode, `AlwaysAllow` included; without it the server answers a MicroVM's log and exec `Unavailable`.

  Each CN is a name that is not a Node's name.
- **A Node Agent's certificate has the subject `CN=<node name>`, `O=gaon:nodes`.** The Organization lets a verifier outside gaon tell a Node from the other identities the CA signs. The signer refuses a request whose Organization is another, missing or repeated, and `gaon:nodes` is bound to no role ([ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md)). A certificate issued before that decision has no Organization until the agent renews it. A Node Agent and a signer from before it cannot be mixed: the signer denies the old agent's request.
- **A Node Agent serves a MicroVM's log and exec with its Node certificate** ([ADR 0043](./adr/0043-a-microvms-log-and-exec-are-subresources-authorized-apart-from-get-and-the-api-server-proxies-them-to-its-nodes-agent-over-mutual-tls.md)), so an operator-supplied one (`--tls-cert`, `--tls-key`) must carry the extended key usage server auth as well as client auth: an agent given one without server auth logs why, does not serve and writes no `agentPort`, so its Node's logs and exec are `Unavailable`. A bootstrapped one asks for both in every request, so a certificate issued after this change has both, and one issued before it gains server auth when the agent renews.
- **Renewal of an operator-supplied certificate is the operator's.** Each service reads its files at start, so a replaced file takes effect at the next start of that service.

### Config shape

- **argv plus files.** Every gaon service binary takes flags, and the certificate files are named by the TLS flags. The API server's flags are in [apiserver.md](./apiserver.md) §Running and the Node Agent's in [node-agent.md](./node-agent.md) §Configuration. The scheduler takes `-api`, `-interval` and the TLS flags of `lib/api`, and the controllers take `-api`, `-lease-margin`, `-default-lease-duration`, `-signing-cert`, `-signing-key`, `-signing-duration` and the same TLS flags (`scheduler/cmd/gaon-scheduler/main.go`, `controllers/cmd/gaon-controllers/main.go`).
- **The workloads unit is configured by its flags and one manifest.** It takes `-server`, the base URL of the API server (the same virtual IP), `-interval`, the TLS flags of `lib/api` and the leader-election flags (`workloads/cmd/gaon-workloads/main.go`). An administrator applies `workloads/deploy/rbac.yaml` once for the cluster with `gaon apply -f`; until then the unit is refused ([workloads.md](./workloads.md) §Identity and role). Its certificate has the common name `gaon-workloads`, the same on every Node, and under `RBAC` that manifest's binding names it; it takes no Organization.
- **The gateway unit is configured by its flags and one manifest.** `gaon-gateway-controller` requires `-server`, `-proxy-image` and `-proxy-ca-configmap`. `-server` is the base URL of the API server (the same virtual IP); `-proxy-image` is the proxy image by digest, `<registry>/gaon/gateway-proxy@<digest>`; `-proxy-ca-configmap` is the ConfigMap whose key `ca.crt` the proxies trust. `-proxy-server` (default `-server`) and `-proxy-service-account` (default `gaon-gateway-proxy`) are optional, and it takes `-interval`, the TLS flags of `lib/api` and the leader-election flags (`gateway/cmd/gaon-gateway-controller/main.go`). An administrator applies `gateway/deploy/rbac.yaml` once for the cluster with `gaon apply -f` ([gateway.md](./gateway.md) §Identity and role). Its certificate has the common name `gaon-gateway-controller`, the same on every Node, and under `RBAC` that manifest's binding names it; it takes no Organization.
- **The URL a binary takes differs.** `gaon-node-agent` and `gaon-storage-local` take `--api-server`, the scheduler and the controllers take `-api`, and the CLI takes `--server` ([cli.md](./cli.md)), each the API server's address with the path `/apis/compute.gaon.garam.sh/v1alpha1`. `gaon-workloads` and `gaon-gateway-controller` take `-server`, the bare base URL with no path.
- **The Node Agent's `--service-account-audience` is set on every Node.** It is the audience a MicroVM's ServiceAccount token is requested with: one of the API servers' `--api-audiences`, which default to the issuer `--service-account-issuer`. While it is empty, a MicroVM that names a ServiceAccount is not started, and the DNS and gateway-proxy workloads each name one ([node-agent.md](./node-agent.md) §Configuration).
- **Environment variables are a fallback of the Node's own services only.** Of the services, only `gaon-node-agent` and the two plugin binaries read `GAON_*` variables, and a flag given wins over its variable ([node-agent.md](./node-agent.md) §Configuration and §Plugins). A Node's config sets flags for every service.

### Versioning

- **A deployment pins gaon by commit** until gaon cuts releases; the repository has no tags at the commit above.
- **No skew promise** between the Node Agent and the API server before `v1beta1`. The API is `v1alpha1` ([resource-api.md](./resource-api.md)).

### Backup and restore

`gaon-etcd-snapshot` ([etcd-snapshot.md](./etcd-snapshot.md)) is run by a CronJob and uploads a snapshot of the Store to S3. The manifest below is applied with `gaon apply -f` once the Secrets exist. The values in angle brackets are the operator's.

```yaml
apiVersion: workloads.gaon.garam.sh/v1alpha1
kind: CronJob
metadata:
  name: etcd-snapshot
spec:
  schedule: "17 */6 * * *"
  historyLimit: 3
  jobTemplate:
    completions: 1
    backoffLimit: 2
    template:
      image: <registry>/gaon/etcd-snapshot@<digest>
      args:
        - -etcd-endpoints=https://<node 1 address>:2379,https://<node 2 address>:2379,https://<node 3 address>:2379
        - -etcd-ca-file=/etc/gaon/etcd/ca.crt
        - -etcd-cert-file=/etc/gaon/etcd/tls.crt
        - -etcd-key-file=/etc/gaon/etcd/tls.key
        - -s3-bucket=<bucket>
        - -s3-prefix=etcd/
        - -region=<region>
        - -rolesanywhere-cert-file=/etc/gaon/aws/tls.crt
        - -rolesanywhere-key-file=/etc/gaon/aws/tls.key
        - -rolesanywhere-trust-anchor-arn=<trust anchor ARN>
        - -rolesanywhere-profile-arn=<profile ARN>
        - -rolesanywhere-role-arn=<role ARN>
      files:
        - {path: /etc/gaon/etcd/ca.crt, mode: 0400, valueFrom: {secretKeyRef: {name: etcd-snapshot-etcd-client, key: ca.crt}}}
        - {path: /etc/gaon/etcd/tls.crt, mode: 0400, valueFrom: {secretKeyRef: {name: etcd-snapshot-etcd-client, key: tls.crt}}}
        - {path: /etc/gaon/etcd/tls.key, mode: 0400, valueFrom: {secretKeyRef: {name: etcd-snapshot-etcd-client, key: tls.key}}}
        - {path: /etc/gaon/aws/tls.crt, mode: 0400, valueFrom: {secretKeyRef: {name: etcd-snapshot-aws, key: tls.crt}}}
        - {path: /etc/gaon/aws/tls.key, mode: 0400, valueFrom: {secretKeyRef: {name: etcd-snapshot-aws, key: tls.key}}}
      nodeSelector:
        gaon.garam.sh/control-plane: "true"
      resources:
        requests: {cpu: 250m, memory: 256Mi}
        limits: {memory: 512Mi}
```

- **The Secrets** are two, of the kind `Secret` in `compute.gaon.garam.sh/v1alpha1`. `etcd-snapshot-etcd-client` holds `ca.crt`, the CA the etcd members' server certificates are signed by, and `tls.crt` and `tls.key`, a client certificate the members' client CA signs. `etcd-snapshot-aws` holds `tls.crt` and `tls.key`, a certificate of the common name `gaon-etcd-snapshot` signed by gaon's CA, with any intermediate after it. The operator creates both from files produced outside gaon; no AWS access key is stored.
- **What AWS holds** is the operator's: a Roles Anywhere trust anchor on gaon's CA, a profile and a role whose trust policy names the common name and whose permission is `s3:PutObject` on the bucket's prefix, and the bucket with a lifecycle rule for retention.
- **`nodeSelector` binds the MicroVM to a Node labelled `gaon.garam.sh/control-plane: "true"`.** The scheduler keeps only Nodes whose labels hold it, and the operator labels each control-plane Node ([scheduler.md](./scheduler.md)); a MicroVM no Node matches stays unbound.
- **The network path.** A MicroVM's traffic to a destination outside the cluster is masqueraded, so etcd sees the address of the Node the MicroVM runs on, and the etcd client port is open only to control-plane Nodes. A run therefore works when the MicroVM is bound to a control-plane Node and fails to connect when it is not, and the Job's retries do not change the Node. Which addresses and ports naru opens is naru's ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
- **S3 is reached over the Node's route to the internet,** by the same masquerade; a Node with no such route needs one from naru.
- **The snapshot holds every Secret's data in clear,** so the bucket's access is as restricted as the Secrets are.

#### Restore

A restore replaces the Store of all three members from one snapshot. It loses every write after the snapshot's revision, and it is done with the control plane stopped. Each step says what it needs from naru.

1. **Choose the object** by its key, `<prefix><time>-rev<revision>.db`. Download it from S3 and compare its SHA-256 with the object's checksum. Needs from naru: a Node with the S3 route and the `etcdutl` of the etcd version in use.
2. **Check it** with `etcdutl snapshot status <file>`, which verifies the integrity hash and prints the revision. Needs: nothing more.
3. **Stop everything that writes to etcd:** the API servers, and with them the scheduler, the controllers, `gaon-workloads` and the Node Agents, on all three Nodes; then stop the three etcd members. Needs: a way to stop and keep stopped each service on each Node.
4. **Copy the file to each of the three Nodes.** Needs: file delivery to a Node.
5. **On each Node run `etcdutl snapshot restore <file>`** with a new `--data-dir`, that Node's own `--name` and `--initial-advertise-peer-urls`, the same `--initial-cluster` on all three (the three names and peer URLs) and the same new `--initial-cluster-token`, different from the old one. Add `--bump-revision <n> --mark-compacted`, with `n` larger than the number of writes the cluster made since the snapshot: a resourceVersion is an etcd revision, so a client that holds one from after the snapshot would otherwise hold a version above the store's. Needs: the Node's member name, peer URL and the cluster string, which are the ones in its etcd config, and the new data directory.
6. **Point each etcd member at its restored data directory and the new token, and start all three.** Needs: a config change to the members' data directory and token, delivered by naru ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)), and the members' certificates unchanged.
7. **Check health** with `etcdctl endpoint health --cluster` against the three, and see one leader.
8. **Start the API servers, then the scheduler, the controllers, `gaon-workloads` and the Node Agents.** Needs: the same as step 3, in reverse.
9. **Check** that `gaon get` of Nodes answers, and that the Nodes' Leases renew.

A step that fails is not retried over a half-restored member: the old data directories are kept until step 9 passes, and a failed attempt starts again from step 5 with a new data directory.

## Rationale

- Services, topology, certificates, config shape and versioning as decided: [ADR 0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md).
- Who starts gaon's services and how gaon relates to naru: [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md).
- The authorization mode, the roles and the certificate subjects: [ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md).
- Plugins as separate processes of a Node, their socket directory and the identity they use: [ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md).
- etcd as the Store and what its loss costs: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md). Three Nodes, three members, the election and the VIP: [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md).
- The snapshot CronJob, its credentials and the restore: [ADR 0028](./adr/0028-a-cronjob-runs-gaon-etcd-snapshot-which-uploads-a-checked-etcd-snapshot-to-s3-under-iam-roles-anywhere-credentials.md).
- Mutual TLS, one certificate per caller, and issuance outside gaon: [ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md).
- The bootstrap token, the signing request and renewal, which narrow ADR 0005 for Node Agents: [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md).

## Open questions

- **Garbage collection of CertificateSigningRequests.** The contract has no delete path, so requests stay in the Store ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)).
- **Growth beyond three members.** More members, or more than one cell, wait for their own ADR.
- **Skew policy.** Which Node Agent and API server versions work together is left to `v1beta1`.
- **Node API.** How gaon calls naru's node API to apply a config is not decided ([ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)).
