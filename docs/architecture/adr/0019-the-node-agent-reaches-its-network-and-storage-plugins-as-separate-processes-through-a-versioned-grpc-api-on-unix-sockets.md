# ADR 0019: The Node Agent reaches its network and storage plugins as separate processes, through a versioned gRPC API on Unix sockets

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#121, part of goal #117, whose decision D1 is the PM's. It supersedes the compiled-in decision of [ADR 0012](./0012-the-node-agent-asks-a-network-plugin-and-a-storage-plugin-compiled-into-it-before-it-creates-a-machine.md) and keeps the rest of it: which calls the agent makes, in which order, and what a refusal and a release mean. The behaviour is in [node-agent.md](../node-agent.md) §Plugins, and how a plugin is installed on a Node in [deployment.md](../deployment.md) §Plugins.

## Context

- **ADR 0012 named its own sign.** Its signs list "a third party needs to supply a plugin and rebuilds the agent to do it", and it rejected separate processes only until "a need for a third-party plugin is met". The owner's decision 2 of aurora#749, as recorded in #117 D1, is that extensions are separate processes reached through public APIs, so that a third party adds a network or a storage system without changing the core, and the built-ins are shown to be extensions by being delivered that way. ADR 0012's sign is therefore taken as met.
- **What ADR 0012 said a protocol needs, and where each is now decided:** a wire format and its versioning (Decision 1 and 2), a place on the node for the plugins (Decision 3), and a failure mode for a plugin that is not running (Decision 5). The identity a plugin uses to reach the resource API, which ADR 0012 did not need because the plugin was the agent, is Decision 6.
- **The agent called the plugins in process.** The `Network` and `Storage` interfaces, `Refusal`, the built-in `default` network and the `local` storage were packages of `node-agent/`, and `--network-plugin` and `--storage-plugin` chose among those compiled in.
- **Units reach each other only through a published API** ([structure.md](../structure.md) §Boundaries and dependency direction), and a package two units need is a library under `lib/`.

## Decision

**The agent reaches each plugin as a process of its own, by gRPC over a Unix socket in a directory it and the plugin share. The API, `gaon.plugin.v1alpha1`, is a library module, `lib/plugin/`, and the two built-in plugins are binaries that import nothing of `node-agent/`.**

1. **The calls.** The proto package has two services:
   - `NetworkService.Policy(microvm)` returns the `NetworkPolicy` the MicroVM is created with, or a `Refusal`.
   - `StorageService.Attach(microvm)` returns the `DiskAttachment`s the MicroVM is created with, or a `Refusal`; `StorageService.Release(machine_id)` gives back what `Attach` readied.

   Their meaning is ADR 0012's, unchanged: the order of calls, a `Refusal{reason, message}` failing the MicroVM with that reason and message and mvmd not being asked to create it, any other error being retried, `Attach` repeatable for the same MicroVM and epoch, `Release` repeatable and called for an identifier `Attach` never saw. A refusal is a field of the response and not a gRPC error, so a plugin's own failure, which gRPC reports as `Unknown`, is never read as a verdict. The MicroVM travels as the JSON of the resource API's `MicroVM`, which `lib/api` already versions, so the plugin API does not duplicate the resource's fields. The policy and the disks travel in messages of their own that mirror mvmd's runtime messages, and the agent converts them to mvmd's; a plugin does not import mvmd's client to answer the agent.
2. **Versioning.** The version is the proto package, `gaon.plugin.v1alpha1`, and the Go package `github.com/garamsh/gaon/lib/plugin/v1alpha1`. A change that keeps old callers working adds a field or a call; any other change is a new package, `v1alpha2` or `v1beta1`, served beside it for as long as a release supports both. An agent that calls a version a plugin does not serve gets gRPC `Unimplemented`, which is not a refusal and is retried and shown as in Decision 5. Before `v1beta1` there is no skew promise between an agent and a plugin, as there is none between the agent and the API server ([deployment.md](../deployment.md) §Versioning).
3. **The socket directory and discovery.** A plugin serves the socket `<dir>/<kind>-<name>.sock`, `kind` being `network` or `storage` and `name` matching `[a-z0-9]([a-z0-9-]*[a-z0-9])?`. The directory is `/run/gaon/plugins` and is chosen by `--plugin-dir` of the agent and of each plugin. `--network-plugin <name>` and `--storage-plugin <name>` of the agent name the plugin found at that path, so discovery is by name and path and there is no registry and no scan. The socket is mode 0600, so only the user the plugin runs as, which is the agent's, reaches it. A plugin removes a stale socket when it starts, and refuses to start when a process still answers on it. An empty name is no plugin: the agent creates a MicroVM with the empty policy, which mvmd reads as isolated, and attaches no disk, and a MicroVM that names a Volume fails with `NoStoragePlugin`.
4. **Nothing plugin-specific is compiled into `node-agent/`.** The agent holds the client of `lib/plugin`, and the interfaces it calls are those of `lib/plugin`. The built-ins are `network-default/` (binary `gaon-network-default`, the network `default` with `--egress-prefix` and `--ingress-prefix`) and `storage-local/` (binary `gaon-storage-local`, the storage `local` with the status loop and the disk deletion of [ADR 0013](./0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md)). Each is a module of its own that requires `lib/api` and `lib/plugin`, and `storage-local/` mvmd's `lib/runtimeapi` for the disk calls. `--egress-prefix` and `--ingress-prefix` leave the agent for `network-default`.
5. **A plugin that is not running.** The agent does not connect at start and does not stop for an absent plugin. A call to a socket nobody serves fails with gRPC `Unavailable`, which is not a refusal:
   - the MicroVM is **not created**: no `CreateMachine` is made, so it never runs before its policy, which keeps invariant 6 of [resource-api.md](../resource-api.md);
   - the call is **retried** at every resync, and a plugin started later answers the next one;
   - the reason is **visible in the MicroVM's condition**: `phase` `Pending` and `Ready` false with the reason `NetworkPluginUnavailable` or `StoragePluginUnavailable` and a message naming the error, written while the call fails and replaced when it succeeds.

   Each call is bounded by 10 seconds, so a plugin that accepts the connection and does not answer delays one round and no more. The same holds for a plugin whose answer is an error that is not a refusal.
