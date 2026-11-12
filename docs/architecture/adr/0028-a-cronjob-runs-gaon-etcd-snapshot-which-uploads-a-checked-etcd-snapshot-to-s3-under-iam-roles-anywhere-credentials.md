# ADR 0028: A CronJob runs gaon-etcd-snapshot, which uploads a checked etcd snapshot to S3 under IAM Roles Anywhere credentials

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#174.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **The Store is the only copy of the cluster's state.** Three members survive the loss of one Node ([ADR 0023](./0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)); they do not survive the loss of the disks, a bad write that replicates, or an operator's mistake. Nothing takes a copy off the cluster today.
- **A CronJob already runs a MicroVM on a schedule** ([ADR 0025](./0025-a-cronjob-is-a-kind-the-workloads-unit-runs-by-creating-a-job-from-its-template-at-each-scheduled-time-only-while-it-leads.md)), with files from Secrets ([ADR 0020](./0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md)).
- **etcd's client port is open to control-plane Nodes only** and requires a client certificate (ADR 0023). ADR 0004 keeps etcd clients to the API server; a backup needs a second one.
- **A snapshot holds every Secret's data in clear,** since the Store keeps Secrets in clear (ADR 0020).
- **A long-lived AWS key in a Secret is a second thing to rotate and leak,** and the Secret is itself in the Store the snapshot copies.

## Decision

**A CronJob runs `gaon-etcd-snapshot`, a binary of its own unit. Each run takes one snapshot through etcd's maintenance snapshot over mutual TLS, checks it against its own integrity hash, uploads it to an S3 bucket with its SHA-256 as the checksum S3 verifies, and exits 0; any failure exits non-zero. Its AWS credentials are temporary ones from IAM Roles Anywhere, asked for with a certificate of the common name `gaon-etcd-snapshot` that gaon's CA signed.**

1. **The object key is `<prefix><UTC time, 20060102T150405Z>-rev<revision>.db`.** The revision is the one stored in the snapshot.
2. **Retention is the bucket's lifecycle rule,** which the operator owns. The binary deletes nothing.
3. **The etcd client certificate, the Roles Anywhere certificate and their keys reach the MicroVM as `files` from Secrets.** No AWS access key is read or stored anywhere in gaon.
4. **Restore is a procedure, not a binary.** [deployment.md](../deployment.md) §Backup and restore gives it.

## Consequences

- **A failed run is a failed Job,** which the CronJob keeps in its history and the Job's `backoffLimit` retries. No object is left by a failed upload, because S3 stores nothing for a body that differs from the checksum.
- **A corrupt snapshot is not uploaded.** The hash check fails the run before the upload.
- **A single upload caps a snapshot at 5 GiB.** A larger Store needs a multipart upload, which is not written.
- **The bucket holds every Secret in clear,** so its access is the operator's to restrict.
- **The unit has new dependencies:** the AWS SDK for S3 and AWS's Roles Anywhere credential helper.
- **The unit is a second etcd client.** Its certificate is one the etcd members trust for clients.
- **The snapshot is consistent as of one revision,** the one in its key. A restore from it loses what was written after.
- **A snapshot restores to a store holding the keys it was taken with,** shown by the unit's test.

## Signs the decision was wrong

- A snapshot nears 5 GiB.
- A run fails often for a cause the Job's retries do not clear, such as the MicroVM not reaching the etcd client port.
- A restore from an object is found to hold a different store than the cluster had at its revision.
- The operator keeps a long-lived AWS key for the job because Roles Anywhere's trust does not fit the certificates gaon's CA issues.

## Rejected alternatives

- **A long-lived AWS access key in a Secret.** It would sit in the Store the snapshot copies, and need rotation by hand.
- **`etcdctl snapshot save` run by a shell script.** It would add an image with a shell and the tool, and the upload and the checksum would be the script's. A binary links the client and tests the run against a real etcd.
- **A snapshot on a Node's disk, or in the cluster.** It does not survive the loss of the Nodes the backup is for.
- **The unit deleting old objects.** It would need the permission to delete in the bucket; the lifecycle rule needs none, and the role can be limited to `PutObject`.
- **A multipart upload now.** The limit is stated and refused, not silently met.
