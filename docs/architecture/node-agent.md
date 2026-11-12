# Node Agent

What `gaon-node-agent` does on a node: it registers the node, keeps its Lease and mvmd's fencing deadline renewed, runs the MicroVMs bound to it on mvmd, and reports their status.

Basis: [resource-api.md](./resource-api.md) for the contract it keeps, [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md) for the boundary. The mvmd statements below hold for garamsh/mvmd at commit `3b37892f303a4d4dbd8e5360906d0f9c036b562e`, runtime API `v1alpha2`, which the agent reaches only through mvmd's `lib/runtimeapi` module over mvmd's Unix socket. The MicroVM prefixes statement holds at `d189133bba8d842d2bcb20046f39fe03161efeb1`, the same API version. The network policy and disk statements hold at `3f6e60567966819ba920709a1fe84e9a3848a523`, the same API version ([ADR 0012](./adr/0012-the-node-agent-asks-a-network-plugin-and-a-storage-plugin-compiled-into-it-before-it-creates-a-machine.md), whose compiled-in decision [ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md) supersedes).

## Current decisions

### Configuration

Every value is a flag with an environment variable as its fallback. A flag given wins over the variable, and the variable over the default. The agent refuses to start when a required value is missing or the durations do not fit each other, defaults included: the defaults satisfy both inequalities below.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--api-server` | `GAON_API_SERVER` | none, required | root URL of the resource API: the API server's address with `/apis/compute.gaon.garam.sh/v1alpha1`, which [resource-api.md](./resource-api.md) §Paths are relative to |
| `--tls-ca` | `GAON_TLS_CA` | none | file of the CA certificates the API server's certificate must chain to |
| `--tls-cert` | `GAON_TLS_CERT` | none | file of the client certificate the agent presents to the API server |
| `--tls-key` | `GAON_TLS_KEY` | none | file of the key of the client certificate |
| `--bootstrap-token-file` | `GAON_BOOTSTRAP_TOKEN_FILE` | none | file holding one bootstrap token, `<id>.<secret>`, with which the agent asks for its client certificate ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md)); needs `--cert-dir` and `--tls-ca` |
| `--cert-dir` | `GAON_CERT_DIR` | none | directory where the agent keeps its issued certificate and key in one file, `node-agent.pem`; needs `--tls-ca` |
| `--node-name` | `GAON_NODE_NAME` | none, required | name of this node's Node and Lease |
| `--mvmd-socket` | `GAON_MVMD_SOCKET` | none, required | path of mvmd's runtime API socket |
| `--delta` | `GAON_DELTA` | 1s, provisional | δ, how long a MicroVM can run past mvmd's recorded deadline expiry |
| `--lease-duration` | `GAON_LEASE_DURATION` | 30s | validity of the Lease after a renewal, a whole number of seconds |
| `--renew-period` | `GAON_RENEW_PERIOD` | 5s | time between renewal rounds |
| `--deadline-length` | `GAON_DEADLINE_LENGTH` | 15s | length each deadline renewal asks mvmd for |
| `--rpc-timeout` | `GAON_RPC_TIMEOUT` | 2s | bound on each call of a renewal round |
| `--resync-period` | `GAON_RESYNC_PERIOD` | 10s | time between two reconciliations of the MicroVMs and two reports of the Node's status |
| `--restart-backoff` | `GAON_RESTART_BACKOFF` | 10s | wait before the first restart of a MicroVM, doubled for each restart after it |
| `--restart-backoff-max` | `GAON_RESTART_BACKOFF_MAX` | 5m | cap on the wait between two restarts, not below the backoff |
| `--restart-reset-after` | `GAON_RESTART_RESET_AFTER` | 10m | how long a MicroVM must have run for the wait to start over at the backoff |
| `--plugin-dir` | `GAON_PLUGIN_DIR` | `/run/gaon/plugins` | the directory the plugins serve their sockets in |
| `--network-plugin` | `GAON_NETWORK_PLUGIN` | none | the name of the network plugin, the process serving `<plugin-dir>/network-<name>.sock`; empty names no plugin; a name that cannot be in a socket path refuses the start |
| `--storage-plugin` | `GAON_STORAGE_PLUGIN` | none | the name of the storage plugin, the process serving `<plugin-dir>/storage-<name>.sock`; empty names no plugin; a name that cannot be in a socket path refuses the start |

The three TLS values are given together or not at all. All three give a mutual TLS 1.3 client built by `lib/api` that trusts only the CA file; none gives plain HTTP, for a loopback API server; one or two refuse the start, naming the ones missing, as does a file that cannot be read or does not fit. The agent builds its client once at start, so a replaced file is read at the next start.

[ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) decides how the agent gets its certificate without operator-supplied files. The agent keeps its own pair when `--cert-dir` is given, with or without `--bootstrap-token-file`, and does this:

- **Flags.** `--bootstrap-token-file` and `--cert-dir` exclude `--tls-cert` and `--tls-key`: the agent takes its certificate from one set or the other, and giving both stops the start naming both sets. `--bootstrap-token-file` without `--cert-dir`, and either without `--tls-ca`, stop the start. The token file holds one token matching `[a-z0-9]{6}\.[a-z0-9]{16}` once surrounding whitespace is trimmed; any other content stops the start, and no message, log line or error carries the token. `--cert-dir` alone is accepted and, with no valid pair in the directory, stops the start.
- **Files.** The agent creates `--cert-dir` with mode 0700 if it is missing. It keeps the pair in one file, `node-agent.pem`, mode 0600, holding the certificate block followed by the key block. The file is written to a temporary file in the directory and renamed into place with one rename, so the directory holds the old pair or the new one whole and a file is never rewritten in place.
- **Valid pair.** The file parses, the key matches the certificate, the certificate's common name is `--node-name`, and its `NotAfter` is in the future. At start the agent uses a valid pair, without a token, and renews it as below. Without a valid pair it bootstraps with the token, and without a token the start stops.
- **A request.** The bootstrap and every renewal generate a new ECDSA P-256 key and a `CertificateSigningRequest` named `<node-name>-<16 random hex digits>` whose request carries the node name as common name, the Organization `gaon:nodes` and no other subject attribute, and no subject alternative name ([ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md)). The agent reads the request every second until it is Approved with a certificate or Denied. The bootstrap calls carry the token and no client certificate; renewal calls present the current certificate and no token.
- **Outcomes.** A Denied request at bootstrap stops the start with the reason, and so does any other error of the bootstrap. A renewal that is Denied or fails is logged, the pair in use stays in the directory and in memory, and the agent asks again after one minute. The pair is replaced only by a certificate received.
- **Renewal.** It is due at `NotBefore` plus two thirds of the validity, and at once when that time has passed at start. The new pair replaces the old one in the TLS client's `GetClientCertificate` callback under a lock, so no restart is needed. A pair issued before ADR 0022 has no Organization: it is valid, and the renewal due for it replaces it with one that has the Organization. A connection already open keeps the pair it was made with; each connection made after the swap presents the new one.

The files given as `--tls-cert` and `--tls-key` are still read once at start and are not renewed.


δ's default of 1s is provisional: it is not measured, in mvmd either, and it is far above a real-time thread's wake-up and a SIGKILL (mvmd ADR 0003). The durations must satisfy two inequalities, checked at start: the deadline length exceeds the renew period plus two call timeouts, so the deadline never lapses between rounds, and the Lease duration is at least the deadline length plus δ plus two call timeouts, so a round can always renew the deadline.

### Registration

- The agent creates its Node with an empty `spec`, by a `PUT` without a version, when the API has none, and never writes `spec` again.
- The Node comes first. Until the API has it, the agent retries the create every resync period and makes no other request that needs it: not the MicroVM list or watch, not a Lease write, not a Node status write. A restarted agent whose Node exists reads it and starts the rest at once. The certificate renewal does not need the Node and runs throughout. Under `RBAC` the Node authorizer allows a Node's MicroVM reads only once a Node of its name exists, and a Lease named as no Node is `Invalid` ([resource-api.md](./resource-api.md) §Authorization), so a request made earlier is refused.
- It writes the Node's `status` by compare-and-swap on every resync: `state` is `Admitted` when a user has set `spec.admitted` and `Discovered` otherwise, `capacity` comes from mvmd's runtime info (cpus as a whole number, memory and storage in bytes, storage being mvmd's state total), `microvmPrefixes` is the runtime info's `machine_prefixes`, and the `Ready` condition is true while mvmd answers. A Node whose mvmd reports no prefix has none. A reported prefix that is not a CIDR is logged at warn level with the prefix named, and is not written; the others are. When mvmd's runtime info fails, the agent writes `Ready` false with reason `RuntimeUnreachable` and the error as the message, keeps the stored `capacity` and `microvmPrefixes`, and keeps the condition's transition time while its status and reason are unchanged.
- It creates the Lease named as the Node on its first round and renews it by compare-and-swap, writing the node as holder.

### Finalizer

- The agent's finalizer is `compute.gaon.garam.sh/node-agent`.
- **Added** to `metadata.finalizers` before the agent first creates or starts a MicroVM bound to its Node, so a MicroVM that is not yet finalized is never run.
- **Removed** once a MicroVM bound to its Node with `deletionTimestamp` set is released on mvmd. Released means mvmd holds no machine the agent named after it, under any epoch, by the round's listing of mvmd less what the round removed. A failed stop or delete keeps the finalizer, and the next resync tries again. A MicroVM bound to another Node keeps the finalizer, since that Node's agent releases it.
- Both are `PUT /microvms/{name}` by compare-and-swap on the version read, changing only the agent's own entry of `metadata.finalizers` and never another's. A conflict is read again and retried, up to three times a round. The API server removes the MicroVM on the write that clears its last finalizer.

### The fencing obligation

- **One loop** renews the Lease first and then, in the same round, mvmd's deadline.
- **Validity of the Lease** is counted from the start of the latest acknowledged renewal call on the agent's monotonic clock, as [resource-api.md](./resource-api.md) states.
- **A deadline renewal is sent only if, were mvmd to receive it as late as the call timeout allows, its expiry plus δ is within that validity.** That is the clock reading now, plus the call timeout, the deadline length and δ, no later than the validity. This bounds a renewal whose acknowledgement never arrives, which resource-api.md leaves to this document: mvmd may have recorded it, and it was recorded no later than the call timeout after it was sent.
- **When the Lease cannot be renewed**, validity stops growing, the check refuses every further deadline renewal once the margin is gone, and mvmd's deadline lapses on its own. The MicroVMs are fenced by mvmd, not by the agent's survival. A failed Lease call makes the agent read the Lease again before the next write.
- **Every call of a round is bounded** by the call timeout, including the read of mvmd's last applied number, so a runtime that does not answer ends the round with an error and the next round renews the Lease.
- **The renewal number** sent to mvmd increases monotonically; the agent reads mvmd's last applied number once at start, so a restarted agent continues the sequence.
- mvmd refuses to start or restore a MicroVM while its deadline is not armed or has expired; the agent treats that refusal as a failure to retry on the next resync, and the MicroVM stays `Pending`.

### Plugins

[ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md) makes the network and storage plugins processes of their own, which supersedes the compiled-in plugins of [ADR 0012](./adr/0012-the-node-agent-asks-a-network-plugin-and-a-storage-plugin-compiled-into-it-before-it-creates-a-machine.md). The agent reaches each by gRPC over a Unix socket, through the client in `lib/plugin`; nothing plugin-specific is compiled into `node-agent/`. The API, `gaon.plugin.v1alpha1`, is documented in `lib/plugin/README.md`.

- **Discovery:** `--network-plugin <name>` and `--storage-plugin <name>` name the plugin whose socket is `<plugin-dir>/<kind>-<name>.sock`, `kind` being `network` or `storage`. A socket path over 107 bytes, the 108 of a Linux Unix socket path less its terminating NUL, is refused with an error naming that limit: by the agent and by a plugin at its start, not left to fail at bind. There is no scan and no registry. The agent dials lazily, so it starts with a plugin not running. With no network plugin named a MicroVM is created with the empty policy, which mvmd reads as blocking all outside traffic; with no storage plugin named no disk is attached and a MicroVM that names a Volume fails with `NoStoragePlugin`.
- **Order:** for a MicroVM to create, the agent asks the network plugin for the policy, pulls the image, asks the storage plugin to attach the volumes, and creates the MicroVM in mvmd with the policy and disks in the one request, then starts it. No MicroVM is started without its policy, which keeps invariant 6.
- **Refusal:** a plugin refuses a MicroVM with a reason and a message, carried in the response and not as a gRPC error. The MicroVM is `Failed`, its `Ready` condition false with that reason and message, and mvmd is not asked to create it. Any other plugin error is retried at the next resync.
- **A plugin that is not running:** a call that gets gRPC `Unavailable`, or `Unimplemented` from a plugin that does not serve this API version, or that is not answered in 10 seconds, is not a refusal. The MicroVM is not created and the call is retried at every resync, so a plugin started later is served by the next one. While it fails the MicroVM is `Pending` with `Ready` false, the reason `NetworkPluginUnavailable` or `StoragePluginUnavailable` and a message naming the error.
- **Release:** the storage plugin's `Release` is called with the mvmd identifier after the agent deletes a microvm, and after mvmd rejects a create. It is repeatable and may be called for an identifier `Attach` never saw.
- **Identity:** a plugin that uses the resource API acts as the Node it serves, presenting the Node Agent's own client certificate (ADR 0019 Decision 6). A narrower plugin identity is for garamsh/gaon#138.
- **Installation:** a plugin is a Node service installed and started by naru, in the way deployment.md §Plugins describes. The agent does not start plugins.
- **`default` network** (`network-default/`, binary `gaon-network-default`, `--network-plugin default`): a MicroVM naming no network or only `default` is served; any other network is refused with `UnknownNetwork`. Egress is one any-protocol rule for each `--egress-prefix` (`GAON_EGRESS_PREFIX`); ingress is a rule for each port of `spec.ports` and each `--ingress-prefix`, with the protocol the port declares (a `UDP` port is allowed as UDP, a `TCP` port as TCP) (`GAON_INGRESS_PREFIX`). Both are repeatable on the plugin's command line, and its variable is comma-separated and replaced by a flag. With no prefixes the policy is empty, which mvmd reads as blocking all outside traffic. A prefix that is not an IPv4 or IPv6 CIDR refuses the plugin's start, and a port outside 1 to 65535 or a protocol other than `TCP` or `UDP` fails the MicroVM with `InvalidSpec`.
- **`local` storage** (`storage-local/`, binary `gaon-storage-local`, `--storage-plugin local`): serves the Volumes of class `local` pinned to this Node ([ADR 0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md) Decisions 2, 4 and 5) as mvmd disks of `spec.storage` bytes, rounded up, named `volume-<Volume name>`, which mvmd formats ext4. It is given `--api-server`, `--node-name`, `--mvmd-socket`, `--resync-period` and the TLS settings, each with a `GAON_` variable, and reads the Node's certificate as the agent does: `--cert-dir` with `--tls-ca` reads `node-agent.pem` at each new connection, `--tls-cert` and `--tls-key` are read once, and none gives plain HTTP.
  - **Attach:** for each Volume the MicroVM names, once, it reads the Volume and refuses the MicroVM before writing anything if the Volume is missing (`VolumeNotFound`), is of another class (`UnsupportedStorageClass`), is being deleted or pinned to another Node (`VolumeNotUsable`), or is attached to another MicroVM at the same or a higher epoch (`VolumeInUse`). It then lists mvmd's disks, creates the disk only if none has the name, and writes the Volume's `status` as `Bound` with the attachment `{microvmName, bindingEpoch}` and the disk's size as `capacity`, by compare-and-swap on the version read; a conflict is read again and decided again, up to three times. A disk mvmd refuses to create, with outcome kind `REJECTED`, `PRECONDITION_FAILED` or `FAILED`, writes the Volume `Failed` and refuses the MicroVM with `VolumeFailed`; any other error is retried at the next resync. A repeated call for the same MicroVM and epoch makes no disk, and writes `Bound` again only if the phase is no longer `Bound`.
  - **Release:** does nothing. The disk lives until its Volume is deleted.
  - **Status loop:** every resync the plugin lists the Volumes and mvmd's disks and, for each local Volume pinned to this Node and not being deleted, makes the disk if it is missing and writes `status` only when it differs: `Bound` while a MicroVM holds the disk and the Volume has an attachment, `Released` once none holds it and the attachment remains, which is kept, and `Available` with `capacity` while there has been no attachment; a disk that cannot be made is `Failed`. Between `Attach` and mvmd's create the disk is not yet held, so a round there writes `Released`, and the next `Attach` or round writes `Bound` again. A write that conflicts is left to the next round.
  - **Deletion:** a Volume pinned to this Node with `deletionTimestamp` set and the agent's finalizer `compute.gaon.garam.sh/node-agent` has its disk deleted and the finalizer removed once mvmd reports no MicroVM holding the disk. The removal is `PUT /volumes/{name}` by compare-and-swap on the version read, changing only that entry of `metadata.finalizers`; the API server removes the Volume on the write that clears its last finalizer. A disk still held keeps the disk and the finalizer. A disk already absent is not an error. A Volume without the agent's finalizer, or pinned to another Node, is left alone.

### MicroVMs

- **Watched set:** the agent lists the MicroVMs, watches from the list's version and reconciles on every change and every resync. A failed or expired watch is followed by a new list.
- **Identity in mvmd:** a MicroVM is held in mvmd as its name with the binding epoch appended. A binding at a later epoch is therefore a different mvmd machine, and the earlier one is removed, never reused.
- **Kernel:** the agent passes `spec.kernel` unchanged as the create request's `kernel`, so an empty value leaves mvmd to pick the node's default.
- **Epoch rule:** the agent keeps the newest epoch it has seen for each MicroVM and ignores an event, a list entry or a re-read carrying an older one.
- **Run:** a MicroVM bound to this node, not being deleted and not `OnDemand`, has its image pulled by digest, is created in mvmd and started, according to `runStrategy`: `Halted` is stopped and not created, `Once` is not started again after it stopped, unless the agent stopped it for `Halted` and `runStrategy` is now `Once`, which starts it, the others are started whenever mvmd holds them created, and restarted as below when it holds them stopped or faulted.
- **Remove:** every mvmd machine the agent named that is not a MicroVM bound here now, whether unbound, bound elsewhere, bound at an older epoch, being deleted or no longer in the API, is stopped and deleted. This also clears what an earlier run left. An mvmd machine whose identifier the agent did not form is left alone.
- **Status:** the agent writes `phase`, `bindingEpoch` equal to the spec's, `observedGeneration`, `addresses`, `restartCount` and a `Ready` condition, only when they differ from what is stored. `lastExit` is never written: mvmd reports no exit time (garamsh/mvmd#331).
- **Restart:** under `Always` a MicroVM that stopped, and under `RerunOnFailure` one that failed, is started again after a wait and not before. The wait is the backoff, doubled for each restart and capped at the maximum, and starts over once the MicroVM has run for the reset time. A clean exit under `RerunOnFailure` is not restarted. A faulted MicroVM is stopped first, since mvmd accepts only a stop in that state. A `Halted` MicroVM resumed is started at once and is not a restart. A MicroVM the agent holds stopped for `Halted` is started at once when `runStrategy` becomes `Once`, and its next exit is judged afresh. The agent knows it stopped the MicroVM for `Halted` from its memory, so after an agent restart a `Halted` MicroVM switched to `Once` is not started.
- **Failure:** the agent reads the entrypoint's record from mvmd, exec `entrypoint`, once when it first sees the MicroVM stopped, under `Once` as under the restarting strategies. The exit failed when the record is `EXITED` with a non-zero exit code or a terminating signal, when there is no `EXITED` record, or when the MicroVM is faulted; an `EXITED` record with code 0 and no signal is clean. A read that returns no record before the call timeout is a failure; any other error leaves the exit unjudged until the next resync.
- **`restartCount`:** each restart adds one, written by compare-and-swap with the MicroVM's epoch checked, so a count of an earlier epoch is never carried into a later one. A write that conflicts is read again and retried, and a restart whose write failed is written by the next round, never counted twice.
- **Where the state lives:** the wait, the streak and the judged exit are kept in the agent's memory per mvmd machine, so a rebinding starts with none; an agent restart forgets them, and a MicroVM it finds stopped then waits the first backoff. The exit time is not known (garamsh/mvmd#331), so the reset time is measured from when the agent started the MicroVM or first saw it running to when it first sees it stopped, which can lag the exit by up to one resync.

| mvmd state | Phase | Ready |
|---|---|---|
| created | `Pending` | false |
| running | `Running` | true |
| paused | `Suspended` | false |
| stopped, `Halted` | `Stopped` | false |
| stopped, `Once`, clean exit | `Succeeded` | false, not started again |
| stopped, `Once`, failed exit | `Failed` | false, reason `ExitFailed` and a message naming the exit, not started again |
| stopped, `RerunOnFailure` after a clean exit | `Succeeded` | false, not started again |
| stopped, other | `Pending` | false, started again once the restart wait is over |
| faulted | `Failed` | false, the reason is mvmd's last failure reason; stopped and started again once the restart wait is over |

- **Failures the agent settles itself** go to `Failed` with the `Ready` condition false and a reason: `InvalidSpec` for an image that is not `<repository>@sha256:<digest>` or resources it cannot convert, and `Rejected` for a create mvmd refuses, such as one above a Driver's `max_vcpus` or one naming a kernel the node does not offer, with mvmd's reason as the message. The Driver's limit is not on the Node's status. A network or storage plugin's refusal of a MicroVM fails it with the plugin's own reason and message, and `NoStoragePlugin` fails one that names a Volume when no storage plugin is named.
- **A plugin that is not running** leaves the MicroVM `Pending` with `Ready` false and the reason `NetworkPluginUnavailable` or `StoragePluginUnavailable`, until the plugin answers (§Plugins).
- **Outcome kind `IN_PROGRESS`** is treated as never produced by mvmd (garamsh/mvmd#307).

### What the agent does not do yet

- `OnDemand` MicroVMs, wake and suspend.
- Mounting a Volume at its `mountPath`: mvmd's `DiskAttachment` carries a disk identifier and a read-only flag and no path, so the agent attaches the disk and does not pass the path. Volume classes other than `local`, and a `dataSource`, are refused by the `local` plugin or left to a storage plugin that serves them.
- The Node's `addresses`.
- Calling naru.

## Rationale

- One loop for the Lease and the deadline keeps the obligation checkable in one place: the deadline is never renewed outside a round that has just seen the Lease's validity.
- The call-timeout margin pays for not knowing whether an unacknowledged call reached mvmd, instead of asking mvmd afterwards, which a failing path may not allow.
- The restart state is in memory because nothing the contract defines can hold it, and losing it costs one backoff, not a missed restart.
- A clean record equal to the previous one counts as a failure because the cost of a wrong restart is one backoff, and of a wrong stop is a MicroVM that never runs again.
- The exit is read once, at the first sight of the stopped state, so a log that changes afterwards cannot change the verdict.
- The `local` plugin lists Volumes and disks every resync instead of watching because what to write depends on whether mvmd holds the disk, which no Volume event says.
- The plugins are processes because a third party must add a network or a storage system without rebuilding the agent, which is the sign ADR 0012 named; the built-ins are delivered the same way so the API is exercised by what ships. The policy is in the create request because mvmd takes it there, so it cannot be applied late.
- A plugin that is not running leaves the MicroVM `Pending` and retried, not `Failed`, because the plugin may start a moment later and a refusal is the plugin's verdict on the MicroVM, which an absent plugin has not given.
- The plugin names default to none because an agent with nothing installed must still run MicroVMs, isolated; a MicroVM that names a Volume is refused for want of a storage plugin and not silently started without its disk.
- The restart settings are flags with the recorded defaults because the contract fixes no values and a node's operator is the one who knows its workloads.
- The epoch in the mvmd identifier makes rebinding to the same node a replacement rather than a reuse, so state of an older epoch cannot carry into a newer one.

## Open questions

- **Entrypoint record across a start:** mvmd's documents do not say whether the console log an entrypoint's record is answered from is cleared when a MicroVM starts. The agent treats a clean record equal to the one it read at the MicroVM's previous exit as a failure, so a stale record cannot end the restarts; a MicroVM switched from `Always` to `RerunOnFailure` whose entrypoint keeps exiting cleanly is therefore restarted, at the backoff.
- **Node addresses:** which addresses the agent reports.
- **Leaked disks:** the `local` storage plugin deletes a disk only for a Volume it sees being deleted with its finalizer. A `volume-` disk whose Volume the API no longer has, or never pinned to this Node, is not found and not deleted.
