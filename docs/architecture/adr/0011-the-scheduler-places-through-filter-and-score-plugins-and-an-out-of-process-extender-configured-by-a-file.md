# ADR 0011: The Scheduler places through filter and score plugins and an out-of-process extender, configured by a file

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#86, under goal garamsh/gaon#74. The design is the PM's, taken under the project owner's decisions in #74 (Kubernetes semantics, a gaon-native implementation, additive only) and recorded in #86 §Design. It extends the placement rule of [scheduler.md](../scheduler.md) without changing binding, rebinding or the writers of [ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md). Items marked as the drafter's were not in #86 §Design and are listed in the pull request for the PM.

## Context

- **Placement is one fixed rule.** The Scheduler binds a Machine to the first eligible Node by name whose free cpu and memory cover the requests. It cannot prefer an emptier Node, and a site cannot add a rule of its own without a change to the Scheduler.
- **`make e2e` expects today's placement** with no configuration, so the new points must change nothing when none is given.
- **Invariant 1 rests on eligibility.** A Machine is rebound only after the node lifecycle controller sets `LeaseExpired`, and a Machine is placed only on a Node that is Admitted, Ready and not expired ([scheduler.md](../scheduler.md) §Placement rule). A plugin that could be switched off would put that rule in a configuration file.
- **The Scheduler's files are read at start.** [ADR 0006](./0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md), Decision 3, configures a service by argv and files and gives environment variables no route into a service. The Scheduler does not watch a file.
- **An extender is an HTTP client the Scheduler builds.** [ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md) has the API server serve beyond loopback only with mutual TLS and serve plain HTTP on loopback only; the Scheduler's own API client is built the same way from a CA, certificate and key.
- **The semantics follow Kubernetes' documentation, read on 2026-11-12.** Each page was fetched through a tool that returns a summary of the text rather than the text, so what follows is what those summaries stated:
  - "Scheduling Framework", <https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/>. The page text is 105740 characters; characters 0 to 100000 and then the rest were each fetched, so all of it came through a summary. The first fetch showed no footer; the fetch of the tail showed "Last modified July 04, 2025". The page says "Stable since Kubernetes v1.19" and "Beta since Kubernetes v1.37" for one feature, and its version selector lists v1.33 to v1.37.
  - "Scheduler Configuration", <https://kubernetes.io/docs/reference/scheduling/config/>. The text is 111348 characters; 0 to 100000 and then the rest were fetched, and one summary stopped at the plugin list, so the plugin descriptions were not all seen. The footer reads "Last modified June 24, 2026". It says "Stable since Kubernetes v1.25", its version selector lists v1.33 to v1.37, and its example names `kubescheduler.config.k8s.io/v1` and `KubeSchedulerConfiguration`.
  - "Scheduler Extender", <https://github.com/kubernetes/design-proposals-archive/blob/main/scheduling/scheduler_extender.md>, a design proposal in an archive repository that was archived on 2021-12-02. It states no date and no Kubernetes version. About 10000 characters including the page's own chrome came through one fetch, whose summary judged it complete; the document body ends with the `ExtenderBindingArgs` definition.
  - What was taken from them:
    - a filter plugin is called per Node, in the configured order, and the first one that marks the Node infeasible stops the rest for that Node;
    - the scheduler combines the Nodes' scores from all plugins "according to the configured plugin weights" and selects the Node with the highest weighted sum;
    - a score plugin is enabled by name with a `weight`, and `NodeResourcesFit` has the scoring strategy `LeastAllocated`, which is the default;
    - the configuration file is passed as `kube-scheduler --config <filename>`, has an `apiVersion` and a `kind`, and the extender's filter receives the Nodes that passed the built-in predicates and its prioritize the Nodes that also passed the extender's filter;
    - an extender has a `urlPrefix`, a `filterVerb` and a `prioritizeVerb`, each verb optional, a positive integer `weight` that multiplies its scores before they are added to the built-in ones, an `httpTimeout`, and TLS client settings;
    - the extender, not the scheduler, binds when it has a `bindVerb`.
  - What the pages do not state, and so is not taken as Kubernetes' behaviour:
    - the range of a score plugin's score (the Framework page says only "a well defined range of integers"), and the formula of `LeastAllocated` and its resource weights; both are the PM's design in #86;
    - how a tie in the weighted sum is broken; gaon breaks it by Node name so that a pass is repeatable;
    - the fields of the extender's filter and prioritize answers, and the range of its scores;
    - an `ignorable` flag, and the effect of any failure other than a timeout: the proposal says a filter timeout fails the Pod's scheduling and a prioritize timeout is ignored;
    - when the configuration file is read, and whether extenders are configured in it;
    - Kubernetes' behaviour for an unknown field, an unknown plugin or a weight below 1.
  - Where gaon departs from Kubernetes on purpose: the two filters cannot be disabled; there is no `bindVerb`; a failed prioritize call is not ignored unless the extender is `ignorable`; the file is a gaon kind in a gaon group, with a smaller field set; extender scores are 0 to 10 and scaled by 10 onto the plugins' 0 to 100.

