# etcd snapshot

What `gaon-etcd-snapshot` does: each run takes one snapshot of the etcd Store, checks it and uploads it to S3. The unit is `etcd-snapshot/` and its binary is `gaon-etcd-snapshot`. It is a client of etcd, not of the public resource API, and imports no other unit's packages. How a CronJob runs it and how a snapshot is restored are in [deployment.md](./deployment.md) §Backup and restore.

Basis: [ADR 0028](./adr/0028-a-cronjob-runs-gaon-etcd-snapshot-which-uploads-a-checked-etcd-snapshot-to-s3-under-iam-roles-anywhere-credentials.md), [ADR 0025](./adr/0025-a-cronjob-is-a-kind-the-workloads-unit-runs-by-creating-a-job-from-its-template-at-each-scheduled-time-only-while-it-leads.md) for the CronJob that runs it and [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md) for the etcd it copies.

## Current decisions

### A run

- **One run is one process.** It takes a snapshot, checks it, uploads it and exits. It runs no loop; the schedule is the CronJob's.
- **The snapshot is etcd's maintenance snapshot,** streamed to a file in a directory made for the run, mode 0600, and flushed to disk. The directory is removed when the run ends, whatever its result.
- **The check is etcd's own integrity hash.** A snapshot file is the database followed by the 32 bytes of its SHA-256; a file whose size is not 32 more than a multiple of 512, or whose hash differs, fails the run before anything is uploaded.
- **The revision is read from the snapshot file,** not from the stream, whose header etcd 3.6.16 does not fill (`etcdutl` snapshot status, in `etcd-snapshot/internal/snapshot/etcd`).
- **The upload is one `PutObject`** with the SHA-256 of the file as the checksum S3 verifies. S3 refuses a body that differs from it and stores no object. A file over 5 GiB is refused by the unit.
- **Exit codes:** 0 for a stored snapshot (and for `-h`), 1 for a failed run, 2 for flags the unit cannot run with.
- **`-timeout`** bounds the whole run, 15 minutes by default.

### The key

- **`<prefix><UTC time as 20060102T150405Z>-rev<revision>.db`,** from the run's clock and the revision in the snapshot. `-s3-prefix` is the prefix, empty by default; one that names a folder ends in `/`.
- **Retention is the bucket's lifecycle rule.** The unit deletes nothing.

### Flags

- **etcd:** `-etcd-endpoints` (default `http://127.0.0.1:2379`), `-etcd-ca-file`, `-etcd-cert-file`, `-etcd-key-file` and `-etcd-dial-timeout` (5s). The same names as `gaon-apiserver`'s; the three files are given all together or not at all, and with them the connection is TLS 1.3 with the client certificate.
- **S3:** `-s3-bucket`, `-region`, `-s3-prefix` and `-s3-endpoint`, which replaces the S3 endpoint and selects path-style addressing, for a stand-in.
- **Credentials:** `-rolesanywhere-cert-file`, `-rolesanywhere-key-file`, `-rolesanywhere-trust-anchor-arn`, `-rolesanywhere-profile-arn`, `-rolesanywhere-role-arn` and `-rolesanywhere-endpoint`, which replaces the Roles Anywhere endpoint. `-region` is also the Roles Anywhere region.
- **Scratch:** `-snapshot-dir`, where the run's directory is made; the system temporary directory by default. It holds the whole snapshot, so it has room for one.

### Credentials

- **The credentials are IAM Roles Anywhere sessions of one hour,** asked for by signing the request with the certificate and its key, through AWS's credential helper (`github.com/aws/rolesanywhere-credential-helper`). The unit reads no AWS access key.
- **The certificate's common name is `gaon-etcd-snapshot`,** signed by gaon's CA, and the unit refuses a certificate with another. The trust policy of the role names that common name.
- **The key is ECDSA or RSA;** another type is refused.
- **The helper reads the AWS environment variables and shared files** when it loads its configuration, so a MicroVM that sets `AWS_*` variables changes what it uses. The CronJob in [deployment.md](./deployment.md) sets none.

### Files

- **Certificates and keys arrive as `files` from Secrets,** read at start. A replaced file takes effect at the next run.

## Rationale

- The CronJob, the unit, the key, the check, the credentials and what was rejected: [ADR 0028](./adr/0028-a-cronjob-runs-gaon-etcd-snapshot-which-uploads-a-checked-etcd-snapshot-to-s3-under-iam-roles-anywhere-credentials.md).

## Open questions

- **The image.** No Dockerfile builds `gaon-etcd-snapshot` and no image is published; the CronJob's `image` is the operator's.
- **Multipart upload.** A Store whose snapshot passes 5 GiB needs it.
- **Encryption of the snapshot.** It holds Secrets in clear; the bucket's server-side encryption and access policy are the operator's.
- **`nodeSelector` is not honoured.** The scheduler does not read it ([scheduler.md](./scheduler.md)), so the MicroVM may run on a Node other than the one the network path in [deployment.md](./deployment.md) §Backup and restore names.
