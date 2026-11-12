# ADR 0012: The Node Agent asks a network plugin and a storage plugin, compiled into it, before it creates a Machine

> Status: superseded by [ADR 0019](./0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md)
> Date: 2026-11-12

Drafted for garamsh/gaon#90. The design is the PM's, recorded in #90 §Design. It fills the gap [node-agent.md](../node-agent.md) §What the agent does not do yet named: a Machine was created with no network policy and no disks, so invariant 6 of [resource-api.md](../resource-api.md), that no Machine runs before its network policy is applied, was not kept by the agent. The boundary is [ADR 0002](./0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md): gaon reaches mvmd only through its runtime API.

## Context

- **mvmd's runtime API takes both in the create call.** At garamsh/mvmd commit `3f6e60567966819ba920709a1fe84e9a3848a523`, runtime API `v1alpha2`:
  - `CreateMachineRequest` has `network_policy`, a `NetworkPolicy` of `egress` and `ingress` lists of `NetworkRule` (`prefix`, `protocol`, `ports` as `PortRange{first, last}`), and `disks`, a list of `DiskAttachment` (`disk_id`, `read_only`).
  - The policy is the Machine's for its life. An empty policy blocks all traffic to and from outside the node, so a Machine created without one is isolated and not open.
  - `runtimeapi.Dial` validates each request on the client and refuses an unparseable prefix, a port outside 1 to 65535, a first port above the last, ports on a rule that is not TCP or UDP, and an ICMP protocol that does not match its prefix's family. A refused request never reaches mvmd.
  - The statements of node-agent.md about mvmd otherwise hold at the commit it names; only these two messages are taken from the newer one.
- **The Machine's spec names what the plugins need.** `spec.networks` names networks and `spec.ports` the ports it serves; both are in the contract and nothing read them.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12:**
  - "Network Plugins", <https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/network-plugins/>. The footer reads "Last modified July 30, 2024"; the body says "version 1.3 through to the latest 1.37", and its version selector lists v1.33 to v1.37. The fetch returned a summary of the text, whose article body is about 7000 characters; it was read in one fetch, with the page's navigation.
  - "Volumes", <https://kubernetes.io/docs/concepts/storage/volumes/>, its `csi` section. The footer reads "Last modified September 24, 2026"; its version selector lists v1.33 to v1.37, and the section names CSI ephemeral volumes as stable since v1.25. The page is 155957 characters long. It was read in two fetches that each returned a summary, characters 0 to 100000 and then the rest; the `csi` section is in the second, and the sections before `emptyDir` were read only for the introduction and `configMap`.
  - What was taken from them:
    - a network is a plugin's concern and not the container runtime's core: Kubernetes asks a CNI plugin, through the runtime, to set up a pod's network, and the plugin is chosen by configuration on the node;
    - a CSI driver is named by `driver`, a volume by `volumeHandle` and `readOnly` marks it read-only;
    - the `csi` section names RPCs for attaching and mounting a volume that are separate from the pod's own start, which is why the agent's storage call is separate from mvmd's create; the section does not say when they run.
  - What the pages do not state, and so is not taken as Kubernetes' behaviour:
    - whether a network plugin may refuse a pod, or what becomes of a pod it refuses; the network page links a troubleshooting page and says nothing more;
    - which component assigns a pod's address;
    - how network policy is enforced; the page only links to Network Policies;
    - when the CSI calls are made, in what order, and what the `csi` section says of a failed call: nothing, apart from naming `ControllerPublishVolume`, `NodeStageVolume`, `NodePublishVolume` and the others;
    - an example of a `csi` volume in a pod.
  - Where gaon departs from Kubernetes on purpose: plugins are compiled in and not separate processes the agent calls through a protocol, and a plugin's refusal fails the Machine, which Kubernetes' pages do not describe.

## Decision

**The agent has two plugin interfaces, `Network` and `Storage`, inside `node-agent/`. It calls both before `CreateMachine` and puts their results in that one request. The plugins are chosen by flag, from those compiled in.**

