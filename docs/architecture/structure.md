# Structure

The shape gaon is partitioned into, what may reference what, and when a new unit is earned.

## The shape

gaon is divided by responsibility into executable units, one top-level folder per unit. A unit is a Go module with its binary under `cmd/`, or a library module under `lib/`. This is the domain structure with the unit as the boundary: each folder is named for what it runs or publishes, never for a technical role.

Nothing here states what goes inside a unit, or how the units are deployed. A unit's own files follow `docs/convention/stack-go.md` §1; deployment is a separate decision. The control-plane units and the Node Agent run as host services naru starts, not inside microVMs.

## Units

The units of milestone M1, each created by the pull request of the issue that owns it (issues in garamsh/gaon):

- **`lib/api/`**, a library module: the `v1alpha1` resource types and their client. Issue 3.
- **`apiserver/`**: the resource API server, binary `gaon-apiserver`. Issue 4.
- **`scheduler/`**: binds unbound MicroVMs to Nodes, binary `gaon-scheduler`. Issue 5.
- **`controllers/`**: the controllers, the node lifecycle controller among them. Issue 6.
- **`node-agent/`**: runs bound MicroVMs on mvmd, binary `gaon-node-agent`. Issue 7.
- **`lib/plugin/`**, a library module: the `gaon.plugin.v1alpha1` API the Node Agent calls its plugins through, with its Go client and server helpers. Issue 121.
- **`network-default/`**: the network plugin `default`, binary `gaon-network-default`. Issue 121.
- **`storage-local/`**: the storage plugin `local`, binary `gaon-storage-local`. Issue 121.
- **`gaon-cli/`**: the client CLI, binary `gaon`. Issue 8.
- **`workloads/`**: the controller of the `Job` kind, a client of the public API with its own identity, binary `gaon-workloads`. Issue 146.
- **`etcd-snapshot/`**: the backup of the Store, a client of etcd run as a CronJob, binary `gaon-etcd-snapshot`. Issue 174.
- **`tooling/e2e/`**: the end-to-end test of M1 on QEMU nodes. Issue 9.

The Cell Router and microvm and cluster management are in gaon's scope (Discussion #2 comments `discussioncomment-19466471` and `discussioncomment-19533418`, in garamsh/discussions) and have no unit planned yet.

The gateway is planned as the unit `gateway/`, holding the binaries `gaon-gateway-controller` and `gaon-gateway-proxy` ([ADR 0032](./adr/0032-the-gateway-is-gateway-httproute-and-tlsroute-kinds-of-their-own-group-a-gateway-unit-registers-and-runs-with-a-loadbalancer-service-and-a-deployment-of-proxies-for-each-gateway.md), [gateway.md](./gateway.md)); its folder is created by the serving issue's pull request.

The rules for units:

- **A top-level folder is one responsibility**: an executable unit, or a supporting responsibility (`lib/`, `tooling/`, `docs/`).
- **One Go module per executable unit**, its binary under `cmd/<binary>/main.go`.
- **A `go.work` at the root names every Go module**, and the root holds no Go package.
- **A library linked by more than one unit is a module of its own under `lib/`**, its package at the module root as `docs/convention/stack-go.md` §1 lays out a module that ships no binary. `lib/` holds no binary. There is no `shared/`, `utils/` or `common/`, and a package that one unit alone imports belongs to that unit.
- **mvmd's and naru's clients are their projects' own external modules** (mvmd's is `lib/runtimeapi`), required as dependencies and never copied in.
- **A unit's folder is created by its first pull request**, and not before.
- **Dependencies between projects are one-way**: gaon depends on mvmd and on naru, and neither depends on gaon.

## Boundaries and dependency direction

- **A unit is the boundary.** One folder per unit, named for what it runs or publishes and never for a technical role.
- **A unit is reached only through what it declares public.** Everything else it holds is internal, and reaching past that surface is a defect even where the language permits it.
- **A unit reaches another only through a published API.** A controller uses the public resource API, never another unit's `internal/` packages. A package both need is a library module under `lib/`.
- **Shared code sits below every unit, never beside one.** What two units both need is extracted into `lib/`, or owned by one unit and reached through its published API; it never becomes a sibling folder no unit owns.
- **A cycle between units means the split is wrong**, and so does a file that belongs in two of them.
- **A new unit is earned** by a responsibility with its own lifecycle and vocabulary, which something outside it depends on, and whose published surface can stay stable while its inside changes.

## Where the composition site lives

- **The application has one composition site.** It is the one place that constructs concrete implementations, and it decides which implementation each declared abstraction receives.
- **Every other unit receives what it depends on and constructs none of it**, whatever the shape makes the unit — a domain, a layer, or an adapter.
- **The composition site stays thin.** It constructs and passes; it decides nothing else.

Each executable unit has its own site: its `cmd/<binary>/main.go`, or the package that file calls where a test outside `cmd/<binary>/` has to reach it (`docs/convention/stack-go.md` §Project envelope).

## Decisions this rests on

- [ADR 0001](./adr/0001-the-repository-is-a-go-workspace-of-executable-units-partitioned-by-responsibility.md): the repository is a Go workspace of executable units, each its own module, partitioned by responsibility. It records the structure taken, the candidates read, and the signs that it was wrong.
- [ADR 0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md): the Node Agent's network and storage plugins are units of their own, binaries reached through the `lib/plugin` API and not compiled into `node-agent/`.

## Open questions

- **How a unit is divided inside.** `docs/convention/stack-go.md` §1 lays out a service module, and no decision here adds to it. Until one does, this binds nothing.
