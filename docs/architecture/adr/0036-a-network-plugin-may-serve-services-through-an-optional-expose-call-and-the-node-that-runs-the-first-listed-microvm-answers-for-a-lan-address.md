# ADR 0036: A network plugin may serve Services through an optional Expose call, and the Node that runs the first-sorting listed MicroVM answers for a LAN address

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Garamsh/gaon#196. [ADR 0027](./0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) allocates a Service's cluster IP and a LoadBalancer Service's LAN address and has the endpoints controller list the ready MicroVMs, and says the network plugin serves them. It does not say how the agent passes them, how a Service with no ready MicroVM behaves, or which Node answers for a LAN address when several could. [ADR 0034](./0034-a-network-plugin-may-enforce-networkpolicy-through-an-optional-enforce-call-whose-failure-does-not-hold-a-microvm.md) settled the same questions for NetworkPolicy.

- **Every Node has the same Services and Endpoints.** A cluster IP is reached from a MicroVM on any Node, so every Node translates it.
- **A LAN address is reached from outside the cluster.** The LAN asks for it by ARP or NDP, and more than one Node answering gives the LAN two answers.
- **A Node's agent cannot list the Nodes.** The Node authorizer lets it read its own Node and not the others, so a rule that needs the set of Nodes cannot be decided by an agent.

## Decision

**A network plugin may implement one more call, `Expose`, declarative and complete. A Service with no ready endpoint refuses connections. The one Node whose name sorts first among those running the Service's listed MicroVMs answers for its LAN addresses.**

1. **The call.** `NetworkService.Expose(routes, announce)` carries a route for each port of each Service that has a cluster IP, and a route for each LAN address of a LoadBalancer Service. A route has its address, protocol, port and the ready backends (address and target port) of the Service's Endpoints for that port, and says whether its address is a LAN one. `announce` holds the LAN addresses this Node answers for. The plugin replaces what it served with exactly that, so a removed Service or endpoint is withdrawn by the next call. The version stays `gaon.plugin.v1alpha1` (ADR 0019 Decision 2).
2. **Optional, and a failure holds nothing.** A plugin that does not implement it answers gRPC `Unimplemented`. As for `Enforce` in ADR 0034 Decisions 2 to 4, the MicroVM is neither held nor given a condition, `Unimplemented` is logged as a warning only while a Service has a route and otherwise at debug level, and any other error is logged as a warning of the round and retried at the next.
3. **A headless Service has no route.** A Service without a cluster IP is not translated.
4. **No ready endpoint refuses.** A route with no backend is refused at the Node: a TCP connection is reset and a UDP packet is answered with an ICMP port unreachable, so the client fails at once and does not wait for a timeout. The refusal holds for the cluster IP and the LAN address.
5. **Which Node answers.** For a LAN address, the Node whose name sorts first among the Nodes running a MicroVM that the Service's Endpoints lists, found from the MicroVMs' `spec.nodeName`. When Endpoints lists none, the Node whose name sorts first among those running any MicroVM, so that the Service still refuses at one Node. Every agent reads the same MicroVMs and Endpoints, so exactly one Node concludes it answers, with no coordination. A Node that runs no MicroVM never answers.
6. **A LAN connection returns through its Node.** A connection that came to a LAN address is masqueraded on the way out, so a backend on another Node replies to the answering Node and not to the client directly.

## Why

- **The plugin is told what is in force, not the changes.** Complete state is repaired by the next round if a call is lost, as `Enforce` is.
- **Refusing is better than dropping.** A client of a Service with no endpoint learns that at once; a silent drop looks like a network fault.
- **The rule needs only what every agent has.** Nodes cannot be listed, but MicroVMs and Endpoints can, so the choice among Nodes is a pure function of them. Choosing among the Nodes that run the backends keeps the answering Node on the path the traffic takes anyway.

## Consequences

- When the answering Node's agent is down while its MicroVMs still show ready, no Node answers and nothing takes over until the endpoints controller removes them.
- A LAN address is announced only from a Node that runs a MicroVM, so a cluster with none announces none.
- The plugin owns a configured pool of LAN addresses on a configured interface, and a LoadBalancer address outside it is not announced and is an error of the round.
- A LAN client's connection is seen by a backend as coming from the answering Node.
- The agent lists Services and Endpoints each round and does not watch them, so a change reaches the plugin within one resync.

## Signs the decision was wrong

- A LAN address goes unanswered for longer than an operator accepts after its Node's agent stops, which would call for a liveness input to the rule.
- Backends need the client's address, which would call for a return path that does not masquerade.
- An agent is given the right to list Nodes, which would allow a choice that does not depend on where MicroVMs run.
