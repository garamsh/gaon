# API server

`gaon-apiserver` serves the `v1alpha1` resource API over etcd. The contract it serves, and the invariants it holds, are in [resource-api.md](./resource-api.md); this document describes how the server is built to hold them. The module is `apiserver/`, its binary `apiserver/cmd/gaon-apiserver`.

## Current decisions

### Responsibility

The server is the only process that talks to the Store. Machines, Volumes, Nodes and Leases are read and written through it by every other unit, using the client in `lib/api`. It owns the invariants the contract assigns to the API server: compare-and-swap on `resourceVersion`, the split of a resource into the sections each writer owns, the state a resource is created in, finalizers, and the check that a Lease names an existing Node. Every rule that is a pure function of resources (`Validate`, `Bind`, `ValidateStatusUpdate`, `Attach`, `ValidateStatus`) lives in `lib/api` and the server calls it; the server adds only what needs the Store.

### Shape

The module follows the service layout of the stack convention. A transport at the module root decodes a request, calls one service method and translates its error; the service holds the rules; a Store interface is the only way the service reaches etcd, and one implementation of it wraps the etcd client.

- The transport serves every path of resource-api.md §Paths under the root `/apis/compute.gaon.garam.sh/v1alpha1` on the server's address, and answers any path outside the root, or none of those, with the `NotFound` error object. It has one error translator: an `APIError` becomes its code and body, and any other error is logged once and answered as `Internal`, 500.
- The service has one generic description of a kind (its path prefix and how its resources are keyed) and one file of rules per kind. A kind that needs no rule beyond the generic ones adds none.
- The Store interface speaks in keys, bytes and revisions: get, list, create, update at a revision, delete, and watch from a revision. It knows nothing of resources.

### The Store

`resourceVersion` is the etcd mod revision of the key, as the contract fixes. A resource is stored as its JSON without the version, under `/registry/<plural>/<name>`; the version is set on every read from the revision.

- A create is a transaction that succeeds only where the key does not exist; an update and a delete are transactions that succeed only where the key's mod revision equals the one the service read. A transaction that fails reads the key again to say whether it was missing or stale. Two writers of one version cannot both win, which is the property the contract builds on.
- The service reads before it writes, so it can check the version, run the rules and refuse early; the transaction is the guard that holds under a race.
- A list is one read, and its response carries the revision the read was made at. Listing, then watching from that version, loses nothing and replays nothing.
- A watch is an etcd watch from the given version plus one, on the kind's prefix. A `Deleted` event carries the last stored value, with its version set to the revision of the deletion.
- A compacted version ends the stream with an `Error` event whose reason is `Expired`, as the contract fixes; it is the last event, and the HTTP response is a stream, not an error status. A watch without a version, or with one that is not a number, is refused with `Invalid` before the stream opens.

### Writes

Every write to an existing resource carries the version it was read at, and a write without it or with a stale one gets `Conflict`; a write to a resource that does not exist gets `NotFound`. A PUT without a version creates and gets `AlreadyExists` where the name is taken.

- **Sections.** A spec PUT keeps the stored status, binding fields and deletion timestamp; a status PUT keeps the stored spec and metadata; the binding path sets only `spec.nodeName` and `spec.bindingEpoch`. The contract names a writer for each section, and the server cannot tell writers apart, because authentication gives it an identity but no rule on which identity writes which section (see open questions); it keeps the sections apart by the path a write arrives on.
- **Generation.** The generation of a resource is 1 on creation and grows by one on every write that changes its spec, a binding included.
- **Creation.** A Machine is created `Pending`, with no node and no epoch; a create that carries a node or an epoch is `Invalid`. A Node is created `Discovered`, whatever status the body carries.
- **Node state.** A Node status write that changes `status.state` must set the state that agrees with the stored `spec.admitted`: `Admitted` where it is true, `Discovered` where it is false. A write that leaves the state as stored is accepted whatever `spec.admitted` is, so one writer's lag, such as the node lifecycle controller writing `LeaseExpired` while the Node Agent has not yet moved the state, does not block another writer's section. The server does not move the state when the spec changes; the Node Agent does, through a status write.
- **Lease.** A Lease is named as the Node it belongs to; a write for a name that is no Node is `Invalid`.
- **Volume.** A Volume status write that changes the attachment must name a Machine that exists, at that Machine's current binding epoch, and then holds only where `Volume.Attach` accepts it: the Volume is unattached or attached at a lower epoch. The first failure is `Invalid`, the second `Conflict`. A Volume has no create path in the contract, so a status write for an absent Volume is `NotFound`.
- **Finalizers.** A delete of a resource with finalizers sets its deletion timestamp and keeps it; a second delete keeps the first timestamp. A finalizer cannot be added once the timestamp is set. The write that clears the last finalizer of a resource being deleted removes it, and the watch shows `Deleted`. A resource without finalizers is removed by the delete at once. The deletion timestamp comes from the server's clock, injected so a test can fix it.
- **Bodies.** A body is refused if it has a field the type does not, if anything follows the value, or if it is over 1 MiB. A body whose name differs from the path, or a name with a slash, is `Invalid`; a slash would address another key.

