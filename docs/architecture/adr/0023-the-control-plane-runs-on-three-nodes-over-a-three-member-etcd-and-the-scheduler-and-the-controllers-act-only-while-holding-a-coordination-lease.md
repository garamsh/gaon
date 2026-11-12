# ADR 0023: The control plane runs on three Nodes over a three-member etcd, and the scheduler and the controllers act only while holding a coordination Lease

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#123. It supersedes [ADR 0004](./0004-the-store-is-etcd-in-one-cell-of-one-member.md) as to its single member (its Decision 5 left the growth of the cell open), and narrows [ADR 0006](./0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md) as to the one control-plane Node. The rest of both stands. The Node side, the address that clients reach and the etcd cluster's configuration, is naru's, designed in garamsh/naru#59.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **One control-plane Node is one failure.** Its loss, or its etcd member's, stops every write: Nodes cannot renew their Leases and mvmd stops each Node's MicroVMs when its deadline lapses ([ADR 0004](./0004-the-store-is-etcd-in-one-cell-of-one-member.md), Consequences).
- **The API server holds no state of its own that a second process would not have,** with one exception found in this change: `service.defs`, a read-write mutex in each process that ordered the writes of an object of a user-registered kind against the writes of its definition. A second process does not take the first's mutex, so the order it gave is lost.
- **The scheduler and the controllers each run their loop unconditionally.** Two instances of either act on the same resources: two schedulers bind one MicroVM twice, two sets of controllers race on deletion and on garbage collection.
- **The bootstrap roles already let both write a coordination Lease** ([ADR 0021](./0021-the-bootstrap-roles-give-the-scheduler-and-the-controllers-the-coordination-lease-and-the-controllers-read-and-delete-every-resource.md)); the Lease is `coordination.gaon.garam.sh/v1alpha1`, a holder, a renew time and a duration, written by compare-and-swap on its `resourceVersion`.
- **naru's design input (garamsh/naru#59)** is that clients reach the API server through one address, a virtual IP that naru keeps on a Node where the API server answers, and that the etcd image naru ships follows the etcd version gaon's `apiserver/go.mod` pins.

## Decision

**The control plane runs on three Nodes. Each runs etcd, `gaon-apiserver`, `gaon-scheduler` and `gaon-controllers`. The three etcd members form one cluster. Of the schedulers, and of the controllers, the one that holds the coordination Lease of its role acts, and the others wait.**

1. **etcd endpoints.** Each `gaon-apiserver` is given all three members' client endpoints in `--etcd-endpoints`, comma separated, the same list on every Node. It starts if any one answers, trying each in order with `--etcd-dial-timeout` for each, and fails if none does. The client then reaches the cluster through any member that is up, so a stopped member, the first one included, does not stop a server or one that starts later.
2. **How clients reach the API server.** Through one address, naru's virtual IP. A client has a single `-api` or `--server` value; there is no failover list in any client, and no gaon client changes beyond its configuration. The scheduler and the controllers reach the API server the same way, so a Node that loses its own API server loses nothing.
3. **Leader election.** The scheduler and the controllers each contend for a Lease in the coordination group, `gaon-scheduler` and `gaon-controllers`, read and written through the VIP.
   - **Taking it.** A candidate takes the Lease at once when it is absent or released, that is its holder is empty or is the candidate's own identity. Otherwise it takes it when it has watched the same `resourceVersion` for the whole Lease duration on its own clock. It takes it by compare-and-swap, so of several candidates exactly one succeeds. It reads no time from another host's clock and none from the Lease's `renewTime`.
   - **Holding it.** The holder renews every retry period. It acts only until the start of its last acknowledged renewal plus the renew deadline. A renewal the server did not acknowledge by then, or a Lease another holder wrote, ends its term: the context it gave its loop is cancelled and it waits for the loop to return. It then contends again as a candidate.
   - **Times.** The Lease duration is 15s, the renew deadline 10s and the retry period 2s, set by `--leader-election-duration`, `--leader-election-renew-deadline` and `--leader-election-retry-period`. The duration is a whole number of seconds and the three satisfy retry period < renew deadline < duration; the binary refuses to start otherwise.
   - **What a non-leader does.** It polls the Lease every retry period and does nothing else: it starts no controller, no scheduling pass and no watch of resources. Its process, its API server and its etcd member stay up.
   - **Takeover.** After the holder's last renewal, another instance takes over no sooner than the duration and no later than the duration plus two retry periods, 19s by default: one poll to see the last renewal and one to act after the duration has run on its own clock. The holder stopped acting at most the renew deadline after that renewal began, so there is no instant at which both act, to the extent the clocks run at the same rate.
   - **Releasing it.** A holder that is stopped, or whose loop fails, writes the Lease with an empty holder, and another instance then takes it on its next poll.
   - **Identity and name.** `--leader-election-identity` names the instance and defaults to the host name and a random suffix, so a restart is a new identity; `--leader-election-name` changes the Lease. `--leader-elect=false` runs without election and is for a deployment with one instance.