## Decision

**The Scheduler picks the Node with the highest weighted sum of filter-surviving score plugins and extender scores, through two always-on filter plugins, optional score plugins and extenders named in a file given by `--config`, and with no file it places as it did.**

1. **Plugin points.** A filter plugin answers, for a Machine and a Node, pass or a reason the Node does not fit. A score plugin gives each Node that passed every filter an integer from 0 to 100. A Node's score is the sum of each score plugin's score times its weight. The Machine goes to the Node with the highest score, ties broken by Node name, so with no score plugin it is the first fitting Node by name.
2. **Built-in plugins.**
   - `NodeEligible` (filter): the Node's `status.state` is `Admitted`, its `Ready` condition is `True` and its `LeaseExpired` condition is not `True`.
   - `NodeResourcesFit` (filter): the Machine's cpu and memory requests are no more than the Node's free capacity, which is its `status.capacity` less the requests of every Machine whose `spec.nodeName` is that Node, whatever its phase, and less the bindings made earlier in the pass. It runs on a Node only after `NodeEligible` passed it.
   - `LeastAllocated` (score): for cpu and for memory, the capacity left free after the Machine, times 100 over the capacity, rounded down, and 0 where the capacity is 0; the two are averaged and rounded down.
   - The two filters always run and cannot be disabled, because eligibility carries invariant 1. `LeastAllocated` runs only when the configuration names it.
