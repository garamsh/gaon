# Workloads

What `gaon-workloads` does: it registers the kinds `Job`, `CronJob`, `Deployment` and `StatefulSet`, runs a Job's MicroVMs to completion, creates a Job from a CronJob at each scheduled time, keeps the replicas of a Deployment and keeps the named replicas of a StatefulSet with their Volumes. The unit is `workloads/` and its binary is `gaon-workloads`. It is a client of the public resource API through `lib/api` and imports no other unit's `internal/` packages.

Basis: [ADR 0024](./adr/0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md), [ADR 0025](./adr/0025-a-cronjob-is-a-kind-the-workloads-unit-runs-by-creating-a-job-from-its-template-at-each-scheduled-time-only-while-it-leads.md) for the CronJob, [ADR 0026](./adr/0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md) for the Deployment, [ADR 0030](./adr/0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md) for the StatefulSet, [ADR 0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md) for the definition and [ADR 0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md) for `uid`, owner references and label-selector rules.

## Current decisions

### The kind

- **`Job`** of group `workloads.gaon.garam.sh`, version `v1alpha1`, plural `jobs`, cluster scoped, with a `status` subresource.
- **Spec**: `completions` (required, at least 1), `backoffLimit` (at least 0; absent is 0) and `template`, a MicroVM spec. The unit forces `runStrategy` to `Once` and clears `nodeName` and `bindingEpoch` in what it creates.
- **Status**: `active`, `succeeded`, `failed` and `conditions`. A finished Job has one True condition, `Complete` (`CompletionsReached`) or `Failed` (`BackoffLimitExceeded` or `InvalidTemplate`).

- **`CronJob`** of the same group and version, plural `cronjobs`, cluster scoped, with a `status` subresource.
- **Spec**: `schedule` (required), five numeric fields, minute, hour, day of month, month, day of week, in UTC; each is `*`, a number, a range `a-b` (a range whose start is above its end wraps), a list, or a step `/n` after `*` or a range; 7 is Sunday; with both day fields restricted a day matching either counts. `jobTemplate` (required), a Job spec. `historyLimit` (at least 1; absent is 3).
- **Status**: `lastScheduleTime`, the latest time dealt with.
- **The schedule is validated by the schema's `pattern`**, which the API server applies on create and update and which names `spec.schedule` in the refusal. The parser the unit runs and the pattern are built from one table.

