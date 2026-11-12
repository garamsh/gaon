# Scheduler

What the Scheduler binds, the placement rule and its plugin points, and when it rebinds: the responsibility of `scheduler/`, binary `gaon-scheduler`.

Basis: [resource-api.md](./resource-api.md) §MicroVM, §Node, §Volume, §Lease and fencing and §Paths, with the writers of [ADR 0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md) and the pinning of [ADR 0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md). The Scheduler reaches the API only through `lib/api`, and never reads the Store.

## Current decisions

### What it does

- Each pass lists the Nodes, the MicroVMs and the Volumes, and binds each MicroVM that needs a binding through `POST /microvms/{name}/binding`. It is the only writer of `/binding`.
- **It acts only while it holds the Lease `gaon-scheduler`** of the coordination group, on every Node of the control plane but one at a time. A scheduler that does not hold it polls the Lease and runs no pass; one that loses it stops its loop and contends again, and a term starts with a new service, so no pass takes anything from the term before. The Lease, its times and the flags: [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md) and [deployment.md](./deployment.md).
- It polls: `gaon-scheduler` runs a pass at start and then every `-interval` (default 5 seconds). A pass holds no state from the one before.
- It reaches the API over plain HTTP, or over mutual TLS 1.3 when `-tls-ca`, `-tls-cert` and `-tls-key` are all given, with a client `lib/api` builds. Given one or two of them, or a file that cannot be read or does not fit, it exits with a usage error naming what is missing. The flags have no environment variable. Calls time out after 30 seconds either way.
- A MicroVM with `deletionTimestamp` set is left alone.
- A list of Volumes that is `Forbidden`, as on a cluster whose `gaon:scheduler` role predates `volumes` (ADR 0013), fails the pass with that error once the rest of it has run: a MicroVM that names no Volume is bound as usual, and a MicroVM that names one is left alone, with no condition written, until a list succeeds. Any other failure to list Volumes ends the pass, as for Nodes.

### Placement rule

