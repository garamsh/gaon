# Workloads

What `gaon-workloads` does: it registers the kind `Job` and runs a Job's MicroVMs to completion. The unit is `workloads/` and its binary is `gaon-workloads`. It is a client of the public resource API through `lib/api` and imports no other unit's `internal/` packages.

Basis: [ADR 0024](./adr/0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md), [ADR 0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md) for the definition and [ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md) for `uid`, owner references and label-selector rules.

## Current decisions

### The kind

- **`Job`** of group `workloads.gaon.garam.sh`, version `v1alpha1`, plural `jobs`, cluster scoped, with a `status` subresource.
- **Spec**: `completions` (required, at least 1), `backoffLimit` (at least 0; absent is 0) and `template`, a MicroVM spec. The unit forces `runStrategy` to `Once` and clears `nodeName` and `bindingEpoch` in what it creates.
- **Status**: `active`, `succeeded`, `failed` and `conditions`. A finished Job has one True condition, `Complete` (`CompletionsReached`) or `Failed` (`BackoffLimitExceeded` or `InvalidTemplate`).

### Registration

- At start the unit reads the definition `jobs.workloads.gaon.garam.sh`: it creates it when absent, updates it at the stored `resourceVersion` when its spec differs, and writes nothing when equal. A failure to register makes the process exit with the error.
- The definition carries the label `workloads.gaon.garam.sh/managed-by=gaon-workloads`, which the unit's role names.

### The run

- **A sweep** every `-interval` (default 2s) lists the Jobs and the MicroVMs the unit labeled, and for each unfinished Job decides: complete when `succeeded >= completions`; fail when `failed > backoffLimit`; otherwise create the next MicroVM when none is active. One MicroVM is active at a time.
- **Counts are derived each sweep** from the MicroVMs a Job owns: Succeeded, Failed, and the rest as active. A status of another epoch than the spec's reads as Pending (`EffectivePhase`). The status is written only when it changed.
- **A MicroVM is owned** when its owner reference matches the Job's `apiVersion`, `kind`, `name` and `uid`. A recreated Job therefore counts none of the old one's MicroVMs.
- **A MicroVM is named `<job>-<index>`**, the first index free among the listed ones, and carries the labels `managed-by=gaon-workloads` and `workloads.gaon.garam.sh/job=<name>`.
- **A template the API refuses** (`Invalid`) fails the Job with `InvalidTemplate`, whatever the backoff limit.
- **A finished Job is left alone.** Its MicroVMs are kept until the Job is deleted, then the garbage collector removes them.

### Identity and role

- The certificate's common name is `gaon-workloads`. `workloads/deploy/rbac.yaml` holds the ClusterRole and ClusterRoleBinding `gaon:workloads`; an administrator applies it with `gaon apply -f`. Without it the unit is refused.
- The role: `create` and `update` of definitions labeled `managed-by=gaon-workloads`; `get` of definitions with no selector; `create`, `get` and `list` of MicroVMs labeled `managed-by=gaon-workloads`; `get`, `list`, `watch` of `jobs`; `update` of `jobs/status`.
- **A `get` of an absent object is judged on no labels**, so a selector rule refuses it. The unselected `get` of definitions is what lets the unit find its definition is missing. `resource-api.md` §Authorization states definitions are readable by every certificate-authenticated caller while the API server requires the RBAC `get`; the two disagree and the role is written for the server.

### Deployment

- [deployment.md](./deployment.md) does not describe this unit yet; that is held for garamsh/gaon#123.

## Rationale

- Deriving counts from the owned MicroVMs keeps the unit stateless: a restart, a lost write or a repeated sweep changes nothing.
- Matching the owner by `uid` follows ADR 0018: the `uid` is what separates an object from a later one of the same name.
- Keeping terminal MicroVMs gives the user the record of each run and leaves removal to the one component that already deletes by owner.
- Registering the kind from the unit, not from the API server, is what shows the public API is enough to add a kind with a controller (goal #117 D1).

## Open questions

- **A name held by an unlabeled MicroVM stalls the Job**: the unit cannot list it, so each sweep's create answers `AlreadyExists` and is logged. A generated name suffix would remove it.
- **Sweeping, not watching.** A reaction takes up to one interval. The kind serves `watch`.
- **No `parallelism`**: one MicroVM at a time.
- **The definition is rewritten at start** when it differs, which overwrites an operator's edit.
- **The `get` rule on definitions** follows the server and not `resource-api.md`; which is right is open.
