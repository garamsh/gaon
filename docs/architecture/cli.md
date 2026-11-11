# CLI

The `gaon` client CLI: its commands, which resource kinds each command reaches, and how `apply` treats a concurrent change.

Basis: [resource-api.md](./resource-api.md) §Paths and the types of `lib/api/`. The CLI is the unit `gaon-cli/`, binary `gaon` at `gaon-cli/cmd/gaon`, and talks to the API only through `lib/api/`.

## Current decisions

### Commands

| Command | Does |
|---|---|
| `gaon apply -f FILE` | Creates or updates each resource in a YAML file, in file order. |
| `gaon get [-o yaml\|table] KIND NAME` | Prints one resource. Default `yaml`. |
| `gaon list [-o yaml\|table] KIND` | Prints every resource of a kind. Default `table`. |
| `gaon delete KIND NAME` | Sets the `deletionTimestamp` of a Machine. |

- **Global flag:** `--server URL` is the group and version root of the resource API; `GAON_SERVER` is its default. It comes before the command. With neither, the CLI exits with a usage error.
- **Operands:** a command's flags come before its operands. `KIND` is the lower-case kind name, matched without regard to case.
- **Exit code:** 0 on success, 1 when a request or a check fails, 2 on a mistake in the command line.
- **Output formats:** `yaml` prints the object as the API returned it, with the API's field names. `table` prints the columns below. Other formats are out of scope.

### Verbs by kind

A verb works for a kind where `resource-api.md` §Paths has a path for it. A verb with no path is refused before any request, with a message naming the missing path.

| Kind | `get` | `list` | `delete` | `apply` |
|---|---|---|---|---|
| Machine | `GET /machines/{name}` | `GET /machines` | `DELETE /machines/{name}` | `PUT /machines/{name}` |
| Node | `GET /nodes/{name}` | `GET /nodes` | refused, no `DELETE /nodes/{name}` | `PUT /nodes/{name}` |
| Lease | `GET /leases/{name}` | `GET /leases` | refused, no `DELETE /leases/{name}` | `PUT /leases/{name}` |
| Volume | refused | refused | refused | refused, no `PUT /volumes/{name}` |

- Volume has only `PUT /volumes/{name}/status`, which no command uses. `apply` never writes a status path, so a Volume cannot be applied.
- Table columns: Machine `NAME PHASE NODE EPOCH` (phase is the effective phase, so a void status reads `Pending`), Node `NAME STATE ADMITTED`, Lease `NAME HOLDER DURATION`.

### Apply

- **A file** holds one or more YAML documents; each carries `apiVersion` and `kind`. An empty document is skipped; a file with no resource is an error.
- **Checked before sending:** every document is decoded strictly (a field the type does not have is an error) and validated with `lib/api/`. If any document fails, or names a kind or verb with no path, nothing is sent.
- **Compare-and-swap:** `apply` writes with the `resourceVersion` it holds and never retries.
  - A document that carries `metadata.resourceVersion` is written against it, so a file made from `gaon get` is applied only over the version it was read at.
  - A document without one is first read; a `NotFound` means create, otherwise the version read is the one written against. A change made after that read fails the write.
- **A concurrent change is reported, not overwritten:** `Conflict` (the version moved) and `AlreadyExists` (a create lost a race) end `apply` with exit code 1 and a message saying the resource was changed by someone else.
- **Order and failure:** documents are sent in file order and `apply` stops at the first failure; the lines already printed (`machine/web created`, `node/n1 configured`) were applied.
- **Quantities** in a file are strings: `cpu: "2"`, not `cpu: 2`, because `lib/api/` decodes a Quantity from a JSON string only.

### Delete

`delete` reads the Machine, then deletes it at the version it read. A change between the two is reported as a `Conflict`. The Machine is only marked; the Node Agent removes it after release.

## Rationale

- The verbs follow the paths because the PM reading of issue 8 fixes "every resource kind in `lib/api/`" as every verb with a path in §Paths; a Node cannot be deleted because the contract has no path for it.
- `apply` takes its version from the file first because a version the user read is the only basis on which a change by someone else since can be detected before the write, as `resource-api.md` §Common rules makes every write a compare-and-swap.

## Open questions

- **Server address:** `--server` takes the group and version root, because `resource-api.md` §Paths is relative to it and gives no prefix; the API server unit may fix one.
- **Namespace:** the CLI sends no namespace, as the paths have none yet ([resource-api.md](./resource-api.md) §Open questions).
- **Authentication, several clusters, other output formats:** out of scope for issue 8.
