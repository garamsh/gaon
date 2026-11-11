# ADR 0002: gaon owns the Node Agent and depends one way on mvmd and naru, through their APIs

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#2. The direction is the project owner's, stated in the garamsh/discussions Discussion #2 replies cited below. The fencing contract of mvmd is read at garamsh/mvmd commit `2288ea94810b8151abece78b47f2618db2a645d1`: `docs/architecture/runtime-api.md` §Fencing deadline and its ADR 0003.

## Context

- **Four replies bear on the boundary**, all by the project owner:
  - [Decision reply, 2026-11-08](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19466471): the direction is adopted, the Docker ecosystem is left, and the languages are set (Node Agent and VMM Driver in Go, Guest Agent in Rust). It gave mvmd the Node Agent and everything beneath it, and made gaon's resource API the only contract between the projects.
  - [Supplementary reply, 2026-11-08](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19466927): added a node data-plane API owned by mvmd and called directly by gaon's Gateway and Cell Router.
  - [Decision change, 2026-11-08](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19466961): the current boundary.
  - [naru decision, 2026-11-11](https://github.com/garamsh/discussions/discussions/2#discussioncomment-19533418): gaon is also a client of the node OS project naru, and its scope grows to machine and cluster management.
- **The decision change supersedes** the mvmd scope and the contract item of the decision reply (mvmd owns the Node Agent; the resource API is the only contract; no separate runtime API) and the node data-plane API ownership of the supplementary reply. The direction, the Docker-ecosystem decision and the languages of the decision reply stand.
- **Fencing is the reason a deadline crosses the boundary.** mvmd keeps VMs running when the process that drives it dies, so a Machine rebound elsewhere could run on two nodes (invariant 1 in [resource-api.md](../resource-api.md) §Invariants). In mvmd at the commit above, the client renews one local deadline for the node, a Machine runs at most δ past mvmd's recorded expiry, and that expiry is never later than the client's acknowledgement time plus the renewed length on the client's monotonic clock. δ is not yet measured.

## Decision

**gaon owns the Node Agent, machine and cluster management, and the resource API; mvmd and naru own what they declare; gaon depends on each of them through one API and neither depends on gaon.**

1. **gaon owns:** the API server, the Store, the Scheduler, the controllers, the Gateway, the Cell Router, machine and cluster management as resources and controllers, and the Node Agent. The Node Agent watches, renews the node's Lease, checks epochs, decides OnDemand wake and suspend, and reports status. Demand signals from the Gateway and the Cell Router go to the Node Agent, which decides; mvmd executes what it is asked.
2. **mvmd owns** the node-local microVM runtime and its runtime API: Machine lifecycle, disks, resources, queries and events, exec, file API, console, image pull and conversion, snapshots, VMM drivers and the Guest Agent. It does not know gaon. Its runtime API is the only contract between the projects, defined and versioned by mvmd (`v1alpha2` at the commit above, one alpha version served at a time).
3. **naru owns** the node OS, its installation and upgrade, and the guest kernel. It does not know gaon's resource API. It starts gaon's control-plane services as host services, and the Node Agent and mvmd as configured services. gaon calls naru's node API (apply config, upgrade, reset, status), which is naru's to define; its shape is not decided.
4. **Dependencies are one-way:** gaon to mvmd and gaon to naru. The two cross-project contracts are mvmd's runtime API and naru's node API.
5. **Fencing obligation on the Node Agent.** It renews mvmd's deadline only so that mvmd's recorded expiry plus δ is no later than the end of the node's Lease validity, which is what keeps invariant 1 when the Node Agent dies. The Lease and how its validity is measured are in [resource-api.md](../resource-api.md) §Lease and fencing. δ is a parameter, not a figure, until mvmd measures it.

## Consequences

- The Node Agent's code and its responsibility document land with its unit (issue 7), not here.
- The Node Agent depends on mvmd's client module, required as an external module (`docs/architecture/structure.md` §Units); a change of mvmd's alpha version is a change to that dependency.
- A Node Agent that cannot renew the Lease or reach the Store lets mvmd's deadline lapse, and mvmd then stops the node's Machines. The Lease duration, the deadline length and δ are therefore coupled parameters, and none has a value yet.
- The route by which exec, file and console traffic reaches a node is not decided here.
- Where an item of the two superseded replies is cited elsewhere in the repository, this ADR is the record that it no longer holds.

## Signs the decision was wrong

- A Machine is observed running on two nodes after a Node Agent failure, with the obligation in Decision 5 held.
- The Lease duration cannot be set both long enough to survive ordinary Store latency and short enough that the deadline length stays positive after subtracting δ.
- gaon keeps needing a runtime operation mvmd's API cannot express, and the answer is each time a request to mvmd rather than a change here.
- naru's node API grows to carry gaon resources, so naru learns gaon's resource model.