### Running

`Run` takes a listener, a configuration and a context, and serves until the context ends, then stops accepting and drains for a short time. Without the TLS files the server serves plain HTTP and `Run` refuses a listener that is not on a loopback address. With them it serves mutual TLS on any address, loopback included: the server speaks TLS 1.3 or later and, as built, requires a client certificate that the client CA signed ([ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md)). [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) narrows the requirement; the list below states the decision, which the binary does not yet implement. The binary reads the etcd endpoints, the listen address, the dial timeout and the TLS files from flags and defaults to loopback for the listener.

- `--tls-client-ca`, `--tls-cert` and `--tls-key` are the PEM files of the client CA and of the server's certificate and key. All three are given or none is, and a partial set, or a file that does not load, stops the server at start.
- The certificate's subject CN is the caller's identity, a Node Agent's being its Node name. The server authenticates and does not yet act on the identity.
- **Decided in [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), not yet implemented:**
  - The listener verifies a client certificate when one is given and no longer requires one. A request with a verified certificate is authenticated as before.
  - A request with no certificate and a valid bearer token may only create a CertificateSigningRequest and read one it created, with the identity `bootstrap:<id>`. Any other request without a certificate is refused, an invalid token included, and a token is honoured over TLS only.
  - The server takes `--bootstrap-token-file`, a file of valid tokens, one per line, read once at start. Removing a token takes effect at the next start.
- The server reads the files once at start. It dials etcd at start and fails if it cannot.

## Rationale

- Mutual TLS beyond loopback, the certificate's CN as identity, and authentication without authorization: [ADR 0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md).
- The Store is etcd, and `resourceVersion`, compare-and-swap and watch map onto etcd's revisions, transactions and watches: [ADR 0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md). Keeping the Store behind an interface of keys and revisions keeps the rules testable against a real etcd without the rules knowing it.
- One writer per section, and the Lease and the epoch as the fence against a stale writer: [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). The server holds the invariants the contract gives it; it does not duplicate the rules that `lib/api` owns, so a client and the server cannot disagree about them.
- The server is its own module with its own binary, a unit of the workspace: [ADR 0001](./adr/0001-the-repository-is-a-go-workspace-of-executable-units-partitioned-by-responsibility.md). It imports `lib/api` and nothing else of gaon.
- Tests run the server over an embedded etcd member started in the test process, because no etcd binary is assumed on a developer machine or a CI runner. The etcd server module is a dependency of the tests only; the binary links the etcd client alone. The module is on `go 1.26.0`, as is the workspace, and pins etcd 3.6.16, the newest 3.6 release, with `golang.org/x/net` 0.62.0 and `google.golang.org/grpc` 1.83.2 that it requires. Release 3.6.4, the first pin, carried a client that reached vulnerable code in x/net and grpc from the shipped binary, and the releases that fix them need Go 1.25 or later, which is why the workspace's Go version moved. `govulncheck` over the module reports nothing under Go 1.26.11 and reports only standard library findings under 1.26.0, so the workspace's `toolchain` directive in `go.work` names a 1.26 patch release, which CI and a developer's `go` command build with, and the owner of `go.work` moves it by hand. The pin moves with the Dependabot entry for the module, and with the workspace's Go version.

## Open questions

- **Authorization.** The contract names a writer for each section, and the server authenticates a caller by its certificate but does not check which identity writes which section. Any authenticated caller may write any path, and a client that writes a section it does not own is stopped only by the path.
- **Volume creation.** The contract gives a Volume a status path and no create path, so a Volume cannot come into being through this API. Whoever creates Volumes needs a path in the contract, or the server needs to admit an absent Volume on a status write.
- **Node created with `spec.admitted` true.** The server starts every Node `Discovered`, so one created already admitted waits for the Node Agent to move its state. The contract does not say whether a create may carry `admitted`.
- **The `Deleted` event.** It carries the last stored value, not the resource as the delete left it, so a consumer sees the finalizer-free state only through the preceding `Modified`. The contract does not say which a consumer should expect.
- **Compaction.** Nothing compacts etcd yet. A watch expires only when an operator compacts, and a cell that is never compacted grows without bound.
- **More than one member.** The server dials the endpoints it is given and relies on etcd's linearizable reads; it has not been run against a cell of more than one member.
