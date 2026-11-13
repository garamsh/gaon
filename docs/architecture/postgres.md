# PostgreSQL operator

What the PostgreSQL operator does: it runs PostgreSQL clusters as MicroVMs on a gaon cluster, with replication, failover, backup and restore. The unit is `postgres/`, its binaries are `gaon-postgres-operator` and `gaon-postgres-manager`, and its image is `gaon/postgres`. It is a client of the public API through `lib/api` and imports no other unit's packages. This document is the contract the implementation issues are cut from: nothing is built yet.

Basis: [ADR 0044](./adr/0044-the-postgresql-operator-is-a-unit-of-gaon-that-runs-each-instance-as-a-microvm-with-a-local-volume-fences-by-deleting-the-old-primary-and-backs-up-with-pgbackrest-under-a-serviceaccount-token.md), garamsh/gaon#156 for the requirements, and D13 of garamsh/gaon#117.

Facts about PostgreSQL and pgBackRest below are from the tools' documented behaviour as the author knows it and are **unverified** unless a line says verified. The implementation issue that first depends on one tests it.

## Current decisions

### The unit

- **Registration.** `postgres/` creates or updates the two definitions at start, as `workloads/` registers its kinds ([workloads.md](./workloads.md) §Registration).
- **Election.** The operator acts only while it holds the coordination Lease `gaon-postgres-operator`, on each control-plane Node, with `lib/api`'s leader-election flags ([workloads.md](./workloads.md) §Election).
- **Sweep.** Each term sweeps every 2 seconds by default and decides from the Store and from the instances' reports; no decision is kept in memory.
- **Identity and role.** The certificate's common name is `gaon-postgres-operator`; `postgres/deploy/rbac.yaml` holds the ClusterRole and binding `gaon:postgres-operator`, applied with `gaon apply -f`. The role:
  - `create`, `update` of definitions labeled `managed-by=gaon-postgres`; `get` of definitions with no selector;
  - `get`, `list`, `watch` of `postgresclusters` and `postgresbackups`; `update` of their `status`; `create`, `delete` of `postgresbackups` labeled `managed-by=gaon-postgres`;
  - `create`, `get`, `list`, `delete` of MicroVMs labeled `managed-by=gaon-postgres`; `create` of `microvms/exec` on MicroVMs of that label;
  - `create`, `get`, `list`, `update`, `delete` of Volumes of that label (an update grows one);
  - `create`, `get`, `list`, `update` of Services, Endpoints, ConfigMaps and ServiceAccounts of that label;
  - `create`, `get`, `list`, `update` of Secrets labeled `managed-by=gaon-postgres`, and `get` of Secrets labeled `postgres.gaon.garam.sh/password=true`;
  - `get`, `list`, `watch` of Nodes; `get`, `create`, `update` of `leases` of `coordination.gaon.garam.sh`.
  - The role is bound by an administrator; the ServiceAccount of a cluster is bound to no role.
- **Deployment.** [deployment.md](./deployment.md) lists the binary per control-plane Node once the implementation lands; this document does not.

### The image