4. **The etcd version.** `gaon-apiserver` is built against etcd 3.6.16 and links the etcd client of that release. It must match naru's etcd image: naru takes its pin from `apiserver/go.mod`, so a bump of one is a bump of both in the same change. The 3.7.3 bump that dependabot proposes (garamsh/gaon#134, #135) is not taken here and is a change of its own, with naru's.
5. **The API server holds no per-process ordering.** The write of an object of a user-registered kind is ordered against the write of its definition by etcd instead of by a mutex:
   - An object's create is one transaction that creates its key and compares the definition's key at the revision the request read. A definition that was changed or marked deleted in between fails the create with an internal retry that reads the definition again, five times at most, then `Conflict`. Once a definition is marked deleted no create of its objects can store, so the removal that follows an empty list of its objects leaves none.
   - A definition's create is one transaction that creates its key and compares the key `/locks/customresourcedefinition-names` at the revision read before the other definitions were listed. Of two creates that read the same set, one stores and the other reads again and sees it, so of definitions of one group that share a name exactly one is accepted. The key lies outside the prefix of the definitions, so no list or watch of them reads it.
   - Marking a definition deleted and removing it read again on a conflict, and treat a definition already marked or already removed, by this process or another, as done.
6. **Per-process state, audited.** Nothing else the API server holds is shared between its requests or between its processes: webhook configurations, definitions and RBAC objects are read from etcd at each request; the TLS and token files are read at each process's start and are the same files on every Node; the bootstrap roles are created if absent and an existing one is left; the deletion timestamp is the server's clock, the three Nodes' clocks agreeing to within the skew a Node's NTP keeps.
7. **No fencing beyond the margin.** A write the old holder made just before its term ended may reach etcd after the new holder began. The Store has no token that would refuse it. The renew deadline being below the duration bounds how long the old holder may keep acting, not what it already sent. The scheduler's binding and the controllers' writes are conditioned on the versions they read ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md)), so a stale write fails and a stale pass is repeated.

## Consequences

- **One Node can stop** and the control plane keeps serving: etcd keeps a quorum of two, the VIP moves to a Node whose API server answers, and a scheduler and a set of controllers on the others take over within the takeover time.
- **A takeover is not instant.** The control loops are paused between 15s and 19s by default. Nodes keep their Leases through the API server, which stays up, so a lost leader loses no Node.
- **Two Nodes stopped at once stop the Store.** With two of three members down etcd has no quorum, so every write is refused until one returns.
- **Stopping a leader by `SIGTERM` is fast and a crash is slow.** A released Lease is taken on the next poll; a crash waits the duration.
- **The clients are unchanged.** `-api`, `--server` and the config of the CLI each name the VIP.
- **A bump of etcd is a bump on two repositories,** so the gaon change names naru's.
- **A third process of the API server is as correct as the second.** The tests run three over one etcd and over a three-member cluster; a test with the first member stopped shows a server starting over the others.
- **The retry of a create against its definition is bounded.** A creator that sees the definition change five times in a row gets `Conflict`, as one that loses a version race does.
- **Rejected: a failover list in every client.** The VIP gives one address for them all and it is naru's to keep.
- **Rejected: a server-side fence on the Lease** (a write refused unless the writer still holds it). It would put the Lease's rules in the Store's write path for every resource, and the conditioned writes already make a stale pass harmless.
- **Rejected: a lock key per definition name** rather than one for all. Conflicts are between definitions that share a kind, plural or singular with different names, which a per-name key does not see.
- **Rejected: etcd's own election (the concurrency package).** It would link a second coordination mechanism beside the Lease the contract already has, and give the scheduler and the controllers a direct etcd client, which ADR 0004 keeps to the API server.

## Signs the decision was wrong

- A scheduler or a set of controllers acts twice in one instant, shown by two holders in a Lease's history, or a MicroVM bound twice.
- A takeover takes longer than the duration plus two retry periods, because the poll is slower than the retry period.
- The 15s pause in scheduling is longer than operators accept, and the times are changed in every deployment.
- Three Nodes are not enough, or a stale write from an old holder lands a second effect that the conditioned writes do not stop.
- The etcd versions of gaon and naru differ in a shipped deployment.
