# ADR 0001: The repository is a Go workspace of executable units, each its own module, partitioned by responsibility

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#1. The direction is the project owner's, stated in the gaon PM session of 2026-11-11 and recorded on that issue. The precedent is garamsh/garam at `6e80e2a4f775a5ddc252884492e4540e048242d1`: its `docs/architecture/structure.md` §Services, ADR 0125 and ADR 0132.

## Context

- **gaon is several executables in one repository.** The scope is the control-plane services (API, Store, Scheduler, Controllers, Gateway, Cell Router), a Node Agent per node, and machine and cluster management as resources and controllers. The decisions are garamsh/discussions Discussion #2 comments `discussioncomment-19466471`, `discussioncomment-19466961` and `discussioncomment-19533418`.
- **Two external projects sit on gaon's boundary.** gaon calls mvmd's runtime API and naru's node API. Dependencies are one-way: gaon to mvmd and gaon to naru. mvmd does not know gaon, and naru does not know gaon's resource API.
- **Control-plane services and the Node Agent run as host services naru starts**, not inside microVMs (Discussion #3 decision 7, as recorded on garamsh/gaon#1).
- **The structure must cover** control-plane services, a per-node agent and two external client modules in one repository, so that a change lands in one folder and a unit's pull request touches only that unit.
- **Candidates read** (`docs/architecture/structures/` at `32db8744d4ff326416fbfc4d677059f29fddb220`): layered, hexagonal and domain.
  - Layered divides by technical responsibility, so one feature crosses every layer; gaon's units differ in what they run, not in a technical tier.
  - Hexagonal divides an application from its technologies. It could apply inside a unit, which this decision does not constrain, and it says nothing of how several executables relate.
  - Domain divides by subject matter, one folder per boundary reached through a declared surface, and leaves open whether a boundary may depend on a sibling.

## Decision

**The structure builds on the domain structure, with the unit as the boundary: the repository is a Go workspace in which each executable unit is a top-level folder holding one Go module, and units reach each other only through a published API.**

1. **One Go module per executable unit**, a top-level folder with its binary under `cmd/`, as `docs/convention/stack-go.md` §1 lays a service out.
2. **A `go.work` at the root names every Go module.**
3. **A unit reaches another only through what that unit publishes.** A controller uses the public resource API and never another unit's `internal/` packages. Reaching into another unit's `internal/` is a defect even where it builds. This is the side taken on the point the domain structure leaves contested, whether a boundary may depend on a sibling at all.
4. **A library linked by more than one unit is a module of its own under `lib/`.** There is no `shared/`, `utils/` or `common/`, and a package that one unit alone imports belongs to that unit.
5. **mvmd's and naru's clients are those projects' own external modules** (mvmd's: `lib/runtimeapi`), required as dependencies and not copied into this repository.
6. **A unit's folder is created by its first pull request.** Bootstrap creates none.

`docs/architecture/structure.md` carries the rules and names the planned units.

## Consequences

- A change to one unit touches one folder, and a unit's owner is the folder's owner.
- The resource API is the only coupling between control-plane units, so each can be built and tested against the library that publishes it.
- A second consumer of a package forces its extraction into `lib/` as a module, which costs a `go.mod` and a `go.work` entry.
- Every Go entry point has to cover every module `go.work` names, because no root module exists.
- Which structure applies inside a unit is not decided here: the unit's own files follow `docs/convention/stack-go.md` §1.

## Signs the decision was wrong

Carried from `docs/architecture/structures/domain.md` at `32db8744d4ff326416fbfc4d677059f29fddb220` §Signs the choice was wrong, read with the unit as the boundary.

- Most changes touch several units at once, and a pair of units that always move together cannot be released independently.
- The same fix is applied once per unit, and a unit keeps being missed.
- The shared code every unit imports (`lib/`) is growing faster than any unit.
- Which unit a new file belongs to cannot be settled without asking someone.
- A unit's public surface has grown until it names what the unit keeps inside.
