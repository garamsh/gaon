# ADR 0044: The PostgreSQL operator is a unit of gaon that runs each instance as a MicroVM with a local Volume, fences by deleting the old primary and backs up with pgBackRest under a ServiceAccount token

> Status: proposed
> Date: 2026-11-13

Drafted for garamsh/gaon#298, part of garamsh/gaon#156.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **What is asked.** #156 asks for PostgreSQL 17 and 18 with replication, failover, a stable read-write and read-only address, backup to S3, restore to a point in time, upgrades, credentials as Secrets, status, and survival of a Node's loss. D13 of #117 fixes the shape: a separate unit with its own binary, image and CustomResourceDefinitions, built on `lib/api`, the stateful kind (D8) and Volumes (D7) only, changing nothing in the core.
- **The model.** CloudNativePG is the reference. Its design assumes what Kubernetes has and gaon does not: spread and anti-affinity, a volume that follows a pod, a `kubectl exec`-free status channel, and a Service selector that follows a label.
- **What the core gives.** A `local` Volume is pinned to one Node at its first binding and a MicroVM with it is never rebound elsewhere ([resource-api.md](../resource-api.md) §Volume "Pinning"; [scheduler.md](../scheduler.md) §Rebinding). Invariant 1 holds that a MicroVM runs on at most one Node at a time, by the Node Agent's fencing obligation ([resource-api.md](../resource-api.md) §Lease and fencing, §Invariants). A MicroVM's spec is replaced, not edited ([resource-api.md](../resource-api.md) §MicroVM "Mutable"). The Scheduler reads no spread and no affinity ([scheduler.md](../scheduler.md) §Placement rule).
- **Three of the choices below are not obvious from the core:** whether the StatefulSet kind serves, how an old primary is stopped, and where a base backup runs.
- **Backups leave the cluster under a token, not a key.** D13 and the status of 2026-11-13 on #156 (issuecomment-6851861982) fix AWS OIDC federation with a gaon ServiceAccount token: the token file at a fixed path, `sub` `gaon:serviceaccount:<name>`, and the agent's `--service-account-audience` ([resource-api.md](../resource-api.md) §Secret, ConfigMap and ServiceAccount; [node-agent.md](../node-agent.md) §MicroVMs).
- **Volume mounts are assumed.** A Volume is mounted at its `mountPath` (garamsh/gaon#299, first in the node-agent role's order) and is writable by the entrypoint's non-root user (garamsh/mvmd#648). This ADR designs on both and does not design around their absence; [postgres.md](../postgres.md) §What the operator needs of the core lists them.
- **Third-party facts.** Each is marked verified where this ADR's author read it on 2026-11-13, and unverified where it is from memory or not stated by the source read.

## Decision

**The operator is the unit `postgres/`, a client of the public API through `lib/api` with its own certificate and role. It registers the group `postgres.gaon.garam.sh` with two kinds, `PostgresCluster` and `PostgresBackup`. For each cluster it runs every instance as a MicroVM it owns, with one `local` Volume each, on a different Node. Instances are PostgreSQL with a manager process and pgBackRest in one image per major version. The operator reads instance state by exec of the manager, decides failover itself, fences the old primary by deleting its MicroVM and, for a lost Node, waiting for the confirmed Lease expiry, and moves the read-write and read-only Services by writing their Endpoints. Backup is pgBackRest to S3 under `AssumeRoleWithWebIdentity` with the cluster's ServiceAccount token.**

1. **Unit and names.**
   - Directory `postgres/`, module `github.com/garamsh/gaon/postgres`.
   - Binary `gaon-postgres-operator`, run on each control-plane Node and acting only while it holds the coordination Lease `gaon-postgres-operator`, as `gaon-workloads` does ([workloads.md](../workloads.md) §Election).
   - Binary `gaon-postgres-manager`, the process each instance runs.
   - Image `gaon/postgres`, one per major, in ECR account 486152169996, region ap-northeast-2, tagged `<major>-<version>`. A release is the git tag `postgres/v0.1.0-dev.N`, `-rc.N` or none, which builds both majors. Only the instance image is an image; the operator is a binary, as the gateway's controller is.
   - Certificate common name and ClusterRole `gaon-postgres-operator`, in `postgres/deploy/rbac.yaml`.
2. **Group, kinds and fields.** `postgres.gaon.garam.sh/v1alpha1`, cluster-scoped, single version. `PostgresCluster` holds the version (by image digest), instance count, storage, replication mode, failover timings, parameters, databases, roles, backup and bootstrap. `PostgresBackup` is the record of one base backup, made by the schedule or by a user. [postgres.md](../postgres.md) §The kinds states the fields.
3. **How instances run.** MicroVMs the operator owns, named `<cluster>-<n>` with `n` never reused, each with a Volume of the same name, created before it, and a `nodeSelector` on a Node label the operator picks per instance so that no two instances of a cluster share a Node.
4. **Replication.** Streaming, asynchronous by default, synchronous per cluster (`ANY n` of the standbys). A new standby is seeded by `pg_basebackup` from the primary.
5. **Failover and fencing.** The operator promotes the most advanced reachable standby after the primary has been fenced. Fencing is, in order: the operator's decision is written to the cluster's status and to a topology file first; the old primary's MicroVM is deleted; the operator waits until it is gone from the Store, or, if its Node cannot answer, until the Node's `LeaseExpired` is `True`.
6. **Services.** `<cluster>-rw` and `<cluster>-ro` are Services with no selector, and the operator writes their Endpoints.
7. **Backup.** pgBackRest. WAL goes by `archive-push`, full, differential and incremental backups by `backup`, run in the primary's MicroVM by exec. The operator evaluates the schedule and keeps the `PostgresBackup` records.
8. **Restore.** A new cluster with `spec.bootstrap.recovery` restores from a prefix of the bucket to the latest point or to a time, and archives to a prefix of its own.
9. **Credentials to AWS.** Each cluster has a ServiceAccount `postgres-<cluster>` the operator creates. The instances' pgBackRest uses `AssumeRoleWithWebIdentity`. aurora holds the bucket, the OIDC provider and one role per cluster, and publishes the issuer's two documents ([postgres.md](../postgres.md) §R5 Backup).
10. **Upgrades.** A minor version is a change of image digest, rolled through the standbys and then a switchover. A major version is a new cluster of the new version filled by `pg_dump` and `pg_restore`, a procedure and not a field.
11. **Credentials and configuration.** Generated Secrets for the superuser, the replication user, each role and the TLS material. Parameters, databases and roles are declared on the cluster and reach the instances as files.
12. **Status.** The primary, the target primary, the timeline, each instance's role, lag and pending restart, the last base backup and the last archived WAL, and conditions.
13. **Reuse or write.** Reuse the proven components that do the data work inside the instances: PostgreSQL's own streaming replication, `pg_basebackup`, `pg_rewind` and promotion, and pgBackRest for backup and restore. Write the decision of who is primary, as the operator, since it is the part tied to gaon's Lease, fencing and Services.

## Options weighed

### Reuse or write

Asked by aurora's owner: how PostgreSQL runs without CloudNativePG. The two places a component could be reused are leader election with failover, and backup with point-in-time restore. Facts about a tool not read are marked unverified.

- **Backup and restore: reuse.** Chosen: pgBackRest. Writing WAL archiving, a base backup with a manifest, retention and restore to a time would be writing a backup tool, whose bugs show only at a restore. The weighing of the three tools is under "The backup tool" below.
- **Replication, promotion and rewind: reuse PostgreSQL's own.** Streaming replication, `pg_basebackup`, `pg_rewind` and promotion are what every option below calls; none is written here.
- **Leader election and failover, option A: Patroni with etcd as its store.** Patroni is a process in each instance that holds a leader key with a time to live in a distributed store and promotes and demotes its PostgreSQL by it (unverified, from memory). Not taken:
  - ADR 0004 keeps etcd's clients to the API server, and etcd's client port is open to control-plane Nodes only ([ADR 0028](./0028-a-cronjob-runs-gaon-etcd-snapshot-which-uploads-a-checked-etcd-snapshot-to-s3-under-iam-roles-anywhere-credentials.md) Context), so an instance cannot reach it without a change to the core.
  - It adds a second expiry, the key's, beside the Node's Lease. The safety argument of gaon is invariant 1 and the Lease's confirmed expiry; a second timer that has to agree with it is the thing to avoid.
- **Option B: Patroni on gaon's API through Leases.** Patroni's store is pluggable; whether it can take one written for gaon, and in what language and size, is unverified. Not taken:
  - an instance's token has the audience AWS needs and is bound to no role on purpose (see "The credentials to AWS"), so each instance would need a role that writes Leases, and a compromised instance could then write them;
  - the coordination Lease has one holder and expiry measured by the observer ([resource-api.md](../resource-api.md) §Coordination Lease and Event), and it says nothing of the Node's: a primary on a partitioned Node could still believe it holds the key until its own timer ends, so the fence would still need invariant 1 underneath and the deletion of the MicroVM;
  - the Services would still need a writer of Endpoints.
- **Option C: write the decision in the operator.** Chosen. The decision has one input, the Store (instance reports by exec, Node conditions, MicroVMs), and one output, a status write followed by the fence and a promotion. It runs under the Lease the other units use ([workloads.md](../workloads.md) §Election).
  - **Why this is the safer one here.** A promotion is safe when the old primary cannot write, and in gaon that is known from the Store (the MicroVM is gone, or `LeaseExpired` is `True` and invariant 1 holds). Reusing an election would put a second authority beside that fact and leave the question of which one wins when they differ. Writing the decision uses the one fact the core guarantees.
  - **What it costs.** The code is ours to test, and the part of it that is the hard part, a failover with the primary's Node lost, is rehearsal 2 of #156 and a test of the harness. This is also why every step is idempotent and written before it is taken.
  - **What is not written:** the operator never touches PostgreSQL's replication protocol, WAL or a backup format.
- **A cluster of the operator's own that runs a consensus protocol among instances (Raft).** Rejected: it is the same second authority with more code.

### The workload kind for instances

- **StatefulSet** ([workloads.md](../workloads.md) §The stateful replicas). Gives ordinal names, a Volume per replica that is never deleted, and a replacement with the same name and Volume.
  - **Rejected.** A replacement has the same name and the same `local` Volume, and that Volume is pinned to one Node, so a replica whose Node is lost cannot be replaced elsewhere: the set waits for the Node. A cluster that survives the loss of a Node (R10) needs a new instance with a new Volume on another Node. The set also has one template, so the primary and the standbys could not differ in what they run, and it places by the Scheduler alone, which spreads nothing.
- **MicroVMs the operator owns, with Volumes it creates.** Chosen. The operator names each instance, picks its Node, creates a replacement with a new Volume, and decides which is the primary. It writes what `workloads` writes (MicroVMs, Volumes with a `managed-by` label) under its own role.
  - **Cost.** The operator reimplements what the set does for ordinals and rollouts, which is small here because a rollout is a switchover.
- **A Deployment of one replica per instance.** Rejected. It replaces a MicroVM with a new one that names no Volume of the old, and a database needs the Volume to follow.

### Where the replicas go

- **The Scheduler spreads.** Not available: it reads no spread and no affinity ([scheduler.md](../scheduler.md) §Placement rule).
- **A Node label per Node, set by an administrator, and the operator picks.** Chosen. The label `postgres.gaon.garam.sh/node=<Node name>` on a Node marks it as one instances may use; the operator picks, for each new instance, a Node that is eligible (`Admitted`, `Ready`, no `LeaseExpired`) and holds no instance of the cluster, the fewest instances of any cluster first, and sets the instance's `nodeSelector` to that label. The Scheduler still checks fit. This is the gap named in [postgres.md](../postgres.md) §What the operator needs of the core.
- **The operator binds the MicroVM itself.** Rejected. The binding is the Scheduler's section ([resource-api.md](../resource-api.md) §Common rules); a second writer would race it, and the role would need `microvms/binding`.
- **Changing the core to spread.** Not this ADR's to decide (#156 Goal); listed as a need.

### How an instance is told what to do, and how the operator reads it

- **Exec of the manager.** Chosen. The operator calls `POST /microvms/{name}/exec` ([resource-api.md](../resource-api.md) §Log and exec) with a command that asks the running manager over its local socket: a status report, a promotion, a shutdown, a base backup. It uses a public path and needs no route from a control-plane Node to a MicroVM's address.
  - **Cost.** An exec is one call per instance per sweep, and it answers only while the Node's agent is reachable, which is the signal the operator needs anyway. A role holding `create` on `microvms/exec` is treated as holding the guest, so the operator's rule is limited to MicroVMs labeled `managed-by=gaon-postgres`.
- **An HTTP port on the manager, called by the operator.** Rejected: no document of the core says a control-plane process reaches a MicroVM's address, and an open port needs its own authentication.
- **The manager writes its status to the API.** Rejected: the instance's token has the audience AWS needs, which is also one the API server accepts ([node-agent.md](../node-agent.md) §Configuration, `--service-account-audience`), so the account is bound to no role, and giving it one would let a compromised instance write the cluster.

### Replication mode, and what bounds loss

- **Asynchronous.** Default. Loss is the lag at the moment of the failure, not otherwise bounded; the operator reports it.
- **Synchronous, `ANY n`.** Per cluster. A commit is acknowledged only after `n` standbys have it, so a promotion loses no acknowledged commit as long as fewer than `n` standbys are unreachable at the decision; the operator refuses to promote otherwise. The cost is that the primary stops committing when fewer than `n` standbys are connected, which for aurora's one replica means a lost replica stops writes. That is a choice the cluster makes, which is why it is a field and not a default.
- **Quorum with a relaxing mode (CloudNativePG's `preferred`).** Not taken: the first version has two values and no state in which durability changes without the user's word.

### How failover is decided

- **The operator, from the Store and the exec reports, written down before it acts.** Chosen. `status.targetPrimary` is written first, then the fence, then the promotion, so a new leader of the Lease reads where the old one stopped. Every step is idempotent and read from the Store.
- **A consensus agent in each instance (Patroni).** Rejected, with its reasons under "Reuse or write".
- **The Node Agent restarts the primary and nothing else.** Not a failover.

### Fencing

- **Delete the old primary's MicroVM and wait.** Chosen. A delete is durable in the Store, so a Node that returns does not start the old primary: its agent releases a MicroVM being deleted ([node-agent.md](../node-agent.md) §Finalizer, §MicroVMs "Run"). The wait is for the MicroVM to be gone, which its agent does after it has stopped the machine. For a Node that does not answer, the wait is for `LeaseExpired` `True`, after which invariant 1 holds that the old copy has stopped ([node-lifecycle.md](../node-lifecycle.md)).
- **Halt it (`runStrategy` `Halted`).** Equivalent in effect and kept as no alternative worth a second path.
- **Rely on the Lease alone.** Not enough: a primary that crashes on a healthy Node would otherwise never be stopped.
- **STONITH through mvmd or a power control.** Not in the core and not available.
- **The topology file.** The operator also writes `<cluster>-topology`, a ConfigMap naming the primary, mounted as a file. A manager that starts finds another instance named and starts as a standby, and a running primary that sees another named stops PostgreSQL. This is a second line and nothing relies on it alone.

### How the Services follow the primary

- **A Service with a selector on a `role` label.** Rejected. The operator would move a label across two MicroVMs with two writes, and for the length between them none or two are selected. Readiness would have to mean "is the primary".
- **A Service with no selector and Endpoints the operator writes.** Chosen. `-rw` lists the primary alone and `-ro` the Ready standbys, or the primary when no standby is Ready; each switch is one write of one object, after the promotion succeeded ([resource-api.md](../resource-api.md) §Service, Endpoints and NetworkPolicy "Who writes it").
- **The standbys follow `<cluster>-rw`,** by its DNS name ([dns.md](../dns.md)), and so need no address of the primary.

### The backup tool

All three are judged on what #156 asks: S3 under a web identity, WAL archiving, a point in time, retention, and the facts for status.

| Tool | S3 under a web identity | Status facts | Notes |
|---|---|---|---|
| pgBackRest | `repo-s3-key-type` accepts `web-id` (verified, pgbackrest.org configuration reference). That it reads `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE`, and re-reads the token file as the agent replaces it, is **unverified**. | `info` with JSON output gives the backups and the archived WAL range (unverified, from memory) | C binary in the PGDG packages; retention by count or time in its own settings; archive push and get; restore to a time |
| WAL-G | The S3 reference read does not state web identity ("determines AWS credentials like other AWS tools", verified); **unverified** | `backup-list` and `wal-show` (unverified) | Static Go binary built from source; delta backups |
| barman-cloud | Through boto3, which supports web identity (unverified) | its own list (unverified) | Python; CloudNativePG's tool; no standby backup in the cloud mode that CloudNativePG documents (unverified) |

- **pgBackRest is chosen** because the only one of the three whose reference states a web-identity key type, because its retention is a setting and not a script, and because its `info` is the source of requirement 9's last backup and last archived WAL.
- **If the first test shows pgBackRest does not read the token file,** the decision is wrong in its first clause, and the fall-back is WAL-G with the same flow; the contract above the tool does not change.
- **A base backup is taken in the primary's MicroVM.** A backup run by a CronJob's MicroVM would need pgBackRest's remote protocol into the primary, which is a server and a certificate in each instance for no gain, and the CronJob's schedule cannot see which instance is primary. The schedule is therefore evaluated by the operator, which asks the primary's manager to run the backup. The cost is the primary's I/O during a backup, which is stated and not hidden.

### The credentials to AWS

- **A ServiceAccount token through `AssumeRoleWithWebIdentity`.** Chosen, as D13 decides. The operator creates `postgres-<cluster>` and the instances name it.
- **IAM Roles Anywhere, as the etcd snapshot does** ([etcd-snapshot.md](../etcd-snapshot.md) §Credentials). Not taken: D13 names the operator and the token, and the certificate of a MicroVM would need a signer the core does not offer for workloads.
- **A static key in a Secret.** Forbidden by #156 requirement 5.
- **The constraint.** The token is also accepted by the API server for the user `gaon:serviceaccount:postgres-<cluster>` because the agent's audience is one of the API audiences ([resource-api.md](../resource-api.md) §Secret, ConfigMap and ServiceAccount). The operator binds that user to no role.

### Minor and major upgrades

- **Minor: rolling replacement.** Each standby in turn is stopped through the manager, its MicroVM deleted and made again with the new image and the same Volume, and waited for until it is Ready and its lag is below a bound. Then a switchover: the primary is shut down through the manager, the standby that has received its last WAL is promoted, the Endpoints move, and the old primary is made again as a standby. Nothing acknowledged is lost. The same path serves a parameter that needs a restart.
- **Major: a dump and restore into a new cluster.** Chosen as the first version, as #156 allows. Downtime is the time from stopping the writers to starting them on the new cluster, and the cutover is the user's.
- **`pg_upgrade` in place.** Not taken now: it needs both versions' binaries in one image and, in link mode, cannot go back, and the image is one major. It may come as its own ADR.
- **Logical replication.** Out of scope in #156.

### Credentials and configuration

- **Secrets the operator creates, one per credential,** labeled `managed-by=gaon-postgres`, so its role can be limited by label. A password a user supplies is a Secret the user labels `postgres.gaon.garam.sh/password=true`, because a role cannot name a Secret ([resource-api.md](../resource-api.md) §RBAC), and the operator reads no other.
- **Files, not environment.** An environment reference resolves when the MicroVM starts, a file is replaced in place ([resource-api.md](../resource-api.md) §MicroVM, ADR 0029), so a changed parameter, role or password reaches a running instance and the manager reloads it.
- **TLS** is on, with a CA the operator creates per cluster. Rotation is open.

## Consequences

- **The core does not change.** What the operator lacks is listed as gaps in [postgres.md](../postgres.md) §What the operator needs of the core; the largest are no spread, no Node-name label, and an object on a lost Node that stays until that Node returns.
- **The operator owns what a StatefulSet would.** Ordinals are serial numbers, a rollout is a switchover, and a replacement is a new Volume and a base backup.
- **A failover is not instant,** and its time is derived and not measured: see [postgres.md](../postgres.md) §R3 Failover.
- **A lost Node leaves Terminating objects** (its MicroVM and Volume) until it returns, since only its agent clears their finalizers ([node-agent.md](../node-agent.md) §Finalizer).
- **The primary carries the backup's I/O.**
- **aurora has work to do before a backup runs:** the bucket, the issuer published on public HTTPS, the OIDC provider, a role per cluster, the ECR repository and the Node labels.
- **The operator reads every instance by exec,** so its role holds `create` on `microvms/exec` for MicroVMs of its label.
- **A third-party fact may be wrong.** Those marked unverified are tested first, by the implementation issue that uses them.

## Signs the decision was wrong

- The first run of pgBackRest on a gaon token does not obtain credentials, or its credentials lapse after an hour with the token file replaced.
- A failover on the harness takes much longer than the derived time, or promotes a standby that lacks a commit a synchronous cluster acknowledged.
- A returning old primary accepts a write, or a MicroVM that was deleted starts.
- The exec per instance per sweep loads the API server or the agents noticeably, or an agent's answer for a healthy instance is slow enough to look like a loss.
- Instances land on one Node or on two Nodes of one hypervisor often enough that the label per Node is not a placement an administrator can keep.
- An administrator deletes lost Nodes' objects by hand often, which says the replacement path needs the core.

## Rejected alternatives

- **StatefulSet, a Deployment per instance, the operator binding, a selector Service, a consensus agent per instance, a CronJob base backup, a static key, Roles Anywhere, `pg_upgrade` now.** Each is weighed above with its reason.
- **VolumeSnapshot to seed a standby.** A `local` snapshot is a file on the source Volume's Node, and a Volume made from it is pinned to that Node ([resource-api.md](../resource-api.md) §VolumeSnapshot, §Volume "From a snapshot"), which is the Node the standby must not use. It is also not a backup.
- **A pooler in front.** Out of scope in #156, and garam's platform needs session connections.
