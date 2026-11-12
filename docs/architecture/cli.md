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
| `gaon delete KIND NAME` | Sets the `deletionTimestamp` of a resource, which the server removes at once if no finalizer holds it. |
| `gaon config current-context` | Prints the name of the current context of the config file. |
| `gaon config use-context NAME` | Sets the current context of the config file and writes the file. |

- **Global flag:** `--server URL` is the group and version root of the compute group, `http://<host>:<port>/apis/compute.gaon.garam.sh/v1alpha1`, in full; `GAON_SERVER` is its default, and the current context of the config file is the next default, §Config file. It comes before the command. With none of the three, the CLI exits with a usage error. The roots of the RBAC and apiextensions groups and of a registered kind's group are derived from it by replacing that suffix, [ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §1. A `--server` without the suffix reaches the compute kinds only; a command that needs another group is a usage error before any request to it.
- **TLS flags:** `--tls-ca`, `--tls-cert` and `--tls-key` come before the command, like `--server`, and have no environment variable; each beats the same file of the config file's current context. All three give a mutual TLS 1.3 client built by `lib/api/` that trusts only the CA file; none gives plain HTTP, for a loopback server. One or two, or a file that cannot be read or does not fit, end the CLI with a usage error naming what is missing, before any request.
- **Operands:** a command's flags may stand anywhere among its operands, before, between or after, as `-o` does in `gaon get microvm web -o yaml`. A `--` ends the flags, and what follows it is an operand. A flag a command does not have is a usage error wherever it stands.
- **`KIND`** is any name in §Kind names, matched without regard to case, or a name of a registered kind, §Registered kinds. An unknown kind is a usage error that lists the built-in kinds and the registered ones.
- **No `list` command.** `get KIND` lists; `list` is an unknown command ([ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md)).
- **Exit code:** 0 on success, 1 when a request or a check fails, 2 on a mistake in the command line.
- **Output formats of `get`:** `table` prints the columns below, and is the default; `yaml` and `json` print the object, or the list object for `get KIND`, as the API returned it, with the API's field names. Any other value is a usage error, before any request. `describe` takes no `-o`.

### Config file

The CLI reads a YAML file of clusters, users and contexts, [ADR 0016](./adr/0016-the-gaon-cli-reads-its-server-and-credentials-from-a-config-file-of-clusters-users-and-contexts.md). The file's field names are `current-context`, `clusters[].cluster.server`, `clusters[].cluster.certificate-authority`, `users[].user.client-certificate`, `users[].user.client-key`, `contexts[].context.cluster` and `contexts[].context.user`, each list entry carrying a `name`.

- **Where:** `--config FILE`, else `GAON_CONFIG`, else `.gaon/config` under the home directory. A file named by the flag or the variable must exist; the default may be absent, and then there is no file.
- **A cluster's `server` is the compute root, as `--server` is**, so the other groups' roots are derived from it as from the flag. A context names one cluster and at most one user; the CA is the cluster's, the certificate and key the user's.
- **Credentials are paths**, relative ones taken against the directory of the file. A user names both a certificate and a key or neither. The file holds no embedded data.
- **Ranking:** `--server`, then `GAON_SERVER`, then the current context's cluster; each TLS flag, then the file's value for that file. The three TLS files together must still be all or none.
- **The current context is always resolved**, so a `--server` does not hide a broken file.
- **Refused as a usage error naming the file, before any request:** a named file that is missing, a file that is not YAML or has an unknown field, a repeated or empty name, a current context that names no context, a context that names no cluster or a cluster or user that is not defined, a cluster with no server, a user with one of its two files.
- **`config use-context NAME`** refuses a name that is not a context and leaves the file as it was; writing the file drops its comments. Neither `config` command needs a server, and both need the file to exist.
- **Credentials are never printed or logged.**

### Verbs by kind

A verb works for a kind where `resource-api.md` §Paths has a path for it. A verb with no path is refused before any request, with a message naming the missing path.

`get KIND NAME` and `describe` use the path of one; `get KIND` uses the collection path.