- **One image per major, `gaon/postgres:<major>-<version>`,** built from `postgres/Dockerfile` on a digest-pinned PostgreSQL base, holding PostgreSQL, pgBackRest and `gaon-postgres-manager`. It runs as the base's non-root postgres user, with no shell as an entrypoint.
- **A release is the git tag `postgres/v0.1.0-dev.N`, `-rc.N` or `postgres/v0.1.0`,** N a positive integer without leading zeros (aurora#749, decision 14); the workflow `.github/workflows/postgres-image.yml` builds both majors and pushes them to the ECR repository `gaon/postgres` in account 486152169996, region ap-northeast-2, as [etcd-snapshot.md](./etcd-snapshot.md) §The image states for its unit.
- **A cluster names its image by digest,** `<registry>/gaon/postgres@<digest>`, which the Node Agent pulls ([node-agent.md](./node-agent.md) §MicroVMs). The digests of a release are in the release notes of its tag; a tag reference is refused.
- **The base images are pinned by digest** and moved by Dependabot. pgBackRest's version is a build argument; Dependabot does not follow it (open question).

### The kinds

Both are cluster-scoped kinds of `postgres.gaon.garam.sh/v1alpha1`, with `status` as a subresource, and registered under the schema subset of [resource-api.md](./resource-api.md) §CustomResourceDefinition. That subset has no default, no `additionalProperties` and no bound on a list, so the operator applies each default below and checks each bound, and reports a cluster that breaks one as `Valid` false.

**`PostgresCluster` `spec`:**

| Field | Meaning | Default |
|---|---|---|
| `image` | digest reference of the instance image; its major is the cluster's | none, required |
| `instances` | total instances, primary included, 1 or more | 1 |
| `storage.size`, `storage.class` | each instance's Volume: a quantity, and a StorageClass | none, `local` |
| `resources` | `requests` and `limits` of cpu and memory of each MicroVM | none |
| `postgres.parameters` | map of parameter to string value | empty |
| `replication.mode` | `Asynchronous` or `Synchronous` | `Asynchronous` |
| `replication.synchronousStandbys` | `n` of `ANY n`, for `Synchronous`, 1 up to `instances - 1` | 1 |
| `failover.primaryFailureSeconds` | time a primary must be unreachable or not Ready before failover | 30 |
| `failover.instanceReplaceSeconds` | time an instance's Node must be lost before the instance is replaced | 600 |
| `databases` | list of `{name, owner}`, the owner a declared role | empty |
| `roles` | list of `{name, passwordSecret}`; absent `passwordSecret`, the operator generates the password; at most 32 | empty |
| `backup.bucket`, `.region`, `.prefix` | where the repository is; the prefix is the cluster's own | none, backup off |
| `backup.roleArn` | the IAM role the instances assume | none, required with `bucket` |
| `backup.schedule` | `{full, differential}`, each a five-field cron expression in UTC, or empty | empty |
| `backup.retention.fullBackups` | number of full backups kept, with the WAL they need | 7 |
| `bootstrap.recovery` | `{sourcePrefix, sourceStanza, targetTime, backupSet}`, creation only | absent: `initdb` |

- **Immutable after creation:** `bootstrap`, `backup.bucket`, `backup.prefix` and the major of `image`. A change to `image` to a digest of another major sets `Valid` false and changes nothing.
- **Parameters the operator owns** are refused in `postgres.parameters` with `Valid` false: `listen_addresses`, `wal_level`, `archive_mode`, `archive_command`, `restore_command`, `primary_conninfo`, `hot_standby`, `max_wal_senders`, `synchronous_standby_names`, `ssl` and its files, `data_directory`, `hba_file` and `recovery_target*`. The rest pass through, as strings.
- **Names.** A cluster's name is a DNS label of at most 40 characters that starts with a letter, since the Service names `<name>-rw` and `<name>-ro` and the instances `<name>-<n>` come from it.

**`PostgresCluster` `status`**, written by the operator to `/status`:

| Field | Meaning |
|---|---|
| `phase` | `Creating`, `Healthy`, `Degraded`, `FailingOver`, `Upgrading`, `Restoring` |
| `observedGeneration` | the `generation` last acted on |
| `primary` | the instance that is primary now |
| `targetPrimary` | the instance a failover or switchover has decided on; equals `primary` when none is under way |
| `timeline` | PostgreSQL's timeline of the primary |
| `version` | the server version the primary reports |
| `instances` | list of `{name, node, role, ready, lagBytes, lagSeconds, pendingRestart}`, `role` being `Primary`, `Standby` or `Unknown` |
| `nextInstance` | the serial the next instance takes |
| `backup.lastSuccessful` | `{backupID, type, startTime, stopTime}` of the last good base backup |
| `backup.lastArchivedWAL`, `backup.lastArchivedTime` | the newest WAL the repository holds and when it arrived |
| `backup.firstRecoverabilityTime` | the oldest time a restore can reach |
| `conditions` | `Valid`, `Ready` (a primary is writable and every declared instance is Ready), `ContinuousArchiving`, `RestartRequired`, `Unschedulable` |

- **Reasons and messages** carry the detail, never a Secret's value.

**`PostgresBackup`.** `spec`: `cluster` (the name) and `type` (`Full`, `Differential`, `Incremental`), written by a user, or by the operator at a scheduled time with the labels `managed-by=gaon-postgres` and `postgres.gaon.garam.sh/cluster=<name>` and the name `<cluster>-<UTC yyyymmddThhmmss>`. `status`: `phase` (`Pending`, `Running`, `Completed`, `Failed`), `backupID`, `startTime`, `stopTime`, `size`, `walStart`, `walStop`, `message`. The operator deletes the records of backups pgBackRest has expired, and the record is owned by its cluster.

### Instances

- **An instance is a MicroVM** named `<cluster>-<n>`, owned by the cluster (owner reference with `uid`) and labeled `managed-by=gaon-postgres`, `postgres.gaon.garam.sh/cluster=<name>` and `postgres.gaon.garam.sh/instance=<n>`. `n` starts at 1 and is never reused; `status.nextInstance` holds the next.
- **Its spec:** the cluster's `image`; `command` the manager; `user` the numeric `uid:gid` of the image's postgres user; port 5432 named `postgres`; `runStrategy` `Always`; `serviceAccountName` `postgres-<cluster>`; `terminationGracePeriodSeconds` 120, provisional; a readiness and a liveness probe that run the manager's check; `nodeSelector` as below; one Volume.
- **Its Volume** is named `<cluster>-<n>`, class `storage.class`, `ReadWriteOnce`, size `storage.size`, labeled as the instance and owned by the cluster. It is created before the MicroVM. It is mounted at its `mountPath` and writable by the entrypoint's user, which the core is assumed to give (§What the operator needs of the core).
- **Its files** hold the topology (`<cluster>-topology`), the parameters, the TLS material and each role's password. Each is a `files` entry from a Secret or ConfigMap and so is replaced in place while the instance runs ([resource-api.md](./resource-api.md) §MicroVM); the manager reloads on a change.
- **Its Node** is chosen by the operator, once, when it creates the instance: from the Nodes labeled `postgres.gaon.garam.sh/node=<Node name>` that are `Admitted`, `Ready` and not `LeaseExpired`, one holding no instance of the cluster, the fewest instances of all clusters first, ties by name. It sets the instance's `nodeSelector` to that label and value; the Scheduler binds it and checks the fit. With no such Node the instance is not created and `Unschedulable` is True with the reason `NoNode`; an instance the Scheduler cannot place is reported the same way with the Scheduler's reason.
- **Creation order.** The first instance is created alone and becomes primary by `initdb`, or by the restore of R6. Each further instance is created once the primary is Ready, a standby seeded by `pg_basebackup` over the replication user, one at a time.
- **State is read by exec.** Each sweep the operator asks each instance's manager for a report: role, server version, received, replayed and flushed positions, lag, `pending_restart`, and, on the primary, the pgBackRest facts. An instance whose MicroVM is not `Running`, or whose exec answers `NotRunning` or `Unavailable` ([resource-api.md](./resource-api.md) §Log and exec), is unreachable. A report is never trusted past its sweep.
- **Deleting a cluster** deletes its MicroVMs, Volumes, Services, Endpoints, Secrets and ConfigMaps by owner reference ([garbage-collector.md](./garbage-collector.md)); the bucket is not touched. A Volume or MicroVM on a Node that does not return stays until it does.

### R1 Versions

- PostgreSQL 17 and 18 are the two majors the unit builds, one image each; a cluster's major is its image's.
- An instance whose manager reports a server major other than the primary's sets `Valid` false and is listed in no Endpoints.
- Extensions: none are built in beyond PostgreSQL's core; a consumer states a need on #156.

### R2 Topology

- One primary and `instances - 1` standbys by streaming replication, each in its own MicroVM with its own Volume, on a Node that holds no other instance of the cluster. aurora's value is `instances: 2`.
- A standby follows the primary by the name of `<cluster>-rw` ([dns.md](./dns.md) §Form: `<cluster>-rw.default.svc.<zone>`), so it needs no address of the primary.
- With fewer eligible Nodes than `instances`, the instances that fit are made, `Ready` is False and `Unschedulable` True.
- A lost standby's Node: once `failover.instanceReplaceSeconds` have passed since the Node's `LeaseExpired` became True, the operator deletes the instance's MicroVM and Volume and creates a new instance with a new serial, on another eligible Node, seeded by `pg_basebackup`. Before that it waits for the Node.

### R3 Failover

- **Detection.** The primary is failed when it has been unreachable, or its MicroVM not Ready, for `failover.primaryFailureSeconds` as the operator sees it, counted from the first sweep that saw it. A primary the Node Agent restarts under `Always` within that time is not failed over.
- **Decision.** The operator chooses the standby with the largest received position among those it can reach. For `Synchronous` it first requires that fewer than `synchronousStandbys` standbys are unreachable, and otherwise promotes nothing and reports `FailingOver` with the reason `NoSafeStandby`. It writes `status.targetPrimary` before it acts.
- **Fence, in this order:**
  1. the operator writes `<cluster>-topology` with the target as primary;
  2. it deletes the old primary's MicroVM;
  3. it waits until that MicroVM is gone from the Store, which its agent does once the machine is stopped ([node-agent.md](./node-agent.md) §Finalizer), or, when the Node does not answer exec, until the Node's `LeaseExpired` is `True` ([node-lifecycle.md](./node-lifecycle.md)), after which invariant 1 holds that the old copy has stopped.
- **Promote.** The operator asks the target's manager to promote, waits for the report of a writable primary on a new timeline, sets `status.primary` equal to the target and writes the Endpoints (R4). Then the other standbys follow `<cluster>-rw`, using `pg_rewind` where their history diverged, and otherwise by a new `pg_basebackup`.
- **A returning old primary** is not started: its MicroVM was deleted, and a Node that returns has its agent release it. The old instance is then replaced by a new one with a new serial, as in R2, so the old Volume is never mounted again.
- **What bounds data loss.**
  - `Synchronous`: no acknowledged commit is lost while fewer than `synchronousStandbys` standbys are unreachable at the decision, and the operator refuses otherwise. Writes stop on the primary while fewer than that many standbys are connected.
  - `Asynchronous`: the commits the promoted standby had not received, which is the lag at the loss; not otherwise bounded. `status.instances[].lagBytes` shows it.
- **How long it takes.** Derived, **not measured**; the first harness test measures it and this section is then corrected.
  - **A Node lost:** at least `Lease duration + expiry margin` after the last renewal the controller saw, 30 s and 5 s under [resource-api.md](./resource-api.md) §Lease and fencing parameters (provisional), then the operator's sweep, a promotion and the Node Agents' next round that carries the new Endpoints. Expected around one minute.
  - **A primary that fails with its Node alive:** `failover.primaryFailureSeconds` (30 s), the stop of the machine up to its grace period if it is running, a promotion and the same round. Expected 40 to 60 seconds.
  - **A planned switchover (R7):** seconds, with no loss.
- **The operator down** stops failover and nothing else: the instances keep serving, and the new leader of the Lease resumes from `status`.

### R4 Addresses

- **`<cluster>-rw`** is a Service with no selector, a port named `postgres` at 5432, and an Endpoints of the same name holding the primary's address from `status.addresses` and a `targetRef` to its MicroVM, written once the primary is Ready.
- **`<cluster>-ro`** is the same, listing the Ready standbys, or the primary alone while no standby is Ready.
- Both are owned by the cluster and labeled `managed-by=gaon-postgres`. The name resolves as `<cluster>-rw.default.svc.<zone>` ([dns.md](./dns.md) §Form).
- A switch is one write of one Endpoints, after the promotion of R3 has succeeded. Connections to the old primary drop when its MicroVM goes; a client reconnects to the same name.

### R5 Backup

- **Tool.** pgBackRest, in the instances. WAL goes by its `archive-push` as `archive_command`, with a local spool; base backups by `backup` run in the primary's MicroVM by exec of the manager.
- **Schedule.** The operator evaluates `backup.schedule` in UTC each sweep. For each time that has come and has no `PostgresBackup`, it creates one named for the time and then asks the primary's manager to run it, one at a time per cluster; a time missed while no leader ran is not owed. A user's `PostgresBackup` runs the same way.
- **Retention.** `backup.retention.fullBackups` full backups, with the differentials and WAL that depend on them; pgBackRest expires the rest and the operator deletes the records of what expired.
- **Status.** `status.backup` is read from pgBackRest's `info` on the primary; `ContinuousArchiving` is True while the newest archived WAL is less than 5 minutes behind the primary's current position, provisional.
- **Credentials.** The operator creates the ServiceAccount `postgres-<cluster>`; each instance names it, and its token is the file at `/var/run/secrets/gaon.garam.sh/serviceaccount/token` ([resource-api.md](./resource-api.md) §Secret, ConfigMap and ServiceAccount). The instance's environment sets `AWS_ROLE_ARN` to `backup.roleArn`, `AWS_WEB_IDENTITY_TOKEN_FILE` to that path and `AWS_REGION`, and pgBackRest is configured with `repo-s3-key-type=web-id` (the value is verified in pgBackRest's configuration reference; its use of those variables and its re-reading of the file are **unverified**). No key is in an object.
- **What AWS must hold (aurora):**
  - the bucket outside the cluster;
  - an IAM OIDC identity provider for gaon's issuer, with the agent's `--service-account-audience` as its client ID;
  - a role per cluster whose trust policy requires the issuer's `sub` equal to `gaon:serviceaccount:postgres-<cluster>` and `aud` equal to that audience, and whose policy allows `s3:ListBucket` on the bucket for the cluster's prefix and `s3:GetObject`, `s3:PutObject` and `s3:DeleteObject` on the prefix (deletes are the retention);
  - for a restore, `s3:ListBucket` and `s3:GetObject` on the source prefix for the new cluster's role, read-only;
  - the ECR repository `gaon/postgres` and a role to push to it.