- **`Deployment`** of the same group and version, plural `deployments`, cluster scoped, with a `status` subresource.
- **Spec**: `replicas` (required, at least 0), `selector.matchLabels` (required, not empty), `template` (required, a MicroVM spec; the unit forces `runStrategy` to `Always` and clears `nodeName` and `bindingEpoch`) and `strategy`.
- **Strategy**: `type` is `RollingUpdate` (absent is this) or `Recreate`. `rollingUpdate.maxSurge` and `maxUnavailable` are each a whole number or a whole number of percent, **written as a string** (`"1"`, `"25%"`) and checked by the schema's `pattern`, because the schema subset has no int-or-string; each defaults to `25%`. A surge percentage rounds up, an unavailability percentage rounds down, and when both are 0 one replica may be unavailable. `Recreate` is a surge of 0 and an unavailability of `replicas`.
- **Status**: `observedGeneration` (the object's `metadata.generation` when the counts were taken), `replicas`, `readyReplicas`, `updatedReplicas`, `availableReplicas`. They count the owned MicroVMs that are not being deleted and have not failed; `readyReplicas` those whose Ready condition is True; `updatedReplicas` those made from the current template; `availableReplicas` equals `readyReplicas`, there being no minimum ready time. The counts are taken before the sweep's step, so the status is a sweep behind.

- **`StatefulSet`** of the same group and version, plural `statefulsets`, cluster scoped, with a `status` subresource.
- **Spec**: `replicas` (required, at least 0), `selector.matchLabels` (required, not empty), `template` (required, a MicroVM spec; the unit forces `runStrategy` to `Always` and clears `nodeName` and `bindingEpoch`), `volumeClaimTemplates`, `podManagementPolicy` and `updateStrategy`.
- **Claim templates**: each is a `name` and a Volume `spec` (`storageClass` required). The entry of `template.volumes` of the same name is the mount. A name must be set, unique and mounted by such an entry.
- **Policy and strategy**: `podManagementPolicy` is `OrderedReady` (absent is this) or `Parallel`. `updateStrategy.type` is `RollingUpdate` (absent is this) or `OnDelete`; `rollingUpdate.partition` is at least 0 and absent is 0.
- **Status**: `observedGeneration`, `replicas`, `readyReplicas` and `updatedReplicas`, counted as a Deployment's are.

### Registration

- At start the unit reads the definitions `jobs.workloads.gaon.garam.sh`, `cronjobs.workloads.gaon.garam.sh`, `deployments.workloads.gaon.garam.sh` and `statefulsets.workloads.gaon.garam.sh`, each in turn: it creates it when absent, updates it at the stored `resourceVersion` when its spec differs, and writes nothing when equal. A failure to register makes the process exit with the error.
- The definition carries the label `workloads.gaon.garam.sh/managed-by=gaon-workloads`, which the unit's role names.

### The run

- **A sweep** every `-interval` (default 2s) lists the Jobs and the MicroVMs the unit labeled, and for each unfinished Job decides: complete when `succeeded >= completions`; fail when `failed > backoffLimit`; otherwise create the next MicroVM when none is active. One MicroVM is active at a time.
- **Counts are derived each sweep** from the MicroVMs a Job owns: Succeeded, Failed, and the rest as active. A status of another epoch than the spec's reads as Pending (`EffectivePhase`). The status is written only when it changed.
- **A MicroVM is owned** when its owner reference matches the Job's `apiVersion`, `kind`, `name` and `uid`. A recreated Job therefore counts none of the old one's MicroVMs.
- **A MicroVM is named `<job>-<index>`**, the first index free among the listed ones, and carries the labels `managed-by=gaon-workloads` and `workloads.gaon.garam.sh/job=<name>`.
- **A template the API refuses** (`Invalid`) fails the Job with `InvalidTemplate`, whatever the backoff limit.
- **A finished Job is left alone.** Its MicroVMs are kept until the Job is deleted, then the garbage collector removes them.

### The schedule

- **Each sweep** also lists the CronJobs. A CronJob with no `lastScheduleTime` gets it set to now and runs nothing that sweep. Otherwise the unit takes the latest scheduled time after `lastScheduleTime` and up to now, read from the `Clock`.
- **Missed runs are skipped.** Of the times that passed (a restart, a term without a leader) only the latest runs, once; the others are not owed.
- **Concurrency is Forbid.** When a Job the CronJob owns (by `apiVersion`, `kind`, `name` and `uid`, not being deleted) is unfinished, the time is skipped and logged and is not owed later. Another CronJob's Jobs do not count.
- **A Job is named `<cronjob>-<unix minute>`**, owned by the CronJob with its `uid`, with the labels `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/cronjob=<name>` and `workloads.gaon.garam.sh/scheduled-minute=<unix minute>`; its spec is the template. `AlreadyExists` counts as created, which is what makes a time run once when two instances race or a creator stopped before writing the status.
- **History.** After a run, the finished Jobs the CronJob owns beyond `historyLimit` are deleted, oldest scheduled minute first. Unfinished Jobs and other CronJobs' Jobs are kept.
- **A schedule the unit cannot read** runs nothing and is logged; the other CronJobs are unaffected.

### The replicas

- **A replica is a MicroVM the Deployment owns** (by `apiVersion`, `kind`, `name` and `uid`), created by the unit as a client of the public API and named `<deployment>-<index>`, the first free index. It carries the labels `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/deployment=<name>`, `workloads.gaon.garam.sh/template-hash=<hash>` and the selector's labels. The hash is the first ten hexadecimal characters of the SHA-256 of the replica template as JSON. There is no ReplicaSet kind: the label is what tells an updated replica from another.
- **Ready** is the Ready condition True in a status of the spec's epoch. **Failed** is the effective phase Failed.
- **Each sweep takes one step** per Deployment, from the MicroVMs listed: it deletes the Failed replicas; it deletes the replicas above `replicas`, or above `replicas + maxSurge` while a replica of another template is present (not ready first, then of another template, then the highest names); it deletes the replicas of another template that are not ready, and those that are ready while that leaves at least `replicas - maxUnavailable` Ready; it creates replicas of the current template up to `replicas` while at most `replicas + maxSurge` MicroVMs exist.
- **The bounds hold at every write.** The MicroVMs being deleted and those failed count toward the surge until they are gone, and only the Ready ones not being deleted count toward availability. A test drives a rollout under several bounds and checks both after every write. `Recreate` has no surge, so no replica of the new template is created until every old one is gone.
- **An unusable Deployment** (an empty selector, a strategy the schema should have refused, a spec that does not decode) does nothing and is logged; the others are served.
- **Scaling, rollouts and deleted replicas survive a handover**: every decision is read from the Store and the names are deterministic, so the new holder continues from what exists, and a create the old holder got in last answers `AlreadyExists`.

### The stateful replicas

- **A replica is a MicroVM the StatefulSet owns** (by `apiVersion`, `kind`, `name` and `uid`) named `<statefulset>-<ordinal>`, for the ordinals 0 to `replicas - 1`. It carries the labels `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/statefulset=<name>`, `workloads.gaon.garam.sh/ordinal=<ordinal>`, `workloads.gaon.garam.sh/replica-name=<name>`, `workloads.gaon.garam.sh/template-hash=<hash>` and the selector's labels; the ordinal and the name are its stable identity. A MicroVM the set owns whose name is not `<statefulset>-<n>` with `n` written without sign or leading zero is not a replica and is left alone.
- **A Volume per claim template and replica**, named `<claim>-<statefulset>-<ordinal>`, which the replica mounts through the template's volume entry of the claim's name, renamed to it. The unit creates a Volume, labeled `managed-by=gaon-workloads` and `workloads.gaon.garam.sh/statefulset=<name>`, before the replica, only when none of its name is listed. **It never deletes a Volume and gives it no owner reference**, so a replacement, a scale-down and the deletion of the StatefulSet all leave it.
- **A replacement has the name and the Volume of the replica it replaces.** A replica is missing when no MicroVM of its name exists, so nothing is created while the old one is being deleted and its node agent has not released it. A Failed replica is deleted and made again.
- **`OrderedReady`** creates the lowest missing ordinal, one per sweep, only while every lower ordinal exists and is Ready. It deletes the highest ordinal above `replicas`, one per sweep, the next once it is gone.
- **`Parallel`** creates every missing ordinal and deletes every ordinal above `replicas` in one sweep.
- **`OnDelete`** replaces nothing; a replica a user deletes is made again from the current template.
- **`RollingUpdate`** deletes the highest ordinal from `partition` up that is not of the current template, and only while every other ordinal exists, is Ready and is not being deleted. It acts only when no ordinal is missing or above `replicas`. Ordinals below `partition` are not replaced, but one that is deleted is made from the current template.
- **An unusable StatefulSet** (an empty selector, a policy, type or partition the schema should have refused, a claim template mounted by no volume or named twice, a spec that does not decode) does nothing and is logged; the others are served.
- **An ordered creation or a rollout survives a handover**: every decision is read from the Store and the names are deterministic.

### Election

- **It acts only while it holds the coordination Lease `gaon-workloads`**, as `gaon-scheduler` and `gaon-controllers` do ([ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)): it runs on each control-plane Node and the others poll the Lease and do nothing else, not even registering the kind. Each term, from taking the Lease to losing it, runs a new service that registers and sweeps from the Store's state; a term that ends cancels the sweep.
- **The flags are `lib/api`'s**, `--leader-elect` and the five `--leader-election-*` flags with the defaults of [deployment.md](./deployment.md); the name defaults to `gaon-workloads`. The Lease is on the server `-server` names. `-leader-elect=false` is for one instance.
- **No fencing beyond the margin.** A MicroVM the old holder created just before its term ended may reach the Store after the new holder listed. The new holder then creates the same name, which answers `AlreadyExists` and is logged, and its next sweep counts the old holder's MicroVM.

### Identity and role

- The certificate's common name is `gaon-workloads`. `workloads/deploy/rbac.yaml` holds the ClusterRole and ClusterRoleBinding `gaon:workloads`; an administrator applies it with `gaon apply -f`. Without it the unit is refused.
- The role: `create` and `update` of definitions labeled `managed-by=gaon-workloads`; `get` of definitions with no selector; `create`, `get`, `list` and `delete` of MicroVMs labeled `managed-by=gaon-workloads`; `get`, `list`, `watch` of `jobs`; `create` and `delete` of `jobs` labeled `managed-by=gaon-workloads`; `update` of `jobs/status`; `list` of `cronjobs` and `update` of `cronjobs/status`; `list` of `deployments` and `update` of `deployments/status`; `list` of `statefulsets` and `update` of `statefulsets/status`; `create` and `list` of Volumes labeled `managed-by=gaon-workloads`, and no `delete`; `get`, `create` and `update` of `leases` of the coordination group, which a rule cannot narrow to the Lease `gaon-workloads` because a rule names no object and a `get` of an absent Lease is judged on no labels.
- **A `get` of an absent object is judged on no labels**, so a selector rule refuses it. The unselected `get` of definitions is what lets the unit find its definition is missing. `resource-api.md` §Authorization allows only what the Node authorizer or a bound role allows under `RBAC`, so the server's requirement of the RBAC `get` is what the document states.

### Deployment

- [deployment.md](./deployment.md) lists the unit on each control-plane Node, with its flags and its certificate; the manifest is applied once for the cluster.

## Rationale

- Deriving counts from the owned MicroVMs keeps the unit stateless: a restart, a lost write or a repeated sweep changes nothing.
- Matching the owner by `uid` follows ADR 0018: the `uid` is what separates an object from a later one of the same name.
- Keeping terminal MicroVMs gives the user the record of each run and leaves removal to the one component that already deletes by owner.
- Registering the kind from the unit, not from the API server, is what shows the public API is enough to add a kind with a controller (goal #117 D1).

- Keeping no ReplicaSet kind leaves one list and one pass for a rollout; the cost is no rollout history and no rollback ([ADR 0026](./adr/0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md)).

- Keeping the Volumes and the names of a StatefulSet's replicas through a replacement is what a stateful workload needs; the cost is that the Volumes outlive the set until a user deletes them ([ADR 0030](./adr/0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md)).

## Open questions

- **A name held by an unlabeled MicroVM stalls the Job**: the unit cannot list it, so each sweep's create answers `AlreadyExists` and is logged. A generated name suffix would remove it.
- **Sweeping, not watching.** A reaction takes up to one interval. The kind serves `watch`.
- **No `parallelism`**: one MicroVM at a time.
- **A CronJob has one policy**: Forbid, with skip and no catch-up. `Allow`, `Replace`, a catch-up window and a time zone are not offered.
- **A new CronJob waits** up to a sweep for its first sight, then up to a minute for its first run.
- **Clocks at a handover.** A name per time keeps a time from running twice, not from running early or late when two instances' clocks differ.
- **The definition is rewritten at start** when it differs, which overwrites an operator's edit.
- **A bound is a string.** `maxSurge: 1` is refused by the schema; a user writes `"1"`. An int-or-string type in the `lib/api` schema subset would remove it.
- **No progress deadline, pause or rollback.** A template that never becomes ready holds a rollout, with its surge replica kept, until the spec changes.
- **No minimum ready time**: a replica is available as soon as it is Ready.
- **A selector change adopts and releases nothing**; ownership is by owner reference.
- **A StatefulSet keeps no revision**, so a replica below `partition` that is deleted is made from the current template, and a rollout waits on any replica that is not Ready.
- **A StatefulSet's scale-down does not wait for the lower ordinals to be Ready.**
- **A StatefulSet's Volumes are never deleted by the unit**; a user removes them.
- **No DNS**: the identity is in labels; nothing serves the names.
