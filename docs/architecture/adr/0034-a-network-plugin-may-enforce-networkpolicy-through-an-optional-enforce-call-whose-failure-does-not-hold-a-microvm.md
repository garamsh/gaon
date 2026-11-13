# ADR 0034: A network plugin may enforce NetworkPolicy through an optional Enforce call whose failure does not hold a MicroVM

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Garamsh/gaon#165, enforcement half, with garamsh/gaon#204. [ADR 0027](./0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) says the network plugin enforces a NetworkPolicy and the Node Agent passes it, and does not say how. [ADR 0019](./0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md) has one network call, `Policy`, made once before a MicroVM is created, whose result mvmd takes in the create request.

- **`Policy` cannot carry a NetworkPolicy.** It is asked once per create. A NetworkPolicy changes after a MicroVM runs, and a peer it selects by label is another MicroVM's address, which exists only once that MicroVM runs.
- **ADR 0019 Decisions 2 and 5 make a failed or unserved call hold the MicroVM.** An `Unimplemented` answer is "retried and shown as in Decision 5", and so is any error that is not a refusal: the MicroVM is not created and is `Pending` with `Ready` false and a reason, so that no MicroVM runs before its policy (invariant 6).

## Decision

**A network plugin may implement one more call, `Enforce`. It is declarative and complete, and an enforcement failure does not stop a MicroVM from being created or run.**

1. **The call.** `NetworkService.Enforce(microvms)` carries, for each MicroVM bound to the Node that has an address, its addresses and a limit for each direction. A direction no policy selects the MicroVM for is unset, which is unrestricted; a selected one holds the union of what the selecting policies allow, and an empty one allows nothing. The plugin replaces what it enforced with exactly that, so a removed policy or MicroVM is lifted by the next call. Adding a call keeps old callers working, so the version stays `gaon.plugin.v1alpha1` (ADR 0019 Decision 2).
2. **Optional.** A plugin that does not implement it answers gRPC `Unimplemented`. For `Enforce` that is not Decision 2's "retried and shown as in Decision 5": the agent asks again at the next round, and the MicroVM is neither held nor given a condition.
3. **Its failures hold nothing.** Any other error is logged and retried at the next round, and a MicroVM is created and run whatever `Enforce` answers. This departs from ADR 0019 Decision 5, which holds the MicroVM for a plugin that is not running.
4. **An operator sees an unenforced policy in the agent's log.** When the plugin answers `Unimplemented` while a policy selects a MicroVM bound to the Node, the agent logs a warning at every round, naming that the policy is not enforced. With no such policy it logs at debug level only. Any other error is logged as a warning of the round.

## Why

- **The address exists only once the MicroVM runs.** The limits are computed from `status.addresses`, which the agent writes after the MicroVM starts, so enforcement cannot come before the create and cannot be a precondition of it. Holding the MicroVM on `Enforce` would make a MicroVM that cannot start never get its address and never get its limit.
- **Kubernetes leaves policy to the network plugin.** A cluster whose CNI does not enforce NetworkPolicy runs its pods and does not tell the pod; the API accepts the policy. The same here, with the log line above as the only trace.
- **The create-time policy still holds the line.** Every MicroVM is created with the policy `Policy` returned, which for `default` blocks all outside traffic without prefixes, so the window before `Enforce` is not an open one beyond what the create-time policy allows. `Enforce` only narrows it.

## Consequences

- A MicroVM runs unenforced by NetworkPolicy until its address is written and the next round passes, and for as long as `Enforce` fails. Invariant 6 holds for the create-time policy and not for NetworkPolicy.
- The agent lists the NetworkPolicies each round and does not watch them, so a change reaches the plugin within one resync.
- A plugin author who wants the hold of Decision 5 for enforcement does not have it. A future decision may add it for plugins that declare they enforce.

## Signs the decision was wrong

- An operator runs a policy that is silently unenforced and the warning is not seen, which would call for a status on the NetworkPolicy or the Node.
- A MicroVM must not run unlimited for even one round, which would call for the hold of ADR 0019 Decision 5 on `Enforce`, and for the limit to be known before the create.
