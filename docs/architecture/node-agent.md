# Node Agent

What `gaon-node-agent` does on a node: it registers the node, keeps its Lease and mvmd's fencing deadline renewed, runs the Machines bound to it on mvmd, and reports their status.

Basis: [resource-api.md](./resource-api.md) for the contract it keeps, [ADR 0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md) for the boundary. The mvmd statements below hold for garamsh/mvmd at commit `3b37892f303a4d4dbd8e5360906d0f9c036b562e`, runtime API `v1alpha2`, which the agent reaches only through mvmd's `lib/runtimeapi` module over mvmd's Unix socket.

## Current decisions

### Configuration

Every value is a flag with an environment variable as its fallback. A flag given wins over the variable, and the variable over the default. The agent refuses to start when a required value is missing or the durations do not fit each other, defaults included: the defaults satisfy both inequalities below.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--api-server` | `GAON_API_SERVER` | none, required | root URL of the resource API: the API server's address with `/apis/compute.gaon.garam.sh/v1alpha1`, which [resource-api.md](./resource-api.md) §Paths are relative to |
| `--tls-ca` | `GAON_TLS_CA` | none | file of the CA certificates the API server's certificate must chain to |
| `--tls-cert` | `GAON_TLS_CERT` | none | file of the client certificate the agent presents to the API server |
| `--tls-key` | `GAON_TLS_KEY` | none | file of the key of the client certificate |
| `--node-name` | `GAON_NODE_NAME` | none, required | name of this node's Node and Lease |
| `--mvmd-socket` | `GAON_MVMD_SOCKET` | none, required | path of mvmd's runtime API socket |
| `--delta` | `GAON_DELTA` | 1s, provisional | δ, how long a Machine can run past mvmd's recorded deadline expiry |
| `--lease-duration` | `GAON_LEASE_DURATION` | 30s | validity of the Lease after a renewal, a whole number of seconds |
| `--renew-period` | `GAON_RENEW_PERIOD` | 5s | time between renewal rounds |
| `--deadline-length` | `GAON_DEADLINE_LENGTH` | 15s | length each deadline renewal asks mvmd for |
| `--rpc-timeout` | `GAON_RPC_TIMEOUT` | 2s | bound on each call of a renewal round |
| `--resync-period` | `GAON_RESYNC_PERIOD` | 10s | time between two reconciliations of the Machines and two reports of the Node's status |
| `--restart-backoff` | `GAON_RESTART_BACKOFF` | 10s | wait before the first restart of a Machine, doubled for each restart after it |
| `--restart-backoff-max` | `GAON_RESTART_BACKOFF_MAX` | 5m | cap on the wait between two restarts, not below the backoff |
| `--restart-reset-after` | `GAON_RESTART_RESET_AFTER` | 10m | how long a Machine must have run for the wait to start over at the backoff |

The three TLS values are given together or not at all. All three give a mutual TLS 1.3 client built by `lib/api` that trusts only the CA file; none gives plain HTTP, for a loopback API server; one or two refuse the start, naming the ones missing, as does a file that cannot be read or does not fit. The agent builds its client once at start, so a replaced file is read at the next start.

δ's default of 1s is provisional: it is not measured, in mvmd either, and it is far above a real-time thread's wake-up and a SIGKILL (mvmd ADR 0003). The durations must satisfy two inequalities, checked at start: the deadline length exceeds the renew period plus two call timeouts, so the deadline never lapses between rounds, and the Lease duration is at least the deadline length plus δ plus two call timeouts, so a round can always renew the deadline.

### Registration

- The agent creates its Node with an empty `spec`, by a `PUT` without a version, when the API has none, and never writes `spec` again.
- It writes the Node's `status` by compare-and-swap on every resync: `state` is `Admitted` when a user has set `spec.admitted` and `Discovered` otherwise, `capacity` comes from mvmd's runtime info (cpus as a whole number, memory and storage in bytes, storage being mvmd's state total), and the `Ready` condition is true while mvmd answers. When mvmd's runtime info fails, the agent writes `Ready` false with reason `RuntimeUnreachable` and the error as the message, keeps the stored `capacity`, and keeps the condition's transition time while its status and reason are unchanged.
- It creates the Lease named as the Node on its first round and renews it by compare-and-swap, writing the node as holder.

### Finalizer

- The agent's finalizer is `compute.gaon.garam.sh/node-agent`.
- **Added** to `metadata.finalizers` before the agent first creates or starts a Machine bound to its Node, so a Machine that is not yet finalized is never run.
- **Removed** once a Machine bound to its Node with `deletionTimestamp` set is released on mvmd. Released means mvmd holds no machine the agent named after it, under any epoch, by the round's listing of mvmd less what the round removed. A failed stop or delete keeps the finalizer, and the next resync tries again. A Machine bound to another Node keeps the finalizer, since that Node's agent releases it.
- Both are `PUT /machines/{name}` by compare-and-swap on the version read, changing only the agent's own entry of `metadata.finalizers` and never another's. A conflict is read again and retried, up to three times a round. The API server removes the Machine on the write that clears its last finalizer.

### The fencing obligation

- **One loop** renews the Lease first and then, in the same round, mvmd's deadline.
- **Validity of the Lease** is counted from the start of the latest acknowledged renewal call on the agent's monotonic clock, as [resource-api.md](./resource-api.md) states.
- **A deadline renewal is sent only if, were mvmd to receive it as late as the call timeout allows, its expiry plus δ is within that validity.** That is the clock reading now, plus the call timeout, the deadline length and δ, no later than the validity. This bounds a renewal whose acknowledgement never arrives, which resource-api.md leaves to this document: mvmd may have recorded it, and it was recorded no later than the call timeout after it was sent.
- **When the Lease cannot be renewed**, validity stops growing, the check refuses every further deadline renewal once the margin is gone, and mvmd's deadline lapses on its own. The Machines are fenced by mvmd, not by the agent's survival. A failed Lease call makes the agent read the Lease again before the next write.
- **Every call of a round is bounded** by the call timeout, including the read of mvmd's last applied number, so a runtime that does not answer ends the round with an error and the next round renews the Lease.
- **The renewal number** sent to mvmd increases monotonically; the agent reads mvmd's last applied number once at start, so a restarted agent continues the sequence.
- mvmd refuses to start or restore a Machine while its deadline is not armed or has expired; the agent treats that refusal as a failure to retry on the next resync, and the Machine stays `Pending`.

### Machines

- **Watched set:** the agent lists the Machines, watches from the list's version and reconciles on every change and every resync. A failed or expired watch is followed by a new list.
- **Identity in mvmd:** a Machine is held in mvmd as its name with the binding epoch appended. A binding at a later epoch is therefore a different mvmd machine, and the earlier one is removed, never reused.
- **Epoch rule:** the agent keeps the newest epoch it has seen for each Machine and ignores an event, a list entry or a re-read carrying an older one.
- **Run:** a Machine bound to this node, not being deleted and not `OnDemand`, has its image pulled by digest, is created in mvmd and started, according to `runStrategy`: `Halted` is stopped and not created, `Once` is not started again after it stopped, unless the agent stopped it for `Halted` and `runStrategy` is now `Once`, which starts it, the others are started whenever mvmd holds them created, and restarted as below when it holds them stopped or faulted.
- **Remove:** every mvmd machine the agent named that is not a Machine bound here now, whether unbound, bound elsewhere, bound at an older epoch, being deleted or no longer in the API, is stopped and deleted. This also clears what an earlier run left. An mvmd machine whose identifier the agent did not form is left alone.
- **Status:** the agent writes `phase`, `bindingEpoch` equal to the spec's, `observedGeneration`, `addresses`, `restartCount` and a `Ready` condition, only when they differ from what is stored. `lastExit` is never written: mvmd reports no exit time (garamsh/mvmd#331).
- **Restart:** under `Always` a Machine that stopped, and under `RerunOnFailure` one that failed, is started again after a wait and not before. The wait is the backoff, doubled for each restart and capped at the maximum, and starts over once the Machine has run for the reset time. A clean exit under `RerunOnFailure` is not restarted. A faulted Machine is stopped first, since mvmd accepts only a stop in that state. A `Halted` Machine resumed is started at once and is not a restart. A Machine the agent holds stopped for `Halted` is started at once when `runStrategy` becomes `Once`, and its next exit is judged afresh. The agent knows it stopped the Machine for `Halted` from its memory, so after an agent restart a `Halted` Machine switched to `Once` is not started.
- **Failure:** the agent reads the entrypoint's record from mvmd, exec `entrypoint`, once when it first sees the Machine stopped, under `Once` as under the restarting strategies. The exit failed when the record is `EXITED` with a non-zero exit code or a terminating signal, when there is no `EXITED` record, or when the Machine is faulted; an `EXITED` record with code 0 and no signal is clean. A read that returns no record before the call timeout is a failure; any other error leaves the exit unjudged until the next resync.
- **`restartCount`:** each restart adds one, written by compare-and-swap with the Machine's epoch checked, so a count of an earlier epoch is never carried into a later one. A write that conflicts is read again and retried, and a restart whose write failed is written by the next round, never counted twice.
- **Where the state lives:** the wait, the streak and the judged exit are kept in the agent's memory per mvmd machine, so a rebinding starts with none; an agent restart forgets them, and a Machine it finds stopped then waits the first backoff. The exit time is not known (garamsh/mvmd#331), so the reset time is measured from when the agent started the Machine or first saw it running to when it first sees it stopped, which can lag the exit by up to one resync.

| mvmd state | Phase | Ready |
|---|---|---|
| created | `Pending` | false |
| running | `Running` | true |
| paused | `Suspended` | false |
| stopped, `Halted` | `Stopped` | false |
| stopped, `Once`, clean exit | `Succeeded` | false, not started again |
| stopped, `Once`, failed exit | `Failed` | false, reason `ExitFailed` and a message naming the exit, not started again |
| stopped, `RerunOnFailure` after a clean exit | `Succeeded` | false, not started again |
| stopped, other | `Pending` | false, started again once the restart wait is over |
| faulted | `Failed` | false, the reason is mvmd's last failure reason; stopped and started again once the restart wait is over |

- **Failures the agent settles itself** go to `Failed` with the `Ready` condition false and a reason: `InvalidSpec` for an image that is not `<repository>@sha256:<digest>` or resources it cannot convert, and `Rejected` for a create mvmd refuses, such as one above a Driver's `max_vcpus`, with mvmd's reason as the message. The Driver's limit is not on the Node's status.
- **Outcome kind `IN_PROGRESS`** is treated as never produced by mvmd (garamsh/mvmd#307).

### What the agent does not do yet

- `OnDemand` Machines, wake and suspend.
- Volumes and attachment, and the network policy: a Machine is created with none, so invariant 6 is not kept by the agent.
- The Node's `addresses`.
- Calling naru.

## Rationale

- One loop for the Lease and the deadline keeps the obligation checkable in one place: the deadline is never renewed outside a round that has just seen the Lease's validity.
- The call-timeout margin pays for not knowing whether an unacknowledged call reached mvmd, instead of asking mvmd afterwards, which a failing path may not allow.
- The restart state is in memory because nothing the contract defines can hold it, and losing it costs one backoff, not a missed restart.
- A clean record equal to the previous one counts as a failure because the cost of a wrong restart is one backoff, and of a wrong stop is a Machine that never runs again.
- The exit is read once, at the first sight of the stopped state, so a log that changes afterwards cannot change the verdict.
- The restart settings are flags with the recorded defaults because the contract fixes no values and a node's operator is the one who knows its workloads.
- The epoch in the mvmd identifier makes rebinding to the same node a replacement rather than a reuse, so state of an older epoch cannot carry into a newer one.

## Open questions

- **Entrypoint record across a start:** mvmd's documents do not say whether the console log an entrypoint's record is answered from is cleared when a Machine starts. The agent treats a clean record equal to the one it read at the Machine's previous exit as a failure, so a stale record cannot end the restarts; a Machine switched from `Always` to `RerunOnFailure` whose entrypoint keeps exiting cleanly is therefore restarted, at the backoff.
- **Node addresses:** which addresses the agent reports.
