# ADR 0029: A MicroVM's ServiceAccount token is a file at a fixed path, and the Node Agent replaces it and the files that reference a Secret or ConfigMap in place while the MicroVM runs

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#162, step D17 of goal garamsh/gaon#117. The decisions are the gaon PM's, taken on that goal; the path is the worker's proposal, for the PM to revise. The audience is configured, since the API server accepts a token only for one of its `--api-audiences`, by default its issuer URL.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **[ADR 0020](./0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md) leaves two things open.** Its Decision 16 leaves "the Node Agent's writing and renewing of the token as a file" to a later issue, and names no path, no audience and no renewal time. Its Decision 2 says references resolve when the MicroVM starts and a change is not seen by a MicroVM that runs.
- **That Decision 2 rested on a fact about mvmd.** The rejected alternative "Live updates of a referenced Secret or ConfigMap" needed "mvmd to rewrite a file in a running guest, which mvmd#437 does not offer".
- **mvmd now offers it.** `lib/runtimeapi` of garamsh/mvmd at commit `223e3eca764caf60b8dd72c7399b83ff6b437d49`, the version `node-agent/go.mod` pins, has `WriteFile`, a client stream of a start message (machine and path) and chunks. Its proto comment says the new content is committed whole at a clean end of the stream and that a stream that breaks leaves the old content. In the guest agent at the same commit (`guest-agent/crates/guest-agent/src/files.rs`, `Replacement`) the content is written to a temporary file in the target's directory, synced, and renamed over the target, so a reader of the path sees the old file or the new one. An existing file keeps its owner, group and mode, so the first copy of a file is made by the create request, which carries a mode and an owner.
- **mvmd re-sends the create request's files at every boot** (mvmd ADR 0051). A restart of the guest is a `StartMachine` of the same machine, so the guest boots holding the token and the referenced values of the create request, which are older than the agent's last write.
- **The Node authorizer allows `get` of a Secret or ConfigMap and no `list` or `watch`** (ADR 0020, Decision 17), so the agent cannot be told of a change.

## Decision

**A MicroVM that names a ServiceAccount has its token as a file at a fixed path, requested by the Node Agent, delivered in the create request and replaced by `WriteFile` before it expires. A file whose `valueFrom` names a Secret or ConfigMap is replaced the same way when the object's value changes. `env` stays fixed at start.**

1. **The path and the file.** The token is the file `/var/run/secrets/gaon.garam.sh/serviceaccount/token`, with mode 0600. Its owner is decided as a file's owner is with no owner given: the MicroVM's `user` when that is a numeric `uid:gid`, root when it gives none, and a `user` that is a name fails the MicroVM as `InvalidSpec`. A `spec.files` entry at that path fails it as `InvalidSpec`. The token counts toward the 64 files and 1 MiB of ADR 0020, Decision 4.
2. **The request.** The agent asks `POST /serviceaccounts/{name}/token` under the Node's identity, with the audience the Node Agent is configured with (`--service-account-audience`), the default lifetime and the MicroVM's name and uid. A ServiceAccount that does not exist fails the MicroVM as `ReferenceNotFound`; any other error is retried.
3. **Renewal.** Before 80% of the lifetime requested has passed on the agent's clock, counted from the agent's own request time, the agent requests a new token and replaces the file with `WriteFile`. The agent's clock and the requested lifetime decide the time, not the answer's `expirationTimestamp`, so the agent and the API server cannot disagree by their clock skew. A refused request or write is retried at every resync.
4. **Expiry is reported.** When the token in the file has expired on the agent's clock and no renewal was accepted, the MicroVM's `Ready` condition is false with the reason `TokenExpired` and a message naming the ServiceAccount, and its phase is unchanged. An accepted renewal clears it. No token appears in a log line, an event, a status message or an error.
5. **Referenced files follow their object.** At every resync the agent reads each Secret and ConfigMap a `files` entry names, with `get`, and replaces the file by `WriteFile` when the value differs from the one it last delivered. A value that cannot be read, or a write that fails, leaves the file as it was and is retried at the next resync. `env` is resolved at start and never again.
6. **After a start the agent writes again.** At every start it initiates after the first, the agent treats the token and the referenced files as stale, since mvmd boots the guest with the create request's, and replaces them at the next resync. After an agent restart it holds no record of what it delivered, and does the same.
7. **No second process runs beside the workload.** The token and the files are written through mvmd's runtime API into the guest's file system, with no agent, sidecar or init process added to the MicroVM.
8. **Supersedes ADR 0020, Decision 2, for `files`.** A file's reference is re-resolved while the MicroVM runs. The decision stands for `env`. The rest of ADR 0020 is not changed.

## Consequences

- **A token or a secret rotated at its source reaches a running workload** within one resync (10 seconds by default) plus the write, with no restart.
- **The agent reads each referenced object at every resync**, one `get` per object per MicroVM, where it read each once at create. The cost is as many requests as referenced objects per resync, per node.
- **The workload must re-read the file.** A process that read the token or a value at start and holds it in memory keeps the old one. Nothing signals it.
- **The first renewal of a restarted MicroVM comes up to one resync after the boot.** Until then the guest holds the create-time token, which expires when the first one would have, and an agent restart forgets that expiry: with the guest's token unknown, an expiry is not reported until a renewal has been accepted once.
- **The token path joins the contract the workload relies on.** Changing it is a new decision.
- **The agent is configured with the audience.** It has no default, since the issuer URL is the API server's; a node whose agent has none does not create a MicroVM that names a ServiceAccount, and retries at every resync.
- **Resolving env and files again at each start remains [#170](https://github.com/garamsh/gaon/issues/170)**, which waits on garamsh/mvmd#519. This decision replaces a file after the start and does not make a start resolve.

## Signs the decision was wrong

- Resync-time reads of Secrets and ConfigMaps load the API server, or a Node authorizer check, enough that a watch the Node may be allowed is wanted instead.
- A workload needs the token at a path of its own choosing, or with an audience of its own, per MicroVM.
- A workload needs an `env` value to change under a running MicroVM, and restarts it only for that.
- A guest reads a file at the moment `WriteFile` renames it and sees a partial one: the commit read at `223e3eca764caf60b8dd72c7399b83ff6b437d49` is then not the atomic rename it was read to be.
- Workloads miss the window between an expired token and the next accepted renewal, often enough that renewal should start earlier than 80% of the lifetime.

## Rejected alternatives

- **A sidecar or init process in the guest that fetches and renews the token.** It adds a process beside every workload and a credential to bootstrap it, where the agent already holds the Node's identity and mvmd writes files.
- **Renewal timed by the answer's `expirationTimestamp`.** It compares the API server's clock with the agent's.
- **Leaving a file as it was written at start.** It is ADR 0020's Decision 2, which the `WriteFile` of mvmd no longer forces.
- **A token that only the create request writes.** It would expire in the guest after its lifetime, and a MicroVM that runs longer than that would lose its identity.
- **Watching a Secret or ConfigMap.** The Node authorizer allows `get` only.
