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
  - [garbage-collector.md](./garbage-collector.md): how the garbage collector in `gaon-controllers` deletes an object once every owner it names is gone, and which objects it leaves alone.
  - [node-agent.md](./node-agent.md): what `gaon-node-agent` does on a node: registration, Lease and fencing renewal, and running bound MicroVMs on mvmd.
  - [cli.md](./cli.md): the `gaon` client CLI, its commands and how `apply` treats a concurrent change.
  - [deployment.md](./deployment.md): which gaon services run on which Node, what a Node's config carries for them, how certificates reach them and which gaon commit is run.
  - [etcd-snapshot.md](./etcd-snapshot.md): how `gaon-etcd-snapshot` takes an etcd snapshot, checks it and uploads it to S3 under IAM Roles Anywhere credentials.
  - [workloads.md](./workloads.md): how `gaon-workloads` registers the `Job`, `CronJob`, `Deployment` and `StatefulSet` kinds, runs a Job's MicroVMs to completion with retries, creates a Job from a CronJob at each scheduled time, keeps a Deployment's replicas and a StatefulSet's named replicas with their Volumes, as a client of the public API.
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
  - [0020](./adr/0020-a-microvm-takes-a-process-ports-files-and-probes-secret-configmap-and-serviceaccount-are-kinds-and-a-token-request-returns-a-jwt-bound-to-a-microvm.md): a MicroVM takes a process, ports, files and probes, Secret, ConfigMap and ServiceAccount are kinds, and a token request returns a JWT bound to a MicroVM.
  - [0021](./adr/0021-the-bootstrap-roles-give-the-scheduler-and-the-controllers-the-coordination-lease-and-the-controllers-read-and-delete-every-resource.md): the bootstrap roles give the scheduler and the controllers the coordination Lease, and the controllers read and delete every resource.
  - [0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md): a Node's certificate carries the Organization `gaon:nodes`, and the signer signs no other Organization.
  - [0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md): the control plane runs on three Nodes over a three-member etcd, and the scheduler and the controllers act only while holding a coordination Lease.
  - [0024](./adr/0024-a-job-is-a-custom-resource-kind-the-workloads-unit-registers-and-runs-to-completion-as-a-client-of-the-public-api.md): a Job is a custom resource kind the workloads unit registers and runs to completion as a client of the public API.
  - [0025](./adr/0025-a-cronjob-is-a-kind-the-workloads-unit-runs-by-creating-a-job-from-its-template-at-each-scheduled-time-only-while-it-leads.md): a CronJob is a kind the workloads unit runs by creating a Job from its template at each scheduled time, only while it leads.
  - [0026](./adr/0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md): a Deployment is a kind the workloads unit keeps replicas of a MicroVM template for, with no ReplicaSet kind and a rollout bounded by surge and unavailability.
  - [0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md): a Service allocates its addresses from two configured ranges, Endpoints lists its ready MicroVMs and a NetworkPolicy selects MicroVMs by label.
  - [0028](./adr/0028-a-cronjob-runs-gaon-etcd-snapshot-which-uploads-a-checked-etcd-snapshot-to-s3-under-iam-roles-anywhere-credentials.md): a CronJob runs gaon-etcd-snapshot, which uploads a checked etcd snapshot to S3 under IAM Roles Anywhere credentials.
  - [0029](./adr/0029-a-microvms-serviceaccount-token-is-a-file-at-a-fixed-path-and-the-node-agent-replaces-it-and-the-files-that-reference-a-secret-or-configmap-in-place-while-the-microvm-runs.md): a MicroVM's ServiceAccount token is a file at a fixed path, and the Node Agent replaces it and the files that reference a Secret or ConfigMap in place while the MicroVM runs.
  - [0030](./adr/0030-a-statefulset-is-a-kind-the-workloads-unit-keeps-named-replicas-of-a-microvm-template-for-each-with-a-volume-it-keeps-when-the-replica-goes.md): a StatefulSet is a kind the workloads unit keeps named replicas of a MicroVM template for, each with a Volume it keeps when the replica goes.
  - [0031](./adr/0031-a-schema-node-may-hold-an-integer-or-a-string-with-x-kubernetes-int-or-string-and-no-type.md): a schema node may hold an integer or a string with `x-kubernetes-int-or-string` and no type.