- A Node is eligible only if its `status.state` is `Admitted`, its `Ready` condition is `True`, and its `LeaseExpired` condition is not `True`. A missing condition is not `True`.
- A Node's free capacity is `status.capacity` cpu and memory, less the `spec.resources.requests` cpu and memory of every MicroVM whose `spec.nodeName` is that Node, whatever its phase.
- A MicroVM fits a Node when its requests are no more than the free capacity in both cpu and memory. Quantities are compared by value (`1Gi` holds `1G`, `1` holds `1000m`). An empty quantity is zero. These two rules are the built-in filters below.
- The MicroVM goes to the Node with the highest score among those that pass every filter, ties by Node name. With no score plugin every score is 0, so it is the first fitting eligible Node by name. A binding made in a pass is charged to its Node for the rest of that pass.
- A MicroVM's Volumes are read from `spec.volumes` by name. The built-in filter `VolumeNode` keeps only the Node the MicroVM's `local` Volumes are pinned to by `spec.nodeName`, and passes no Node when a named Volume does not exist or has `deletionTimestamp` set, or when its `local` Volumes are pinned to two different Nodes. A MicroVM none of whose Volumes is pinned, or that names none, is not filtered. The API server refuses the same bindings as `Conflict` or `Invalid` (ADR 0013); the filter keeps the Scheduler from proposing them. The MicroVM is unbound and reported as in [§A MicroVM that fits no Node](#a-microvm-that-fits-no-node).
- Nothing else is read: no spreading, affinity, priority, preemption or overcommit, except what a configured score plugin or extender adds ([§Plugins and extenders](#plugins-and-extenders)).

### Plugins and extenders

Decided in [ADR 0011](./adr/0011-the-scheduler-places-through-filter-and-score-plugins-and-an-out-of-process-extender-configured-by-a-file.md).

- **Plugin points.** A filter plugin passes a Node or gives the reason it does not fit. A score plugin gives each Node that passed every filter an integer from 0 to 100. A Node's score is the sum of each score plugin's score times its weight.
- **Built-ins.** `NodeEligible`, `NodeResourcesFit` and `VolumeNode` are filters that always run, in that order, and cannot be disabled, so a Scheduler started without `--config` runs all three; they are the eligibility, fit and Volume rules above. `LeaseExpired` eligibility carries invariant 1, so it is not configurable. `LeastAllocated` is a score plugin, off unless configured: for cpu and for memory, the capacity left free after the MicroVM times 100 over the capacity, rounded down (0 where the capacity is 0), then the two averaged and rounded down.
- **Configuration.** `--config <file>` is YAML with `apiVersion: scheduler.gaon.garam.sh/v1alpha1`, `kind: SchedulerConfiguration`, `scorePlugins` (`name`, `weight`) and `extenders`. It is read once at start ([ADR 0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md)); the flag has no environment variable. Without it there is no score plugin and no extender. An unreadable file, a second YAML document, another `apiVersion` or `kind`, an unknown field, an unknown or repeated plugin, a weight below 1, or a bad extender exits with a usage error. A weight defaults to 1.
- **An extender** has `urlPrefix`, `filterVerb`, `prioritizeVerb` (each optional, at least one required), `weight`, `timeout` (default 5s), `ignorable`, and for https `tlsCAFile` with optional `tlsCertFile` and `tlsKeyFile` (both or neither). It is called in file order.
  - Filter: `POST <urlPrefix>/<filterVerb>` with `{"microvm": <MicroVM>, "nodeNames": [...]}` naming the Nodes that passed the built-ins and earlier extenders. The answer is `{"nodeNames": [...], "failedNodes": {"<name>": "<reason>"}, "error": "<text>"}`. A Node in neither list is refused with the reason `not passed`; a name not sent, or a non-empty `error`, fails the call.
  - Prioritize: the same `POST` to `<urlPrefix>/<prioritizeVerb>` with the Nodes that passed every filter, answered `[{"host": "<name>", "score": 0..10}]`. An omitted Node scores 0; a score outside 0 to 10 or an unsent host fails the call. Each score is multiplied by the extender's `weight` and by 10 and added to the Node's score.
  - The extender never binds.
- **Transport.** https needs a CA, trusts only it, and presents a client certificate when one is given (TLS 1.3). Plain http is accepted only to a loopback IP address. Any other scheme is refused.
- **Failure.** A timeout, connection or TLS error, status other than 200, undecodable body or refused answer fails the call. An `ignorable` extender is then skipped for that pass. Otherwise the MicroVM is not bound this pass: an unbound MicroVM gets `Scheduled` `False` with reason `ExtenderError` and the error as message, and a bound MicroVM waiting for a rebinding is left bound with its status alone.

### Binding and epochs

- A binding names the Node, the MicroVM's `resourceVersion` as read, and the MicroVM's current `spec.bindingEpoch` plus one. The first binding of a MicroVM is epoch 1.
- The binding is a compare-and-swap, so two schedulers racing on one MicroVM produce one binding. The one that loses gets a `Conflict`, which is not a failure: the next pass reads the MicroVM as it then is. A `NotFound` is treated the same way.

### Rebinding

- A MicroVM that has `spec.nodeName` is rebound only when the Node it names has `LeaseExpired` `True`. The node lifecycle controller sets that condition; the Scheduler never reads a Lease and never measures expiry.
- A MicroVM whose Node is not expired, has no `LeaseExpired` condition, has it `False` or `Unknown`, or no longer exists, is left bound.
- The new epoch is the current one plus one, so it is greater than every earlier binding of that MicroVM.
- A MicroVM with a `local` Volume pinned to the Node it is bound to is not rebound, even when another Node fits it: `VolumeNode` passes no other Node, and the expired Node is not eligible. The data stays on that Node's disk until the Node returns; the API server would refuse such a binding as `Conflict` (ADR 0013).
- A MicroVM that fits no other Node stays bound to the expired Node and is tried again next pass. Its status belongs to the dead Node's epoch and is not rewritten.

### A MicroVM that fits no Node

- It stays unbound. The Scheduler writes `PUT /microvms/{name}/status` with a `Scheduled` condition, status `False`, reason `Unschedulable`, whose message gives how many Nodes are eligible of how many exist, the requests that fit none, and how many Nodes each filter or extender refused with the reason, in a stable order. It sets `phase` to `Pending` when the status has none.
- A pass whose condition is identical writes nothing.
- It writes an unbound MicroVM's `/status` as the controller [resource-api.md](./resource-api.md) §Common rules names for "the rest". An unbound MicroVM runs on no Node. This follows Kubernetes, where the scheduler writes `PodScheduled`. Decided in the [PR #18 review](https://github.com/garamsh/gaon/pull/18#pullrequestreview-5853380290).
- A `Scheduled` condition left from before a binding is void once the MicroVM is bound: binding raises `spec.bindingEpoch`, so the old status reads as `Pending` ([resource-api.md](./resource-api.md) §MicroVM). The Node Agent's first status at the new epoch replaces it, and no reader acts on the stale condition. Decided in the [PR #18 review](https://github.com/garamsh/gaon/pull/18#pullrequestreview-5853380290).

## Rationale

- Rebinding waits for a confirmation made by another unit because Lease expiry is measured by an observer ([resource-api.md](./resource-api.md) §Lease and fencing), and invariant 1 depends on it.
- One scheduler acts at a time because two would bind one MicroVM from the same view; the binding's compare-and-swap already makes the second fail, and the Lease keeps the second from trying: [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md).
- A pass that rebuilds the view from two lists, instead of watching, needs no cache to keep consistent, and the binding's compare-and-swap makes a stale view harmless.

## Open questions

- **Driver limits.** garamsh/mvmd#306 has merged, but the Node schema carries no Driver limits, so placement stays within Node capacity and a Create mvmd refuses shows in the MicroVM's status.
- **Node `storage` and the root disk.** `status.capacity.storage` and `spec.rootDiskSize` are not compared; only requests are.
- **`nodeSelector` and `requiredCapabilities`.** The Node schema has no labels or capabilities to match them against, so they are not read.
- **Watching instead of polling,** when the poll interval becomes a cost or a delay worth removing.
