# CLI

The `gaon` client CLI: its commands and grammar, which resource kinds each command reaches, and how `apply` treats a concurrent change.

Basis: [resource-api.md](./resource-api.md) §Paths and the types of `lib/api/`. The CLI is the unit `gaon-cli/`, binary `gaon` at `gaon-cli/cmd/gaon`, and talks to the API only through `lib/api/`.

## Current decisions

### Commands

| Command | Does |
|---|---|
| `gaon apply -f FILE` | Creates or updates each resource in a YAML file, in file order. |
| `gaon get [-o table\|yaml\|json] KIND` | Prints every resource of a kind. Default `table`. |
| `gaon get [-o table\|yaml\|json] KIND NAME` | Prints one resource. Default `table`. |
| `gaon describe KIND NAME` | Prints one resource as labelled lines and its status conditions. |
| `gaon delete KIND NAME` | Sets the `deletionTimestamp` of a resource. |

- **Global flag:** `--server URL` is the group and version root of the resource API, `http://<host>:<port>/apis/compute.gaon.garam.sh/v1alpha1`, in full; `GAON_SERVER` is its default. It comes before the command. With neither, the CLI exits with a usage error.
- **TLS flags:** `--tls-ca`, `--tls-cert` and `--tls-key` come before the command, like `--server`, and have no environment variable. All three give a mutual TLS 1.3 client built by `lib/api/` that trusts only the CA file; none gives plain HTTP, for a loopback server. One or two, or a file that cannot be read or does not fit, end the CLI with a usage error naming what is missing, before any request.
- **Operands:** a command's flags may stand anywhere among its operands, before, between or after, as `-o` does in `gaon get machine web -o yaml`. A `--` ends the flags, and what follows it is an operand. A flag a command does not have is a usage error wherever it stands.
- **`KIND`** is any name in §Kind names, matched without regard to case. An unknown kind is a usage error that lists the kinds.
- **No `list` command.** `get KIND` lists; `list` is an unknown command ([ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md)).
- **Exit code:** 0 on success, 1 when a request or a check fails, 2 on a mistake in the command line.
- **Output formats of `get`:** `table` prints the columns below, and is the default; `yaml` and `json` print the object, or the list object for `get KIND`, as the API returned it, with the API's field names. Any other value is a usage error, before any request. `describe` takes no `-o`.

### Verbs by kind

A verb works for a kind where `resource-api.md` §Paths has a path for it. A verb with no path is refused before any request, with a message naming the missing path.

`get KIND NAME` and `describe` use the path of one; `get KIND` uses the collection path.

| Kind | `get KIND NAME`, `describe` | `get KIND` | `delete` | `apply` |
|---|---|---|---|---|
| Machine | `GET /machines/{name}` | `GET /machines` | `DELETE /machines/{name}` | `PUT /machines/{name}` |
| Node | `GET /nodes/{name}` | `GET /nodes` | refused, no `DELETE /nodes/{name}` | `PUT /nodes/{name}` |
| Lease | `GET /leases/{name}` | `GET /leases` | refused, no `DELETE /leases/{name}` | `PUT /leases/{name}` |
| Volume | `GET /volumes/{name}` | `GET /volumes` | `DELETE /volumes/{name}` | `PUT /volumes/{name}` |

- `apply` never writes a status path, so a status is never applied; a Volume's `spec` is immutable after creation, which the API server enforces.
- Table columns: Machine `NAME PHASE NODE EPOCH` (phase is the effective phase, so a void status reads `Pending`), Node `NAME STATE ADMITTED`, Lease `NAME HOLDER DURATION`, Volume `NAME CLASS SIZE PHASE NODE`.
- The kinds are the four of `lib/api/` that §Paths gives the compute root. The other kinds §Paths lists are not reached by the CLI yet.

### Kind names

A kind is accepted by its singular, its plural and a short name, [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md) fixing the short names. No two kinds share a name.

| Kind | Singular | Plural | Short |
|---|---|---|---|
| Machine | `machine` | `machines` | `mc` |
| Node | `node` | `nodes` | `no` |
| Lease | `lease` | `leases` | `le` |
| Volume | `volume` | `volumes` | `vol` |

### Describe

`describe KIND NAME` reads the object once and prints, in this order:

- labelled lines `Name`, `Kind`, `Resource version`, `Generation`, `Deletion timestamp` (only when set) and `Finalizers`;
- the kind's own lines: Machine image, kernel, run strategy, node, binding epoch, effective phase, requests, limits, volumes, networks, ports, addresses, restart count and last exit; Node admitted, state, capacity and addresses; Lease holder, renew time and duration; Volume storage class, access modes, size, data source, node, phase, capacity and attachment;
- for a Machine and a Node, the `Conditions` table with `TYPE STATUS REASON LAST TRANSITION MESSAGE`, or `-` when the status has none. A Lease and a Volume have no conditions in the contract, so they print no such section.

An empty value prints as `-`.

### Apply

- **A file** holds one or more YAML documents; each carries `apiVersion` and `kind`. An empty document is skipped; a file with no resource is an error.
- **Checked before sending:** every document is decoded strictly (a field the type does not have is an error) and validated with `lib/api/`. If any document fails, or names a kind or verb with no path, nothing is sent.
- **Compare-and-swap:** `apply` writes with the `resourceVersion` it holds and never retries.
  - A document that carries `metadata.resourceVersion` is written against it, so a file made from `gaon get` is applied only over the version it was read at.
  - A document without one is first read; a `NotFound` means create, otherwise the version read is the one written against. A change made after that read fails the write.
- **A concurrent change is reported, not overwritten:** `Conflict` (the version moved) and `AlreadyExists` (a create lost a race) end `apply` with exit code 1 and a message saying the resource was changed by someone else.
- **Order and failure:** documents are sent in file order and `apply` stops at the first failure; the lines already printed (`machine/web created`, `node/n1 configured`) were applied.
- **Quantities** in a file may be strings or numbers: `cpu: "2"` and `cpu: 2` both decode, because `lib/api/` decodes a Quantity from either a JSON string or a JSON number.

### Delete

`delete` reads the resource, then deletes it at the version it read. A change between the two is reported as a `Conflict`. The resource is only marked; the Node Agent removes its finalizer after release.

## Rationale

- The verbs follow the paths because the PM reading of issue 8 fixes "every resource kind in `lib/api/`" as every verb with a path in §Paths; a Node cannot be deleted because the contract has no path for it.
- `get KIND` lists, and `list` is gone, because two verbs for one read is the difference from `kubectl get` the owner met; the reasons are in [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md).
- `apply` takes its version from the file first because a version the user read is the only basis on which a change by someone else since can be detected before the write, as `resource-api.md` §Common rules makes every write a compare-and-swap.

## Open questions

- **Namespace:** the CLI sends no namespace, as the paths have none yet ([resource-api.md](./resource-api.md) §Open questions).
- **Authentication, several clusters, output formats beyond `table`, `yaml` and `json`:** out of scope.