6. **The identity a plugin uses for the resource API.** A plugin that reads or writes resources, as `local` does for Volumes, acts as the Node it serves. It presents the Node Agent's own client certificate, whose common name is the Node's name, and is authorized by the Node authorizer of [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md) as that Node is, which is what the compiled-in plugin had.
   - When the agent keeps its pair with `--cert-dir`, `storage-local` is given the same `--cert-dir` and `--tls-ca` and reads `node-agent.pem` at each new connection, so a pair the agent renews is presented after the renewal with no restart of either.
   - When the agent is given `--tls-cert` and `--tls-key`, the plugin is given the same files, which are read once at its start, as the agent's are.
   - With no TLS the API server is on loopback and the plugin uses plain HTTP, as the agent does.
   - The plugin and the agent run as one user on one Node, so the plugin has the Node's authority and no more; a third-party plugin that needs the resource API therefore has the Node's. That is wider than a plugin needs and is the cost of this decision.

## Consequences

- **A third party supplies a network or a storage system without rebuilding the agent**, by serving the API from a binary on the Node and being named by flag. A test of the agent runs one written for the test and sharing nothing with the agent but `lib/plugin`.
- **A Node has more services to start.** An agent with a plugin named needs that plugin started, in either order. deployment.md says how naru installs one.
- **The agent's default plugins are none.** ADR 0012 defaulted the network to `default` and the storage to `none`, both compiled in; the agent now defaults both to no plugin, so a node that wants policy or `local` volumes starts the plugin and names it. A MicroVM on a Node with no network plugin is isolated.
- **A plugin's identity is the Node's.** The plugin API carries no identity of its own, and a plugin that needs the resource API takes the agent's certificate. A narrower identity needs a certificate subject for a plugin that the signer approves and the Node authorizer scopes to a Node; that is not built here and is for garamsh/gaon#122.
- **`Attach` still runs again at each retry**, and a volume `Attach` readied can still leak when the agent stops between `Attach` and `CreateMachine`. These consequences of ADR 0012 stand.
- **Calls now cost a round trip and can fail for reasons a call in process could not**, which is why a failed call is shown in the condition and not only logged.
- **The MicroVM travels as JSON**, so a plugin that decodes it with an older `lib/api` ignores fields it does not know and cannot see the ones it needs.
- **Rejected: the agent starting the plugins.** Who starts a Node's services is naru's ([ADR 0002](./0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md)), and an agent that spawns processes takes on supervision, restart and logs for them.
- **Rejected: a plugin registry or a scan of the directory.** A name given by flag is checkable at start and says which plugin an agent uses; a scan makes the plugin set depend on what is in a directory.
- **Rejected: the refusal as a gRPC status code.** A plugin that fails reports `Unknown`, and a refusal in the status would be indistinguishable from a bug in the plugin.
- **Rejected: a protocol of our own over the socket.** gRPC gives the wire format, versioning by package and the `Unavailable` and `Unimplemented` signals, and the repository already uses it for mvmd's API.

## Signs the decision was wrong

- A plugin that does not run for long stretches leaves MicroVMs `Pending` that operators then bind elsewhere by hand.
- Plugin authors need the agent's own state, such as the epoch of a restart or the Node's capacity, and add it to the MicroVM's annotations.
- A plugin cannot be given a narrower identity and a third party asks for one.
- A change to the plugin API is needed that no added field or call can make, twice before `v1beta1`.
- The agent's calls to a plugin on a busy Node time out at 10 seconds in normal operation.