- **What aurora must publish.** The API server's `--service-account-issuer` is a public HTTPS host with no path, with a publicly trusted certificate, where aurora publishes `/.well-known/openid-configuration` and `/openid/v1/jwks` as the API server serves them, again on every signing-key change, and the issuer is set before workloads hold tokens, since a change invalidates tokens already issued (issuecomment-6851861982 on #156). **Unverified:** nothing has run against AWS STS with a gaon token.
- **Egress.** Instances reach S3 and STS (`sts.ap-northeast-2.amazonaws.com`); garamsh/naru#57.
- **The token is also an API token** for the user `gaon:serviceaccount:postgres-<cluster>`, which the operator binds to no role.

### R6 Restore

- **A new cluster** with `spec.bootstrap.recovery` names `sourcePrefix` and `sourceStanza` in its own `backup.bucket`, optionally a `backupSet` and a `targetTime` (RFC 3339, UTC). Absent both, it restores to the latest point the repository holds.
- **No access to the source cluster.** The operator reads nothing of the source cluster: the restore is of the repository, by the new cluster's role. The new cluster's `backup.prefix` must differ from `sourcePrefix` or `Valid` is False, so it never writes to the source's prefix.
- **Steps.** The first instance has `phase` `Restoring`: its manager runs pgBackRest's restore to the target into the Volume, starts PostgreSQL, promotes it to a new timeline when the target is reached, and the cluster then runs as any other, archiving to its own prefix and taking a first full backup at once. The standbys follow as in R2.
- **Refused:** a major that differs from the backup's, a target before the backup's end or after the last archived WAL (the first sets `Valid` false, the second restores to the end of the WAL and says so in a condition message).
- **Shown by** the rehearsal 3 of aurora#749 §Final check: the new cluster's status shows the backup set and the time it reached.

