# ADR 0006: A gaon deployment is one control-plane Node, its services configured by argv and files, pinned by commit

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#58. The decisions are the project owner's, recorded in [the PM's decision comment on that issue](https://github.com/garamsh/gaon/issues/58#issuecomment-6798341752). They answer the questions naru's PM raised on 2026-11-11, before taking on garamsh/naru#19, which carries gaon's binaries in naru's image. Code claims hold for garamsh/gaon at commit `819ccd4af66fcb805e1602ff6d2caef17aa17ad9`.

## Context

- **Where gaon's services run was not written down.** [ADR 0002](./0002-gaon-owns-the-node-agent-and-depends-one-way-on-mvmd-and-naru.md) has naru start gaon's control-plane services and the Node Agent, and leaves the shape of naru's node API open.
- **The Store is one etcd member** ([ADR 0004](./0004-the-store-is-etcd-in-one-cell-of-one-member.md)). `gaon-apiserver` dials etcd without TLS and has no flag for it (`apiserver/config.go`), so etcd must be reachable where plain HTTP is acceptable.
- **The scheduler and the controllers have no leader election.** `grep -rniE "leader|election" --include=*.go` over the repository at the commit above matches only `apiserver/testenv_test.go`, where it sets an embedded etcd member's own election timeout.
- **Callers authenticate with certificates, and issuing them is outside gaon** ([ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md)).
- **Every gaon service binary takes flags.** Of the four services, only the Node Agent also reads environment variables (`node-agent/config.go`).
- **gaon has no tags or releases**, and its API is `v1alpha1`.

## Decision

**M1 is one control-plane Node with one etcd member, whose services take argv and files, run from a commit gaon is pinned at.**

1. **Topology.** Every Node runs `gaon-node-agent`. One control-plane Node runs etcd, `gaon-apiserver`, `gaon-scheduler` and `gaon-controllers`, each as one instance. etcd listens on that Node's loopback, and its data survives a reboot and an upgrade, because losing it loses every resource.
2. **Certificate issuance.** For M1 an operator or tooling issues every certificate file, and naru delivers the files to the Node. The end state is gaon's own CA with a bootstrap and CSR flow.
3. **Service configuration.** A gaon service appears in a Node's config as argv plus files. Environment variables stay a Node Agent fallback only.
4. **Versioning.** naru pins gaon by commit until gaon cuts releases. gaon promises no skew policy between the Node Agent and the API server before `v1beta1`.
5. **Left open:** the CSR flow's design, how the cell grows beyond one member, and the skew policy that `v1beta1` brings.

## Consequences

- The control-plane Node is a single point of failure for the whole cell, as the one member already makes the Store ([ADR 0004](./0004-the-store-is-etcd-in-one-cell-of-one-member.md)). The scheduler and the controllers are not replicated either.
- Running a second scheduler or controllers instance is outside what gaon supports until leader election exists.
- Whoever replaces the control-plane Node's disk, or reinstalls it, must keep etcd's data directory or restore it.
- Until the CSR flow lands, certificate renewal is the operator's work, and an expired certificate stops its caller ([ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md)).
- The CSR flow needs its own ADR. It also decides how a Node gets its first certificate without naru delivering one, which may conflict with a node OS that carries certificates as files and issues none.
- A second etcd member needs its own ADR, as ADR 0004 says.
- A Node Agent and an API server built from different commits carry no promise to work together.
- **Rejected for M1: the CSR flow.** It adds a CA, a bootstrap credential and an approval step before any Node can join, to solve a distribution problem that file delivery already covers.
- **Rejected: environment variables for every service.** The flags are the one interface every binary shares, and a second route per binary is a second thing to keep in sync.

## Signs the decision was wrong

- The control-plane Node's loss, or a lost etcd data directory, is the common cause of outages before the cell has a way to grow.
- Operators cannot keep certificates renewed by hand, and bypass the TLS rule to avoid it.
- A commit pin is too coarse for naru to tell which Node Agent and API server pair works.
- A service needs configuration that does not fit argv and files.
