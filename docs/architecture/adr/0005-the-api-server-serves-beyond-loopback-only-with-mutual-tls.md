# ADR 0005: The API server serves beyond loopback only with mutual TLS

> Status: accepted
> Date: 2026-11-11

Drafted for garamsh/gaon#51, under goal garamsh/gaon#49 and #48. The decisions are the project owner's, recorded in #49 §Decisions. The server flags and the TLS floor are the PM's, taken under #49's grant and recorded on #51.

## Context

- **The API had no authentication**, so `gaon-apiserver` refused any listener that was not on a loopback address ([ADR 0003](./0003-the-v1alpha1-resource-api-fixes-one-writer-per-section-and-a-per-node-lease.md), its Decision 10, "Authentication is deferred"). A Node Agent on another node, the scheduler and the controllers cannot reach a server that listens only locally.
- **Callers differ**: each Node Agent acts for one Node, and the scheduler, the controllers and the CLI each act for themselves.
- **How a Node gets its certificate files** is a deployment decision outside gaon.

## Decision

**A `gaon-apiserver` listener that is not on a loopback address is served only with mutual TLS.**

1. **Mutual TLS, one client certificate per caller.** The server requires a client certificate and verifies it against a client CA (`tls.RequireAndVerifyClientCert`). A Node Agent's certificate carries its Node name as the subject CN. The scheduler, the controllers and the CLI each have their own certificate.
2. **The CN is the caller's identity.** The server does not yet act on it; see 4.
3. **Files come from flags.** The server takes `--tls-client-ca`, `--tls-cert` and `--tls-key`, and every client takes its own CA, certificate and key paths. All three server flags are given or none is, and a partial set stops the server at start. Issuance is out of scope.
4. **Authentication only.** Any authenticated caller may write any path, as on loopback. Which identity may write which section is an open question.
5. **Loopback is kept.** Without the flags the server serves plain HTTP on a loopback address and refuses any other.
6. **The TLS floor is 1.3.**

## Consequences

- Nodes, the scheduler, the controllers and the CLI can reach the server on other addresses once each holds a certificate the client CA signed.
- Whoever deploys gaon owns issuing, distributing and renewing the certificates. A certificate that expires stops its caller.
- The server reads its files at start; replacing them takes a restart.
- A caller holding a valid certificate may write any section of any resource, so a compromised Node Agent can write other Nodes' resources until authorization lands.
- This ends ADR 0003's Decision 10, leaving a loopback-only rule for a server without the flags; the rest of ADR 0003 stands.
- **Rejected: bearer tokens.** A token travels in every request and needs a store and a way to issue and revoke, which are more moving parts than a CA and files; the transport would still need TLS to keep the token private.
- **Rejected: one shared certificate.** It authenticates that a caller is a gaon component and not which, so no later rule can tell one Node from another, and one leaked copy exposes every caller.

## Signs the decision was wrong

- Certificate renewal or distribution is the common cause of outages, and operators bypass TLS or the loopback rule to avoid it.
- A caller that cannot present a client certificate needs the API.
- The CN proves too coarse an identity for the authorization rule that follows.
- Restarting the server to replace its files is shown to cost more than the leases it interrupts.