### R7 Upgrade

- **Minor.** A change of `image` to a digest of the same major. The operator replaces each standby in turn: shutdown by the manager, delete the MicroVM, create it again with the new image and the same Volume, wait until it is Ready and its lag is under 16 MiB, provisional. Then it switches over: it asks the primary's manager to shut down and report its last position, waits until a standby has received it, fences as in R3 (the shutdown has stopped it; its MicroVM is deleted), promotes that standby, writes the Endpoints and makes the old primary again as a standby. No acknowledged commit is lost.
- **With one standby,** the cluster has no standby while it is replaced; `Ready` stays True and `Degraded` is the phase, and aurora should know the window exists.
- **A parameter that needs a restart** is reported by the instance as `pendingRestart` and by the cluster as `RestartRequired`; the operator then restarts the instances by the same path, standbys first and a switchover last.
- **Major.** A procedure, not a field:
  1. create a new cluster of the new image with the databases and roles declared;
  2. stop the writers of the old cluster;
  3. dump each database from `<old>-rw` and restore it into the new cluster by exec of the manager on the new primary, with `pg_dump` and `pg_restore`;
  4. start the writers against `<new>-rw`;
  5. delete the old cluster when the new one has taken a backup.
  
  `pg_upgrade` in place is not offered (ADR 0044).