3. **Configuration.**
   - The file is given by `--config`, with no environment variable. It is YAML with `apiVersion: scheduler.gaon.garam.sh/v1alpha1` and `kind: SchedulerConfiguration`, and holds `scorePlugins` (each a `name` and an optional `weight`) and `extenders`.
   - No file means no score plugin and no extender.
   - The file is read once, at start (ADR 0006); a change takes a restart.
   - These stop the start with a usage error: a file that cannot be read or is not one YAML document, another `apiVersion` or `kind`, a field the kind does not define, a score plugin that does not exist (`NodeEligible` and `NodeResourcesFit` are not score plugins), a plugin named twice (the drafter's), a weight below 1 on a plugin or an extender, and an extender that breaks item 5. An omitted weight is 1.
4. **Extender protocol.**
   - For the filter verb, the Scheduler `POST`s JSON to `<urlPrefix>/<filterVerb>`: `{"machine": <the Machine>, "nodeNames": [...]}`, naming the Nodes that passed the built-in filters and every extender before it. The extender answers `{"nodeNames": [...], "failedNodes": {"<name>": "<reason>"}, "error": "<text>"}`: the names that pass, the names that fail with a reason each, and an optional error. A Node sent and in neither list is refused, with the reason `not passed`. A name the answer passes that was not sent fails the call.
   - For the prioritize verb, it `POST`s the same arguments to `<urlPrefix>/<prioritizeVerb>`, naming the Nodes that passed every filter, and gets `[{"host": "<name>", "score": 0..10}]`. A Node the answer omits scores 0. A score outside 0 to 10, or a host that was not sent, fails the call. Each score is multiplied by the extender's weight and by 10 to sit on the plugins' scale.
   - Either verb may be omitted; an extender with neither is refused at start (the drafter's). Extenders are called in the order the file lists them, and one is not called once no Node is left.
   - The extender never binds; the binding stays the Scheduler's compare-and-swap.
5. **Extender transport.**
   - An `https` `urlPrefix` needs `tlsCAFile`, trusts that CA and no other root, and presents a client certificate when `tlsCertFile` and `tlsKeyFile` are both given, with a TLS 1.3 floor, as the Scheduler's API client does. One without the other is refused.
   - A plain `http` `urlPrefix` is accepted only when its host is a loopback IP address; a host name is refused, because a name can resolve outside the machine, and TLS files given with it are refused.
   - Any other scheme is refused. Each extender has a `timeout`, default 5 seconds, that must be positive.
6. **Extender failure.**
   - A failed call is a timeout, a connection or TLS error, a status other than 200, a body that does not decode, an answer item 4 refuses, or a non-empty `error`.
   - A failed call skips the extender when it is `ignorable`.
   - Otherwise the Machine is not bound in that pass. An unbound Machine gets `Scheduled` `False` with reason `ExtenderError` and the error as the message; a bound Machine waiting for a rebinding is left bound, as when it fits no Node.
7. **Unschedulable message.** The `Unschedulable` message keeps its count of eligible Nodes of all Nodes and the requests, and adds how many Nodes each filter or extender refused, with the reason, in a stable order so that a pass whose reasons repeat writes nothing.
8. **Binding and rebinding are unchanged** ([scheduler.md](../scheduler.md) §Binding and epochs, §Rebinding).

## Consequences

- **With no `--config` the Scheduler places as before.** Its tests pass unchanged, and `make e2e`'s failover test sees the same placement. The `Unschedulable` message's wording changes: it no longer says that no eligible Node has free cpu and memory, because an extender can be the one that refused.
- **A site adds a placement rule by running a service.** It needs no change to the Scheduler, and the extender sees only the Machine and Node names, so it reads Node facts from the API itself.
- **A non-ignorable extender that is down stops placement of unbound Machines**, each reported `ExtenderError`, until it answers or the file marks it `ignorable` and the Scheduler restarts. Rebinding stops with it for a Machine whose placement needs the extender, which delays failover.
- **A pass costs one call per extender verb per Machine**, over HTTP with a timeout of at most the configured value, so a slow extender slows the pass. The Scheduler binds Machines one at a time.
- **The wire types are in `scheduler/`**, documented in [scheduler.md](../scheduler.md). An extender written in Go in another unit would copy them or wait for them to move to `lib/` ([structure.md](../structure.md)).
- **A file change takes a restart,** and a restart of the one Scheduler leaves unbound Machines unbound meanwhile.
- **Rejected: letting the file disable `NodeEligible`.** It would let a configuration place a Machine on a Node whose Lease expired, which breaks invariant 1.
- **Rejected: an extender that binds.** The binding's compare-and-swap and epoch are the Scheduler's, and a second binder would need its own fencing.
- **Rejected: an environment variable for the file.** ADR 0006 gives a service argv and files.
- **Rejected: ignoring a failed prioritize call, as the Kubernetes proposal does for a timeout.** A site that depends on an extender's scores wants to see its failure, and `ignorable` gives the other behaviour by choice.
- **Rejected: a host name for a plain `http` extender.** Only an IP literal proves the loopback rule from the file alone.

## Signs the decision was wrong

- Sites run an extender only to express a rule that every site wants, such as spreading, so it belongs in a built-in plugin.
- Extenders need the whole Node list or Node facts the Scheduler already holds, and each one re-reads the API.
- A non-ignorable extender's outage is the common cause of unplaced Machines, and operators mark every extender `ignorable`.
- Restarting the Scheduler to change its file costs more than the Machines it leaves unplaced.
- An extender in another gaon unit needs the wire types, and they are copied.
- Scores of 0 to 10 are too coarse for a site's ranking.
