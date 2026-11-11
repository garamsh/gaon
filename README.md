# gaon

gaon is the orchestration project of the garamsh microVM platform. It schedules and manages Machines on nodes whose runtime is mvmd. Its scope is set by the project owner's decisions in garamsh/discussions Discussion #2 (`discussioncomment-19466471`, `discussioncomment-19466961`, `discussioncomment-19533418`).

## Scope

- **API**: the resource API, with watch, `/status` and finalizers.
- **Store**: the state behind the API.
- **Scheduler**: places Machines on nodes.
- **Controllers**: reconcile resources, including machine and cluster management and multi-cluster, which gaon keeps as resources and controllers.
- **Gateway** and **Cell Router**: the request path into Machines.
- **Node Agent**: one per node, the counterpart of the kubelet. It watches, renews its Node's Lease, validates epochs, decides OnDemand wake and sleep, reports status, and is a client of mvmd's runtime API.

The control plane runs as host services that naru starts, not inside microVMs.

## Neighbouring projects

- **mvmd** is the node-local microVM runtime. It does not know gaon. gaon calls mvmd's runtime API.
- **naru** is the node OS and its install and upgrade. gaon calls naru's node API, and naru does not know gaon's resource API.
- Dependencies are one-way: gaon to mvmd and gaon to naru.
- Containers are not an execution unit anywhere in the project, development and CI included.

## Status

The first milestone, M1, is a Machine defined in YAML that runs on mvmd on a QEMU development node and runs on another node when its node dies. Its units are planned in `docs/architecture/structure.md` §Units.

## Structure

- `AGENTS.md`: the contribution contract, who may change what and the rules that apply to every contributor
- `docs/convention/`: the conventions for code, reviews and documentation; `stack-go.md` is the stack file
- `docs/architecture/`: `structure.md` (how the repository is partitioned), the ADRs, and the index of both
- `docs/procedure/`: this repository's procedures for its recurring acts
- `.github/`: PR and issue templates, and CODEOWNERS