### R8 Credentials and configuration

- **Secrets the operator creates,** labeled `managed-by=gaon-postgres` and owned by the cluster: `<cluster>-superuser` (keys `username`, `password`), `<cluster>-replication`, `<cluster>-role-<name>` for each declared role without `passwordSecret`, and `<cluster>-ca` and `<cluster>-tls`. A password is random and made once, never changed by the operator.
- **A Secret the user gives** is one named by `passwordSecret`, labeled `postgres.gaon.garam.sh/password=true`, with the key `password`; a Secret that does not exist or lacks the label or key sets `Valid` false.
- **Databases and roles.** The manager of the primary creates each declared database with its owner and each declared role with login and the password, and sets the password again when its file changes; it drops nothing that is not declared. It runs this when the primary starts, after a promotion and on a change of the declaration.
- **Parameters** are written to a ConfigMap `<cluster>-config` and reach each instance as a file; the manager reloads. A parameter that needs a restart is R7's.
- **TLS.** The operator creates a CA per cluster and a server certificate for `<cluster>-rw` and `<cluster>-ro` names and the instances', mounted as files; PostgreSQL requires it for every connection that is not local. A client verifies it with `<cluster>-ca`'s certificate.
- **No value is logged.** Neither the operator nor the manager writes a password, token or key to a log, Event, status or error ([resource-api.md](./resource-api.md) §Secret, ConfigMap and ServiceAccount).