| Kind | `get KIND NAME`, `describe` | `get KIND` | `delete` | `apply` |
|---|---|---|---|---|
| MicroVM | `GET /microvms/{name}` | `GET /microvms` | `DELETE /microvms/{name}` | `PUT /microvms/{name}` |
| Node | `GET /nodes/{name}` | `GET /nodes` | refused, no `DELETE /nodes/{name}` | `PUT /nodes/{name}` |
| Lease | `GET /leases/{name}` | `GET /leases` | refused, no `DELETE /leases/{name}` | `PUT /leases/{name}` |
| Volume | `GET /volumes/{name}` | `GET /volumes` | `DELETE /volumes/{name}` | `PUT /volumes/{name}` |
| CertificateSigningRequest | `GET /certificatesigningrequests/{name}` | `GET /certificatesigningrequests` | refused, no `DELETE /certificatesigningrequests/{name}` | `PUT /certificatesigningrequests/{name}`, the create |
| ClusterRole | `GET /clusterroles/{name}` | `GET /clusterroles` | `DELETE /clusterroles/{name}` | `PUT /clusterroles/{name}` |
| ClusterRoleBinding | `GET /clusterrolebindings/{name}` | `GET /clusterrolebindings` | `DELETE /clusterrolebindings/{name}` | `PUT /clusterrolebindings/{name}` |
| CustomResourceDefinition | `GET /customresourcedefinitions/{name}` | `GET /customresourcedefinitions` | `DELETE /customresourcedefinitions/{name}` | `PUT /customresourcedefinitions/{name}` |
| A registered kind | `GET /<plural>/{name}` | `GET /<plural>` | `DELETE /<plural>/{name}` | `PUT /<plural>/{name}` |

The paths of the last four rows are under the root of their group; a registered kind's is `/apis/<group>/<version>`.

- `apply` never writes a status path, so a status is never applied: a signer's `PUT /certificatesigningrequests/{name}/status` and a custom object's `/status` are not reached. A Volume's `spec` and a CertificateSigningRequest's `spec` are immutable after creation, which the API server enforces.
- Table columns: MicroVM `NAME PHASE NODE EPOCH` (phase is the effective phase, so a void status reads `Pending`), Node `NAME STATE ADMITTED`, Lease `NAME HOLDER DURATION`, Volume `NAME CLASS SIZE PHASE NODE`, CertificateSigningRequest `NAME USERNAME STATE` (`Denied` when a true Denied condition is there, else `Approved` when a true Approved one is, else `Pending`), ClusterRole `NAME RULES` (the number of rules), ClusterRoleBinding `NAME ROLE SUBJECTS` (each `Kind/name`), CustomResourceDefinition `NAME KIND VERSION ESTABLISHED` (the status of the Established condition, or `-`), a registered kind `NAME GENERATION`.
- The built-in kinds are the eight of `lib/api/` that §Paths gives a path the CLI reaches. The admission-registration kinds, every `/status` path and watch are not reached by the CLI yet.

### Kind names

A kind is accepted by its singular, its plural and a short name, [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md) fixing the short names. No two kinds share a name.

| Kind | Singular | Plural | Short |
|---|---|---|---|
| MicroVM | `microvm` | `microvms` | `mvm` |
| Node | `node` | `nodes` | `no` |
| Lease | `lease` | `leases` | `le` |
| Volume | `volume` | `volumes` | `vol` |
| CertificateSigningRequest | `certificatesigningrequest` | `certificatesigningrequests` | `csr` |
| ClusterRole | `clusterrole` | `clusterroles` | `cr` |
| ClusterRoleBinding | `clusterrolebinding` | `clusterrolebindings` | `crb` |
| CustomResourceDefinition | `customresourcedefinition` | `customresourcedefinitions` | `crd` |

### Registered kinds

