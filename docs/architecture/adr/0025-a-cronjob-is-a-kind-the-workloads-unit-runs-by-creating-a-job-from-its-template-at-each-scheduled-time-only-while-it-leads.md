# ADR 0025: A CronJob is a kind the workloads unit runs by creating a Job from its template at each scheduled time, only while it leads

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#155.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A Job runs once, when a user creates it.** Work that repeats ("take a snapshot each night") is a user writing the loop, or a person running `gaon apply` by hand.
- **The workloads unit already registers a kind, runs it and acts only while it holds a Lease** ([ADR 0024](./0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md), [ADR 0023](./0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)). A second kind is the same mechanism.
- **The API server validates a custom resource against its definition's schema** ([ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md)), including a `pattern` on a string, and names the field in a refusal. It has no admission step the unit could add for a schedule.
- **`metadata` has no creation time**, so the unit cannot tell when it first saw a CronJob from the object alone.
- **A scheduled time can pass while no instance leads**, and two instances can overlap for a moment at a handover (ADR 0023, no fencing beyond the margin).

## Decision

**The unit registers a second kind, `CronJob`, and at each scheduled time creates a Job from the CronJob's template, named by the time, owned by the CronJob. Only the Lease holder does it.**

1. **The kind.** `CronJob`, plural `cronjobs`, group `workloads.gaon.garam.sh`, version `v1alpha1`, cluster scoped, with a `status` subresource.
   - `spec.schedule` (required): five space-separated fields, minute, hour, day of month, month, day of week. A field is `*`, a number, a range `a-b`, a list of these, and a step `/n` after `*` or a range. Names and `@` macros are not accepted. A day of week of 7 is Sunday. A range whose start is above its end wraps. When both the day of month and the day of week are restricted, a day matching either counts. Times are UTC.
   - `spec.jobTemplate` (required): a Job spec, the same schema as `Job.spec`.
   - `spec.historyLimit` (at least 1; absent is 3): the finished Jobs of this CronJob kept.
   - `status.lastScheduleTime`: the latest time dealt with.
2. **Validation is the schema's.** `spec.schedule` carries a `pattern` that accepts only what the parser accepts, with each field's bounds in it, and `historyLimit` has a minimum. The API server refuses a create or an update with `spec.schedule does not match the pattern ...`, naming the field. The parser and the pattern are one table; a test runs the same expressions through both.
3. **Time is read through the unit's `Clock`.** The command gives the service `systemclock.Clock`; tests give a fake.
4. **The run.** In each sweep, for each CronJob the unit can read:
   - On first sight (no `lastScheduleTime`) it writes `lastScheduleTime` as now and creates nothing, so the minute that began before the CronJob was seen is not run.
   - Otherwise it asks for the latest scheduled time in the interval after `lastScheduleTime` up to now. When there is none it does nothing.
   - **Missed runs are skipped, not caught up.** However many times passed, one Job is created, for the latest. A restart, a lost term or a long sweep does not make the unit start a burst.
   - **Concurrency is Forbid.** When a Job the CronJob owns is unfinished, the time is skipped and logged, and is not owed later. A Job that is being deleted does not count.
   - Otherwise it creates the Job `<cronjob>-<unix minute>` with an owner reference to the CronJob and its `uid`, the labels `managed-by=gaon-workloads`, `workloads.gaon.garam.sh/cronjob=<name>` and `workloads.gaon.garam.sh/scheduled-minute=<unix minute>`, and the template as its spec. `AlreadyExists` counts as created.
   - It then writes `lastScheduleTime` and trims.
5. **Exactly once per time.** The Job's name is the time, so two creators for one time cannot both succeed: the second gets `AlreadyExists`, which the unit treats as done. Together with the leader-only rule this gives one Job per scheduled time across instances and across a handover, which a test shows with two instances and a takeover.
6. **History.** After a run the unit lists the Jobs the CronJob owns, and deletes the oldest finished ones (by scheduled minute) beyond `historyLimit`, tolerating `NotFound`. An unfinished Job and a Job of another CronJob are never deleted; the garbage collector removes a Job's MicroVMs.
7. **A schedule the unit cannot read** (a CronJob stored before the pattern, or by a path that skipped it) runs nothing, is logged, and does not stop the other CronJobs.
8. **Election and role.** The unit is leader-only as in ADR 0023, so a non-leader reads nothing and writes nothing. The role `gaon:workloads` gains `create` and `delete` of `jobs` carrying the unit's label (the Jobs it makes), `list` of `cronjobs` and `update` of `cronjobs/status`.

## Consequences

- **A CronJob created without the unit running does nothing** until a leader runs; the first sweep then treats it as first sight.
- **The time of first sight is the unit's clock at that sweep**, so a CronJob can wait up to a sweep plus a minute for its first run.
- **Skipping loses runs by design.** A user who needs each time run gets a gap after downtime. The status shows the latest time dealt with, not whether it ran or was skipped; the log says.
- **Two clocks can disagree at a handover.** A new leader whose clock is behind the old one's `lastScheduleTime` creates nothing until it catches up; one ahead may run a time early. Names make a time run once, not at the right moment.
- **A scheduled-minute label is Unix minutes**, so a Job's schedule is read by arithmetic, not by a timestamp.
- **Time zones, names (`jan`, `mon`) and `@daily` are not supported.** A user writes the five numeric fields in UTC.
- **A pattern is a long regular expression** in the definition, readable only in the code's table.
- [deployment.md](../deployment.md) is unchanged: the unit, its flags and certificate are the same.

## Signs the decision was wrong

- Users lose runs they needed to the skip and ask for a catch-up window, or for `Allow` and `Replace` as policies.
- Users need a zone and the UTC fields are worked around by hand.
- A run is skipped because an old Job is stuck unfinished, and the user cannot tell why without the log: a status condition is wanted.
- The pattern drifts from the parser and a stored schedule is accepted and not run.

## Rejected alternatives

- **Validate in the unit and write a condition.** The create succeeds and the refusal comes later and elsewhere; the issue asks for refusal on create and update with the field named.
- **Catch up every missed time.** A restart after a day of downtime starts a day of Jobs at once.
- **Allow concurrent runs or replace the running one.** Each needs a policy field and a rule for stopping a Job; Forbid needs neither, and the schema can gain a policy field later.
- **Store the created Job names in the status.** A lost status write then loses the record; the deterministic name makes the Store the record.
- **A watch and a timer per CronJob.** The unit already sweeps on an interval; a minute's resolution needs no more.
