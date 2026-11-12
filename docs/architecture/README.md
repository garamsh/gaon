# Architecture

Current architecture of the system and the decisions behind it. This file is the index; workers read it before touching code, the PM keeps it honest.

## Structure

- **Responsibility documents** (one `.md` per domain or concern, e.g. `memory.md`, `gateway.md`) — the single source of truth for how the system is shaped *now*. To know the current state, read only these.
- **`adr/`** — the record of individual decisions: direction taken, context, rejected alternatives.

## Rules

1. **Responsibility document skeleton**: current decisions (consolidated) / rationale summary with links to the relevant ADRs / open questions. Do not copy ADR content — synthesize the present state.
2. **ADRs are append-only.** Once merged, the body is frozen. Exceptions: updating the status field (`accepted` → `superseded by ADR-XXXX`), fixing typos or broken links, and appending a dated entry under `## Errata` when the decision stands but a fact supporting it was wrong — the erratum names what falsified it, and the original text stays intact. A changed decision means a new ADR that supersedes the old one — never an edit.
3. **Decisions land in pairs.** A PR that adds or supersedes an ADR must update the affected responsibility documents in the same PR. A PR with only one of the two is rejected — the final state must always live in the responsibility documents.
4. Keep this index current: every responsibility document and every ADR is listed here. `structures/` is not — bootstrap deletes it, and an entry for a file that is about to go leaves the index wrong. An ADR covers a decision that changes a project rule — a settled choice that does not affect the rules does not earn one.
5. **Every domain or concern has a responsibility document.** The PR that introduces one adds its document; the PR that removes one deletes it. A system with no code yet has no more than `structure.md` (rule 6), and that is the correct state.
6. **Structural rules live in `structure.md`.** The shape chosen at bootstrap imposes rules — what the unit is, what may reference what, when a new one is earned — and they belong in the responsibility document for that concern, where a reviewer cites one by line and a correction is an ordinary edit. Not the ADR: rule 2 freezes that body, so superseding the choice of shape would be the price of fixing a rule's wording.
7. **A point the structural rules leave to the project is settled in an ADR** — the one recording the shape where the question is met at bootstrap, a later one where it is met later. Which side is taken decides what counts as a defect, so it is a decision under rule 4, and the ADR still holds the record after bootstrap. Nothing obliges a project to settle a question it has not met: until then the point waits under `structure.md`'s open questions, where it binds nothing, and moves into that document's current decisions when the project takes a side.
8. **Structural rules yield only to what a framework imposes.** Where `structure.md` and a convention file decide the same case differently, `structure.md` governs, unless the stack-specific file that applies decides it as its framework imposes it — the alternative fails to build or run, so the project cannot restate it. A stack file's spelling of a structural rule is not that: the rule it spells is `structure.md`'s. The shape chosen at bootstrap is this project's own refinement of what a stack-neutral file states for every project. A project with no `structure.md` meets no such conflict, and this rule asks nothing of it.

## Index

- Responsibility documents
  - [structure.md](./structure.md): the shape gaon is partitioned into, what may reference what, and when a new unit is earned.
  - [resource-api.md](./resource-api.md): the `v1alpha1` resource API contract: writers, state transitions, Lease and fencing, invariants, paths, authorization and resource definitions.
  - [apiserver.md](./apiserver.md): how `gaon-apiserver` serves the resource API over etcd and holds the invariants the contract gives it.
  - [scheduler.md](./scheduler.md): what the Scheduler binds, the placement rule, and when it rebinds.
  - [node-lifecycle.md](./node-lifecycle.md): how the node lifecycle controller confirms Lease expiry and the condition it records.
  - [csr-signer.md](./csr-signer.md): how the signer in `gaon-controllers` decides a CertificateSigningRequest, what it signs and how it is configured.
  - [node-agent.md](./node-agent.md): what `gaon-node-agent` does on a node: registration, Lease and fencing renewal, and running bound MicroVMs on mvmd.
  - [cli.md](./cli.md): the `gaon` client CLI, its commands and how `apply` treats a concurrent change.
  - [deployment.md](./deployment.md): which gaon services run on which Node, what a Node's config carries for them, how certificates reach them and which gaon commit is run.
  - [e2e.md](./e2e.md): how the end-to-end test of M1 runs, what it asserts, and what the host needs.
- ADRs
  - [0001](./adr/0001-the-repository-is-a-go-workspace-of-executable-units-partitioned-by-responsibility.md): the repository is a Go workspace of executable units, each its own module, partitioned by responsibility.
  - [0002](./adr/0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md): gaon owns the Node Agent and depends one way on mvmd and naru, through their APIs.
  - [0003](./adr/0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md): the `v1alpha1` resource API fixes one writer per section and a per-node Lease.
  - [0004](./adr/0004-the-store-is-etcd-in-one-cell-of-one-member.md): the Store is etcd, starting as one cell of one member.
  - [0005](./adr/0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md): the API server serves beyond loopback only with mutual TLS.
  - [0006](./adr/0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md): a gaon deployment is one control-plane Node, its services configured by argv and files, pinned by commit.
  - [0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md): a Node gets its client certificate from gaon's own CA through a CertificateSigningRequest, which a bootstrap token may create.
  - [0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md): the API server authorizes certificate callers with cluster roles, bindings and a Node authorizer.
  - [0009](./adr/0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md): a user registers a resource kind with a CustomResourceDefinition, and the API server serves it under its own group.
  - [0010](./adr/0010-a-user-registers-admission-webhooks-with-webhook-configurations-the-api-server-calls-them-on-every-accepted-write.md): a user registers admission webhooks with webhook configurations, and the API server calls them on every accepted write.
  - [0011](./adr/0011-the-scheduler-places-through-filter-and-score-plugins-and-an-out-of-process-extender-configured-by-a-file.md): the Scheduler places through filter and score plugins and an out-of-process extender, configured by a file.
  - [0012](./adr/0012-the-node-agent-asks-a-network-plugin-and-a-storage-plugin-compiled-into-it-before-it-creates-a-machine.md): the Node Agent asks a network plugin and a storage plugin, compiled into it, before it creates a MicroVM.
  - [0013](./adr/0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md): a Volume is created through its own path, a `local` Volume is a disk its Node's agent makes, and a binding pins it to a Node.
  - [0014](./adr/0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md): the gaon CLI reads like kubectl: `get` lists and reads, `describe` shows, a kind has a plural and a short name, and `list` is removed.
  - [0015](./adr/0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md): the gaon CLI derives the other groups' roots from `--server` and finds a registered kind's plural in the Established definitions.
  - [0016](./adr/0016-the-gaon-cli-reads-its-server-and-credentials-from-a-config-file-of-clusters-users-and-contexts.md): the gaon CLI reads its server and credentials from a config file of clusters, users and contexts.
  - [0017](./adr/0017-the-machine-kind-becomes-microvm-short-name-mvm-in-v1alpha1-in-place.md): the `Machine` kind becomes `MicroVM`, short name `mvm`, in `v1alpha1` in place.
  - [0018](./adr/0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md): metadata gains uid, labels and owner references, lists take a label selector, `Lease` and `Event` are new kinds, and a role rule may carry a label selector.
  - [0019](./adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md): the Node Agent reaches its network and storage plugins as separate processes, through a versioned gRPC API on Unix sockets.