1. **`Network.Policy(ctx, machine)`** returns the `NetworkPolicy` the Machine is created with. **`Storage.Attach(ctx, machine)`** readies the Machine's volumes and returns its `DiskAttachment`s, and **`Storage.Release(ctx, id)`** gives back what `Attach` readied for an mvmd machine identifier.
2. **Order.** For a Machine to create, the agent calls `Policy`, then pulls the image, then calls `Attach`, then `CreateMachine` with the policy and disks, then `StartMachine`. The policy is therefore in the create request, and the Machine is never started without it, which is how the agent keeps invariant 6.
3. **Refusal.** A plugin returns a `*machines.Refusal{Reason, Message}` for a Machine it does not serve. The Machine is `Failed`, with its `Ready` condition false, the plugin's reason and its message, and mvmd is not asked to create it. Any other error is not a verdict, and the agent retries at the next resync.
4. **Release.** `Release` is called with the mvmd machine identifier after the agent has deleted the machine from mvmd, and after mvmd rejected a create for which `Attach` had run. It is repeatable and is also called for an identifier `Attach` never saw, because the agent removes machines left by an earlier run. A `Release` that fails after a delete keeps the removal pending, and the next resync removes again.
5. **Flags.** `--network-plugin`, default `default`, `GAON_NETWORK_PLUGIN`; `--storage-plugin`, default `none`, `GAON_STORAGE_PLUGIN`. A name that is not compiled in stops the start, naming the plugin and the ones there are.
6. **Built-in network `default`.**
   - A Machine whose `spec.networks` is empty or holds only `default` is served. Any other network is refused with the reason `UnknownNetwork`.
   - Egress is one rule for each `--egress-prefix`, for any protocol. Ingress is, for each port of `spec.ports` and each `--ingress-prefix`, a TCP rule for that port.
   - `--egress-prefix` and `--ingress-prefix`, with `GAON_EGRESS_PREFIX` and `GAON_INGRESS_PREFIX`, repeat, and the variable holds a comma-separated list. A flag given replaces its variable. Both are empty by default, which gives an empty policy. A prefix that is not an IPv4 or IPv6 CIDR stops the start.
   - A port outside 1 to 65535 is refused with the reason `InvalidSpec`, because mvmd's client would refuse the request and the agent would retry it for ever.
7. **Built-in storage `none`.** It attaches no disk, refuses nothing and releases nothing.
8. **Unchanged:** identity in mvmd, run, remove, restart, failure, the epoch rule and the renewal loop.

## Consequences

- **A Machine created by the default plugins is isolated** until the operator gives prefixes. That is mvmd's reading of an empty policy and the safe start; a node that wants its Machines reachable sets the flags.
- **The default network's rules are per node, not per Machine.** Every Machine on the node gets the same prefixes, and only the ports differ. A rule per Machine needs a field the contract does not have.
- **Ingress is TCP only.** A UDP port cannot be opened by the default plugin; the contract's `spec.ports` is a list of numbers without a protocol.
- **A volume `Attach` readied can leak** when the agent stops between `Attach` and `CreateMachine` and the Machine is then never created again, since `Release` is called only for a machine id the agent removes. A plugin that holds a resource should be able to find and drop what it does not need; the agent has no call for that.
- **`Attach` runs again at each retry**, so it must be repeatable for the same Machine and epoch.
- **Rejected: plugins as separate processes.** A protocol between the agent and a plugin needs a wire format, versioning, a place on the node for the binaries and a failure mode for a plugin that is not running, none of which any document designs. Compiled-in plugins have none of these, and a plugin is added by a change to the agent. Kubernetes' network page has the container runtime load separate CNI plugins; the choice here is the smaller one until a need for a third-party plugin is met.
- **Rejected: a refusal as an error the agent retries.** A Machine that names a network the node does not serve never will, and retrying hides the reason from the user, who sees it only in the `Ready` condition.
- **Rejected: calling the plugins after `CreateMachine`.** mvmd takes the policy and disks in the create call, so a call after it would start a Machine without them.
- **Not enforced beyond the node.** The Scheduler does not place by network or volume, so a Machine naming a network the node does not serve is bound there and fails.

## Signs the decision was wrong

- Operators want a different policy per Machine, or a UDP port, and put it in annotations or in a patched plugin.
- A third party needs to supply a plugin and rebuilds the agent to do it.
- Volumes leak because the agent stopped between `Attach` and `CreateMachine`, and operators clean them by hand.
- Machines fail with `UnknownNetwork` because the Scheduler binds them to nodes that do not serve their network.