A kind a CustomResourceDefinition registers is reached on the same verbs, [ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §3 to §7.

- **Found by:** when an operand is no built-in name, the CLI lists the definitions once and keeps the Established ones. The operand matches a definition's `singular`, `plural` or `metadata.name` (`<plural>.<group>`). A definition has no short name.
- **A built-in name wins.** A definition's `metadata.name` still reaches its own kind. An operand that matches several definitions is a usage error naming each `<plural>.<group>`.
- **Apply:** a document is matched by `apiVersion` and `kind` against the Established definitions, decoded strictly into `CustomObject` and checked with `CustomObject.Validate` before anything is sent. The schema is checked by the API server.
- **`--server`:** finding a registered kind needs the apiextensions root, so a `--server` without the compute suffix refuses it with a usage error.

### Describe

`describe KIND NAME` reads the object once and prints, in this order:

- labelled lines `Name`, `Kind`, `Resource version`, `Generation`, `Deletion timestamp` (only when set) and `Finalizers`;
- the kind's own lines: MicroVM image, kernel, run strategy, node, binding epoch, effective phase, requests, limits, volumes, networks, ports, addresses, restart count and last exit; Node admitted, state, capacity and addresses; Lease holder, renew time and duration; Volume storage class, access modes, size, data source, node, phase, capacity and attachment; CertificateSigningRequest username, state and whether a certificate is issued; ClusterRole one `Rule N` line per rule with its API groups, resources and verbs; ClusterRoleBinding role and subjects; CustomResourceDefinition group, names, scope, versions and whether the status subresource is on; a registered kind's `Spec` and `Status`, each as one line of compact JSON;
- for a MicroVM, a Node, a CertificateSigningRequest and a CustomResourceDefinition, the `Conditions` table with `TYPE STATUS REASON LAST TRANSITION MESSAGE`, or `-` when the status has none. The other kinds have no conditions in the contract, so they print no such section.

An empty value prints as `-`.

### Apply

- **A file** holds one or more YAML documents; each carries `apiVersion` and `kind`. An empty document is skipped; a file with no resource is an error.
- **Checked before sending:** every document is decoded strictly (a field the type does not have is an error) and validated with `lib/api/`. If any document fails, or names a kind or verb with no path, no write is sent. Finding a registered kind reads the definitions first, so a refused `apply` may have sent that one `GET` ([ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §8).
- **Compare-and-swap:** `apply` writes with the `resourceVersion` it holds and never retries.
  - A document that carries `metadata.resourceVersion` is written against it, so a file made from `gaon get` is applied only over the version it was read at.
  - A document without one is first read; a `NotFound` means create, otherwise the version read is the one written against. A change made after that read fails the write.
- **A concurrent change is reported, not overwritten:** `Conflict` (the version moved) and `AlreadyExists` (a create lost a race) end `apply` with exit code 1 and a message saying the resource was changed by someone else.
- **Order and failure:** documents are sent in file order and `apply` stops at the first failure; the lines already printed (`microvm/web created`, `node/n1 configured`) were applied.
- **Quantities** in a file may be strings or numbers: `cpu: "2"` and `cpu: 2` both decode, because `lib/api/` decodes a Quantity from either a JSON string or a JSON number.

### Delete

`delete` reads the resource, then deletes it at the version it read. A change between the two is reported as a `Conflict`. What it prints follows the server's answer. A resource that still has finalizers comes back with its `deletionTimestamp` set; the command prints `KIND/NAME marked for deletion`, and the owner of each finalizer removes it after its work is done (a MicroVM's Node Agent after release). A resource with no finalizer, such as a ClusterRole, a Volume with no `nodeName` or a MicroVM the Node Agent has not yet started, is removed by the server at once, and the command prints `KIND/NAME deleted`.

## Rationale

- The verbs follow the paths because the PM reading of issue 8 fixes "every resource kind in `lib/api/`" as every verb with a path in §Paths; a Node cannot be deleted because the contract has no path for it.
- `get KIND` lists, and `list` is gone, because two verbs for one read is the difference from `kubectl get` the owner met; the reasons are in [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md).
- The other groups' roots are derived from `--server`, and a registered kind is found among the Established definitions, because the CLI has one address and `lib/api/` has no discovery; the alternatives are in [ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md).
- A cluster's `server` is the compute root, not a base address, so the file and `--server` hold one meaning of one word; the alternatives are in [ADR 0016](./adr/0016-the-gaon-cli-reads-its-server-and-credentials-from-a-config-file-of-clusters-users-and-contexts.md).
- `apply` takes its version from the file first because a version the user read is the only basis on which a change by someone else since can be detected before the write, as `resource-api.md` §Common rules makes every write a compare-and-swap.

## Open questions

- **Namespace:** the CLI sends no namespace, as the paths have none yet ([resource-api.md](./resource-api.md) §Open questions).
- **A `--context` flag, embedded credential data, bearer tokens, output formats beyond `table`, `yaml` and `json`:** out of scope.
