# ADR 0017: The Machine kind becomes MicroVM, short name mvm, in v1alpha1 in place

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#102, step D2 of goal garamsh/gaon#117. The decision is the project owner's, taken in the gaon PM session of 2026-11-12 and recorded on that issue.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **`Machine` reads to a `kubectl` user as Cluster API's Machine**, a server that becomes a node. In gaon the kind is the workload, and a physical machine is `Node` ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md) item 11).
- **It also collides with aurora's `machines` root**, whose guests are gaon's Nodes (issue #102, Why).
- **mvmd's runtime API keeps `Machine`** (`v1alpha2.Machine`, `CreateMachine`, machine identifiers). A gaon workload maps to one mvmd Machine, so the two names stay different on purpose (garamsh/gaon#117 D2).
- **The only consumer outside the repository is aurora**, which changes nothing until gaon publishes the rename (issue #102, Why).

## Decision

**The workload kind `Machine` of `compute.gaon.garam.sh/v1alpha1` is renamed `MicroVM`, in place. There is no `v1alpha2` and no alias.**

1. **Names:** kind `MicroVM`, plural `microvms`, CLI singular `microvm`, short name `mvm`.
2. **Paths:** `/microvms`, `/microvms/{name}`, `/microvms/{name}/status` and `/microvms/{name}/binding`. `/machines` is not served.
3. **Authorization:** the RBAC resources are `microvms`, `microvms/status` and `microvms/binding`. The bootstrap roles that named `machines` name `microvms`.
4. **Store:** the Store keys of the kind use `microvms` where they used `machines`. Nothing migrates existing keys, so a Store holding `machines` keys shows no MicroVM.
5. **Fields and wire names:** a Volume's `status.attachment.machineName` is `microvmName`, and the extender request carries the object under `microvm` where it carried `machine`.
6. **Code:** every gaon Go identifier, package, file and test name that named the kind names `MicroVM`, and the Node Agent's package `machines` is `microvms`.
7. **What keeps `Machine`:** mvmd's runtime API and everything that is mvmd's, namely `v1alpha2` identifiers, the identifiers of mvmd machines the Node Agent derives from a MicroVM's name and epoch, and `fakemvmd`.
8. **ADR 0003:** items 2, 5, 6, 7 and 11 and its Consequences name `Machine` for the workload. They stand with `MicroVM` in its place. Item 11 reads: `MicroVM` is the microVM; a physical machine is the core resource `Node`.
9. **Other ADRs:** a `Machine` in ADR 0002, 0004, 0008, 0009, 0011, 0012, 0013, 0014 or 0015 that names the workload reads `MicroVM`, and a `machine`, `machines`, `machines/status` or `machines/binding` there reads the matching `microvm` form.

## Consequences

- A client of `v1alpha1` that sends `kind: Machine` or calls `/machines` gets the answer the server gives any unknown kind or path.
- Every responsibility document under `docs/architecture/` names `MicroVM`, and cites its own headings by their new names.
- The Node Agent reads two names for one thing: a `MicroVM` of gaon and the mvmd `Machine` it is held as.
- A later kind that refers to the workload ([garamsh/gaon#117](https://github.com/garamsh/gaon/issues/117) D3 onward) names `MicroVM` from the start.

## Signs the decision was wrong

- A reader of the Node Agent mistakes an mvmd Machine for a MicroVM, or the reverse, in a change after this one.
- aurora needs the old name to keep working, and the project adds an alias or a `v1alpha2` for it.
- mvmd renames its `Machine`, and the difference between the two names stops meaning anything.
