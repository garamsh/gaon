# CLI

The `gaon` client CLI: its commands and grammar, which resource kinds each command reaches, and how `apply` treats a concurrent change.

Basis: [resource-api.md](./resource-api.md) §Paths and the types of `lib/api/`. The CLI is the unit `gaon-cli/`, binary `gaon` at `gaon-cli/cmd/gaon`, and talks to the API only through `lib/api/`.

## Current decisions

### Commands

| Command | Does |
|---|---|
| `gaon apply -f FILE` | Creates or updates each resource in a YAML file, in file order, and leaves one the file already matches. `-f -` reads standard input. |
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
| Secret | `GET /secrets/{name}` | `GET /secrets` | `DELETE /secrets/{name}` | `PUT /secrets/{name}` |
| ConfigMap | `GET /configmaps/{name}` | `GET /configmaps` | `DELETE /configmaps/{name}` | `PUT /configmaps/{name}` |
| ServiceAccount | `GET /serviceaccounts/{name}` | `GET /serviceaccounts` | `DELETE /serviceaccounts/{name}` | `PUT /serviceaccounts/{name}` |
| Service | `GET /services/{name}` | `GET /services` | `DELETE /services/{name}` | `PUT /services/{name}` |
| Endpoints | `GET /endpoints/{name}` | `GET /endpoints` | `DELETE /endpoints/{name}` | `PUT /endpoints/{name}` |
| NetworkPolicy | `GET /networkpolicies/{name}` | `GET /networkpolicies` | `DELETE /networkpolicies/{name}` | `PUT /networkpolicies/{name}` |
| A registered kind | `GET /<plural>/{name}` | `GET /<plural>` | `DELETE /<plural>/{name}` | `PUT /<plural>/{name}` |

The paths of the six core kinds and of MicroVM, Node, Lease, Volume and CertificateSigningRequest are under the compute root; those of ClusterRole and ClusterRoleBinding are under the RBAC root, CustomResourceDefinition under the apiextensions root, and a registered kind's under `/apis/<group>/<version>`.

- `apply` never writes a status path, so a status is never applied: a signer's `PUT /certificatesigningrequests/{name}/status` and a custom object's `/status` are not reached. A Volume's `spec` and a CertificateSigningRequest's `spec` are immutable after creation, which the API server enforces.
- Table columns: MicroVM `NAME PHASE NODE EPOCH` (phase is the effective phase, so a void status reads `Pending`), Node `NAME STATE ADMITTED`, Lease `NAME HOLDER DURATION`, Volume `NAME CLASS SIZE PHASE NODE`, CertificateSigningRequest `NAME USERNAME STATE` (`Denied` when a true Denied condition is there, else `Approved` when a true Approved one is, else `Pending`), ClusterRole `NAME RULES` (the number of rules), ClusterRoleBinding `NAME ROLE SUBJECTS` (each `Kind/name`), CustomResourceDefinition `NAME KIND VERSION ESTABLISHED` (the status of the Established condition, or `-`), Secret and ConfigMap `NAME KEYS` (the number of keys), ServiceAccount `NAME`, Service `NAME TYPE CLUSTER-IP EXTERNAL-IP PORTS` (each `[name:]port/protocol[->targetPort]`), Endpoints `NAME ADDRESSES` (the number of addresses), NetworkPolicy `NAME POLICY-TYPES INGRESS EGRESS` (the effective policy types and the number of rules of each direction), a registered kind `NAME GENERATION`.
- **A Secret's data is never printed by a table or by `describe`**, which list only its key names and the size of each value. `get secret NAME -o yaml` and `-o json` print the object as the API returned it, so `data` is there, each value base64, to anyone who can read the Secret.
- **A Service keeps its allocated addresses on `apply`.** A document that leaves `spec.clusterIP` or `spec.loadBalancerIP` empty is compared as if it held the stored value, as the API server's update does (`Service.KeepAllocated`), so it prints `unchanged` when nothing else differs, and `configured` when it does. The document is sent as written, and the server keeps the address. A document naming another address is sent, and the server refuses it.
- The built-in kinds are the fourteen of `lib/api/` that §Paths gives a path the CLI reaches. The admission-registration kinds, every `/status` path and watch are not reached by the CLI yet.

### Kind names

