# ADR 0024: A Job is a custom resource kind the workloads unit registers and runs to completion as a client of the public API

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#146, goal garamsh/gaon#117, decisions D1 and D8.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A MicroVM with `runStrategy: Once` runs once and ends, but nothing starts the next one.** A user who wants "run this to completion, retry a failure, stop after N successes" writes that loop by hand.
- **The resource API already lets a kind be added without a change to the API server.** [ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md) serves a registered kind under its own group, and a custom resource can carry a `status` subresource and `ownerReferences` ([ADR 0018](./0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md)). The garbage collector already deletes what an absent owner leaves.
- **Whether that surface is enough is the question goal #117 D1 asks.** A workload kind built only on it shows what the public API lacks; one built on the API server's internals would hide it.
- **A role rule can carry a label selector** (ADR 0018, Decision 8), so a client can be limited to the objects it labels.

## Decision

**A new unit `workloads/`, binary `gaon-workloads`, registers the kind `Job` of group `workloads.gaon.garam.sh`, version `v1alpha1`, as a CustomResourceDefinition at start, and runs each Job's MicroVMs to completion. It uses `lib/api` only, holds its own certificate and has its own role.**

1. **The kind.** `Job`, plural `jobs`, cluster scoped, with a `status` subresource.
   - `spec.completions` (required, at least 1): the successful MicroVMs that complete the Job.
   - `spec.backoffLimit` (at least 0, 0 when absent): the failed MicroVMs the Job tolerates. The Job fails when failures exceed it.
   - `spec.template`: a MicroVM spec. The unit sets `runStrategy` to `Once` and clears `nodeName` and `bindingEpoch`.
   - `status.active`, `succeeded`, `failed` and `conditions`. A condition is of type `Complete` or `Failed`, with reason `CompletionsReached`, `BackoffLimitExceeded` or `InvalidTemplate`.
2. **The run.** A sweep every `-interval` lists the Jobs and the MicroVMs, counts the MicroVMs each Job owns from their phases (a status of another epoch counts as Pending), writes the counts, and creates the next MicroVM when the Job is unfinished and none is active. One MicroVM is active at a time. A Job with a condition is left alone. A template the API refuses with `Invalid` fails the Job with `InvalidTemplate`. The counts are derived from the MicroVMs every sweep, so the unit keeps no state.
3. **Ownership.** A MicroVM is named `<job>-<index>` and carries an owner reference to the Job with its `uid`, plus the labels `workloads.gaon.garam.sh/managed-by=gaon-workloads` and `workloads.gaon.garam.sh/job=<name>`. A MicroVM counts for a Job only when the reference matches the Job's `apiVersion`, `kind`, `name` and `uid`, so a recreated Job does not take over the old one's MicroVMs. Terminal MicroVMs are kept; the garbage collector removes them with the Job.
4. **Registration.** At start the unit reads the definition `jobs.workloads.gaon.garam.sh`. Absent, it creates it; present with a different spec, it updates it at the stored `resourceVersion`; equal, it writes nothing. A failure to register ends the process with an error. The definition carries the label `managed-by=gaon-workloads`.
5. **Identity, role and binding.** The certificate's common name is `gaon-workloads`. The ClusterRole and ClusterRoleBinding `gaon:workloads` are a manifest, `workloads/deploy/rbac.yaml`, that an administrator applies with `gaon apply -f`; the API server's bootstrap does not write them. The role gives:
   - `create` and `update` of definitions that carry the unit's label, and `get` of definitions without a selector (a `get` of an absent object is judged on no labels, so a selector rule refuses it);
   - `create`, `get` and `list` of MicroVMs that carry the unit's label, the list asked with that selector;
   - `get`, `list` and `watch` of `jobs` and `update` of `jobs/status`.
6. **The unit's own folder is earned** under [structure.md](../structure.md) §Boundaries: it has its own lifecycle and vocabulary (a kind, its status and conditions); a user depends on it for the kind to exist; its surface, the kind's schema, stays stable while the controller behind it changes; and it needs its own binary and identity, since a certificate that may create MicroVMs is not one `gaon-controllers` should hold. Its being outside `controllers/` is also what shows the public API is enough to build a workload kind (D1).

## Consequences

- **Run-to-completion is a kind a cluster has only where an administrator runs `gaon-workloads` and applies its manifest.** The API server and the controllers are unchanged.
- **A name taken by a MicroVM the unit did not create stalls the Job.** The index is the first free among the MicroVMs the unit can list, which are the labeled ones; a create then answers `AlreadyExists` on each sweep and is logged. The Job does not fail. The unit's role cannot list unlabeled MicroVMs.
- **The role grants no `list` or `watch` of definitions.** The unit finds its definition by name only.
- **Sweeping, not watching.** A Job's reaction takes up to one interval and a sweep's time. A watch is possible with the `watch` verb the kind already has.
- **One MicroVM at a time.** A Job with `completions` N takes N runs in sequence; there is no `parallelism`.
- **`resource-api.md` §Authorization says definitions are readable by every certificate-authenticated caller; the API server requires the RBAC verb `get`.** The role carries the `get` rule for that reason. Reconciling the document and the server is not decided here.
- [deployment.md](../deployment.md) does not yet list the unit; its place there is for garamsh/gaon#123.

## Signs the decision was wrong

- A second workload kind needs the same registration and ownership code, and two units copy it where a library under `lib/` was wanted.
- Users meet a stalled Job from a name collision often enough that the unit should name MicroVMs by a generated suffix.
- The sweep's delay is felt, and a watch with a resync is wanted.
- Registration at start is found to fight an operator's edit of the definition on each restart.
- The public API is found to lack something the unit needed, and the unit had to reach past it.

## Rejected alternatives

- **The Job controller inside `gaon-controllers`.** It shares the controllers' certificate, which can delete every resource (ADR 0021), and it would not show that the public API is enough.
- **Bootstrap writes the unit's role in the API server.** It couples the API server to a unit that may not run and puts a role for a client in the server's code. A manifest follows what `gaon apply` already does.
- **A built-in `Job` kind in the compute group.** It changes the API server and the reserved groups for a kind that ADR 0009 already lets a unit add.
- **Counting in `status` and incrementing.** A lost or repeated write changes the count; deriving it from the MicroVMs each sweep cannot drift.