### R9 Status and observation

- The fields of §The kinds, with `status.primary`, `instances[].role` and `lagBytes`, `backup.lastSuccessful` and `backup.lastArchivedWAL` the ones requirement 9 names.
- The operator writes status only when it differs from what is stored, at most once per sweep; `gaon get` shows it ([cli.md](./cli.md)).
- A report not obtained leaves the field it fills at its last value with `Unknown` in `role` and the reason in the `Ready` condition.

### R10 Loss of a Node, and a naru release in place

- **A Node holding a standby is lost:** the cluster serves; `Ready` is True, `phase` `Degraded`, the instance `Unknown`; after `failover.instanceReplaceSeconds` it is replaced (R2).
- **A Node holding the primary is lost:** R3. What was written before is read after, with `Synchronous` as R3 states, and up to the lag with `Asynchronous`.
- **The Node returns:** its agent releases the MicroVMs being deleted and the Volumes' finalizers clear; no process of the old instance starts.
- **A naru release applied in place** may interrupt replication and the Service for a time that is **not known** (garamsh/naru; unverified). The decision is that a break shorter than `failover.primaryFailureSeconds` fails nothing over; replication reconnects by itself and the standbys reach the primary through `<cluster>-rw`. If a release interrupts longer than that, a failover happens and is correct. The harness test of rehearsal 4 measures the break and this value is chosen from it.
- **Shown by** rehearsals 2 and 4 of aurora#749 §Final check, on aurora's cluster or the harness.