A kind is accepted by its singular, its plural and a short name where it has one, [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md) fixing the short names. No two kinds share a name.

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
| Secret | `secret` | `secrets` | none |
| ConfigMap | `configmap` | `configmaps` | `cm` |
| ServiceAccount | `serviceaccount` | `serviceaccounts` | `sa` |
| Service | `service` | `services` | `svc` |
| Endpoints | `endpoints` | `endpoints` | `ep` |
| NetworkPolicy | `networkpolicy` | `networkpolicies` | `netpol` |

### Registered kinds

A kind a CustomResourceDefinition registers is reached on the same verbs, [ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §3 to §7.

- **Found by:** when an operand is no built-in name, the CLI lists the definitions once and keeps the Established ones. The operand matches a definition's `singular`, `plural` or `metadata.name` (`<plural>.<group>`). A definition has no short name.
- **A built-in name wins.** A definition's `metadata.name` still reaches its own kind. An operand that matches several definitions is a usage error naming each `<plural>.<group>`.
- **Apply:** a document is matched by `apiVersion` and `kind` against the Established definitions, decoded strictly into `CustomObject` and checked with `CustomObject.Validate` before anything is sent. The schema is checked by the API server.
- **`--server`:** finding a registered kind needs the apiextensions root, so a `--server` without the compute suffix refuses it with a usage error.

### Describe

`describe KIND NAME` reads the object once and prints, in this order:

- labelled lines `Name`, `Kind`, `Resource version`, `Generation`, `Deletion timestamp` (only when set) and `Finalizers`;
- the kind's own lines: MicroVM image, kernel, run strategy, node, binding epoch, effective phase, requests, limits, volumes, networks, ports, addresses, restart count and last exit; Node admitted, state, capacity and addresses; Lease holder, renew time and duration; Volume storage class, access modes, size, data source, node, phase, capacity and attachment; CertificateSigningRequest username, state and whether a certificate is issued; ClusterRole one `Rule N` line per rule with its API groups, resources and verbs; ClusterRoleBinding role and subjects; CustomResourceDefinition group, names, scope, versions and whether the status subresource is on; Secret its key names with the size of each value, never a value; ConfigMap its key names; ServiceAccount none; Service type, selector, cluster IP, load balancer IP, external IP and ports; Endpoints one `Subset N` line per subset with its addresses and ports; NetworkPolicy pod selector, policy types and the number of ingress and egress rules; a registered kind's `Spec` and `Status`, each as one line of compact JSON;
- for a MicroVM, a Node, a CertificateSigningRequest and a CustomResourceDefinition, the `Conditions` table with `TYPE STATUS REASON LAST TRANSITION MESSAGE`, or `-` when the status has none. The other kinds have no conditions in the contract, so they print no such section.

An empty value prints as `-`.

### Apply

- **A file** holds one or more YAML documents; each carries `apiVersion` and `kind`. An empty document, or one of only comments, is skipped; a file with no resource is an error. `-f -` takes the documents from standard input, and errors name it `standard input`.
- **Checked before sending:** every document is decoded strictly (a field the type does not have is an error) and validated with `lib/api/`. If any document fails, or names a kind or verb with no path, no write is sent. Finding a registered kind reads the definitions first, so a refused `apply` may have sent that one `GET` ([ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §8).
- **Compare-and-swap:** `apply` writes with the `resourceVersion` it holds and never retries.
  - A document that carries `metadata.resourceVersion` is written against it, so a file made from `gaon get` is applied only over the version it was read at.
  - A document without one is first read; a `NotFound` means create, otherwise the version read is the one written against. A change made after that read fails the write.
- **Unchanged:** a document without a `resourceVersion` that the object read already matches is not sent, and prints `KIND/NAME unchanged`. It matches when the document and the object are equal in every field a write sets: everything but `status` and the metadata the server sets (`uid`, `resourceVersion`, `generation`, `deletionTimestamp`). A document that carries a `resourceVersion` is always sent. A field the server adds to a stored object that the document lacks makes the two differ, so the document is sent and prints `configured`; a MicroVM or Volume the server has bound (`spec.nodeName`) is such an object, and the server refuses the write.
- **A concurrent change is reported, not overwritten:** `Conflict` (the version moved) and `AlreadyExists` (a create lost a race) end `apply` with exit code 1 and a message saying the resource was changed by someone else.
- **Order and failure:** documents are sent in file order and `apply` stops at the first failure; the lines already printed (`microvm/web created`, `node/n1 configured`, `lease/n1 unchanged`) were applied. `apply` does not retry a document whose referent is missing: a document that needs another comes after it, and running the same input again after a failure sends only what is not yet applied, the rest printing `unchanged`.
- **Quantities** in a file may be strings or numbers: `cpu: "2"` and `cpu: 2` both decode, because `lib/api/` decodes a Quantity from either a JSON string or a JSON number.

### Applying a Helm-rendered stream

`helm template RELEASE CHART | gaon apply -f -` applies a chart whose templates emit gaon kinds. Helm is the template engine only; gaon does not serve the Kubernetes API, so a chart that emits Kubernetes kinds is not supported. The example chart is `tooling/e2e/testdata/helm/web/`, and `tooling/e2e/extensions/helm_test.go` renders it with the release name `rel` and applies it twice, a second time reporting every object `unchanged`.

- **Order** in the stream is Helm's: a kind Helm knows (`Secret`, `ClusterRole`, `Deployment`, `Job` and the other Kubernetes kinds) in Helm's install order, then every other kind in template file-name order. A chart that needs one gaon-only kind before another names its files so they sort that way.
- **Kinds reached** are those of §Verbs by kind and a registered kind, so a document of any other kind is refused and nothing in the stream is sent. A chart may emit `Secret`, `ConfigMap`, `ServiceAccount`, `Service`, `Endpoints` and `NetworkPolicy` documents.
- **Not offered, and what to use:**
  - **Removing an object a later render no longer holds:** nothing. `apply` records no owner or label of the render, so it cannot tell an object the chart made from one made by another client. Delete it with `gaon delete KIND NAME`.
  - **History and rollback:** nothing. gaon keeps no record of a render. Rolling back is rendering the earlier chart version and applying it.
  - **Hooks:** nothing. Helm runs a hook as a Job or a pod around an install, which `helm template` does not do; a template carrying a hook annotation is applied as an ordinary document.
- **Release names** are the chart's: gaon does not track a release, so a second `helm template` with another name applies a second set of objects.

### Delete

`delete` reads the resource, then deletes it at the version it read. A change between the two is reported as a `Conflict`. What it prints follows the server's answer. A resource that still has finalizers comes back with its `deletionTimestamp` set; the command prints `KIND/NAME marked for deletion`, and the owner of each finalizer removes it after its work is done (a MicroVM's Node Agent after release). A resource with no finalizer, such as a ClusterRole, a Volume with no `nodeName` or a MicroVM the Node Agent has not yet started, is removed by the server at once, and the command prints `KIND/NAME deleted`.

## Rationale

- The verbs follow the paths because the PM reading of issue 8 fixes "every resource kind in `lib/api/`" as every verb with a path in §Paths; a Node cannot be deleted because the contract has no path for it.
- `get KIND` lists, and `list` is gone, because two verbs for one read is the difference from `kubectl get` the owner met; the reasons are in [ADR 0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md).
- The other groups' roots are derived from `--server`, and a registered kind is found among the Established definitions, because the CLI has one address and `lib/api/` has no discovery; the alternatives are in [ADR 0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md).
- A cluster's `server` is the compute root, not a base address, so the file and `--server` hold one meaning of one word; the alternatives are in [ADR 0016](./adr/0016-the-gaon-cli-reads-its-server-and-credentials-from-a-config-file-of-clusters-users-and-contexts.md).
- `apply` takes its version from the file first because a version the user read is the only basis on which a change by someone else since can be detected before the write, as `resource-api.md` §Common rules makes every write a compare-and-swap.
- `apply` compares the document to the object it reads and sends nothing when they match, because every write bumps `resourceVersion` on the server and re-applying a stream would otherwise change every object, and it prints a third result so a run shows what it did. The alternatives are in [ADR 0035](./adr/0035-gaon-apply-reads-standard-input-reports-an-unchanged-object-and-offers-no-prune-history-or-hooks-for-a-helm-rendered-stream.md).

## Open questions

- **Namespace:** the CLI sends no namespace, as the paths have none yet ([resource-api.md](./resource-api.md) §Open questions).
- **Prune, history and hooks for a rendered stream:** not offered, §Applying a Helm-rendered stream. A prune needs a record of what a render made, which `apply` has no field for.
- **A `--context` flag, embedded credential data, bearer tokens, output formats beyond `table`, `yaml` and `json`:** out of scope.