### Tests

Each requirement names a test that cites its heading: `R1` to `R10` above. A test shows a break by failing with the behaviour removed, as the implementation issues require.

## What the operator needs of the core

### Uses of public APIs

| Use | Document and section |
|---|---|
| Register `PostgresCluster` and `PostgresBackup` with a CustomResourceDefinition, status subresource, cluster scope, one version | [resource-api.md](./resource-api.md) §CustomResourceDefinition |
| Create and delete MicroVMs with image, command, env, files, ports, probes, `serviceAccountName`, volumes, `nodeSelector`, `runStrategy`, `terminationGracePeriodSeconds`; read `status.phase`, `Ready`, `addresses` | §MicroVM; [node-agent.md](./node-agent.md) §MicroVMs |
| Create and grow Volumes of class `local`; read `status.capacity` and attachment | §Volume |
| Exec of the manager in an instance | §Log and exec |
| Create ServiceAccounts; the token file in the guest | §Secret, ConfigMap and ServiceAccount; [node-agent.md](./node-agent.md) §MicroVMs |
| Create Secrets and ConfigMaps and mount them as files replaced in place | §Secret, ConfigMap and ServiceAccount; §MicroVM |
| Write a Service with no selector and its Endpoints | §Service, Endpoints and NetworkPolicy |
| Read Nodes, their conditions and `LeaseExpired` | §Node; [node-lifecycle.md](./node-lifecycle.md) |
| Label selectors in a role, and on list and watch | §RBAC; §Metadata |
| Owner references and garbage collection | §Metadata; [garbage-collector.md](./garbage-collector.md) |
| A coordination Lease | §Coordination Lease and Event |
| Set a Node's labels (the administrator, not the operator) | [scheduler.md](./scheduler.md) §Placement rule, "Setting a Node's labels" |

### Assumed, not yet in the core

- **A Volume is mounted at its `mountPath`.** Today a Volume is attached to a MicroVM and not mounted (garamsh/gaon#299, ordered first in the node-agent role). The operator designs on the mount and is blocked by it.
- **A mounted disk is writable by a non-root entrypoint.** Setting the owner of the mounted filesystem's root is garamsh/mvmd#648. PostgreSQL refuses a data directory its user does not own, and the image runs as a non-root user.

### Gaps

Each is a place the core lacks what the operator would use. The operator works around it as stated, and a change to the core is listed and not made.

| Gap | Where it shows | What the operator does |
|---|---|---|
| **No spread or anti-affinity, and no label that names a Node.** The Scheduler reads capacity, `VolumeNode` and `nodeSelector` only, and a Node Agent creates its Node with no labels. | [scheduler.md](./scheduler.md) §Placement rule; [resource-api.md](./resource-api.md) §Node | An administrator labels each Node `postgres.gaon.garam.sh/node=<name>`; the operator picks the Node and sets the selector. |
| **No knowledge of failure domains.** Two Nodes can be on one hypervisor host (aurora's `node-2` and `node-3`). | [scheduler.md](./scheduler.md) §Placement rule | None; the operator treats Nodes as the domain. Open. |
| **An object on a lost Node stays until that Node returns.** A MicroVM bound there keeps the agent's finalizer, and so does its Volume; nothing releases them. | [resource-api.md](./resource-api.md) §Common rules, §Volume "Deletion"; [node-agent.md](./node-agent.md) §Finalizer | The operator deletes and replaces, and the old objects stay `Terminating`. |
| **A MicroVM with a `local` Volume is not rebound,** so a standby cannot move; a replacement is a new Volume. | [scheduler.md](./scheduler.md) §Rebinding; [resource-api.md](./resource-api.md) §Volume "Pinning" | New instance, new Volume, `pg_basebackup`. |
| **A role cannot name an object.** A rule has groups, resources, verbs and a label selector, no names, so reading a user's Secret needs a label the user sets. | [resource-api.md](./resource-api.md) §RBAC | Secrets the operator reads carry `postgres.gaon.garam.sh/password=true`. |
| **The schema subset has no default, `additionalProperties`, `maxItems` or `format`.** | [resource-api.md](./resource-api.md) §CustomResourceDefinition | The operator defaults and checks; `postgres.parameters` is an object that preserves unknown fields. |
| **Exec has no standard input, no choice of user, and needs the agent.** A role holding it is treated as holding the guest. | [resource-api.md](./resource-api.md) §Log and exec | The manager answers a command-line call with a short output; the role limits exec to the operator's label. |
| **A change to a MicroVM's spec is a replacement,** so a restart in place is not available. | [resource-api.md](./resource-api.md) §MicroVM "Mutable" | Delete, wait, create with the same name and Volume. |
| **The signal the guest gets on a stop is not stated by the core;** the agent sends mvmd a grace period. | [node-agent.md](./node-agent.md) §MicroVMs "Grace period" | The operator asks the manager to shut PostgreSQL down first and relies on the stop only for the process after it. |
| **The token is an API token too.** The agent's audience is one of the API audiences, so an instance holds a token the API server accepts. | [resource-api.md](./resource-api.md) §Secret, ConfigMap and ServiceAccount; [node-agent.md](./node-agent.md) §Configuration | The operator binds the account to no role. |
| **No documented route from a control-plane process to a MicroVM's address.** | [resource-api.md](./resource-api.md) §Log and exec | Exec only; no direct connection to PostgreSQL from the operator. |

## Rationale

- **Instances are MicroVMs, not a StatefulSet,** because a `local` Volume cannot leave its Node and a replacement of a lost replica must: [ADR 0044](./adr/0044-the-postgresql-operator-is-a-unit-of-gaon-that-runs-each-instance-as-a-microvm-with-a-local-volume-fences-by-deleting-the-old-primary-and-backs-up-with-pgbackrest-under-a-serviceaccount-token.md) "The workload kind for instances".
- **The failover decision is written, the data work is reused,** since the safety of a promotion is a fact the Store holds (the MicroVM is gone, or the Lease has expired) and a second election would put another authority beside it: ADR 0044 "Reuse or write".
- **Fencing is a deletion and a wait,** because a delete is durable across a Node's return and invariant 1 covers a Node that does not answer.
- **The Services have no selector,** because moving a label is two writes with a window of none or two.
- **pgBackRest,** because its reference states a web-identity key type and its `info` answers requirement 9; the other two are not shown to.
- **Backups run in the primary,** because the schedule must follow it and a remote backup needs a server in each instance.

## Open questions

- **Does pgBackRest read the web identity environment variables and the replaced token file?** Unverified; the first implementation issue tests it, and WAL-G is the fall-back.
- **How long do failovers take, and what does a naru release in place interrupt?** Both unmeasured; the harness measures them and `primaryFailureSeconds` is then chosen.
- **Does a guest's PostgreSQL get a signal on a stop,** and for how long may it flush? Not stated by the core; the operator does not depend on it.
- **Failure domains** (two Nodes on one host). A label the administrator keeps, or a core change.
- **TLS rotation.** The operator creates the material once.
- **pgBackRest's version** is a build argument Dependabot does not follow.
- **Standby backup** to take the load off the primary.
- **`pg_upgrade` in place** as a later ADR.
- **A cap on lag** that stops a failover to a standby too far behind, for `Asynchronous`.
- **The operator's Services in a NetworkPolicy** for the cluster: not designed.
