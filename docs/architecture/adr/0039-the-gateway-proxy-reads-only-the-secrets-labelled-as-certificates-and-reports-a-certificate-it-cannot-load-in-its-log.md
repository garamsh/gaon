# ADR 0039: The gateway proxy reads only the Secrets labelled as certificates and reports a certificate it cannot load in its log

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Garamsh/gaon#223. [ADR 0032](./0032-the-gateway-is-gateway-httproute-and-tlsroute-kinds-of-their-own-group-a-gateway-unit-registers-and-runs-with-a-loadbalancer-service-and-a-deployment-of-proxies-for-each-gateway.md) has the proxy read the Secret each terminating listener names, and leaves two questions to the proxy's issue: which rule lets it read Secrets, and who says that a Secret holds no valid pair.

- **A rule names no object.** A `get` of an absent object is judged on no labels, so a rule cannot be narrowed to the Secrets a Gateway references ([ADR 0018](./0018-metadata-gains-uid-labels-and-owner-references-lists-take-a-label-selector-lease-and-event-are-new-kinds-and-a-role-rule-may-carry-a-label-selector.md)). A `list` and a `watch` are allowed with a request `labelSelector` that selects nothing outside the rule's.
- **Status is written by the controller only,** and the controller reads no Secret.
- **A proxy's log is read by the operator** who runs the unit, and a Secret's value must not reach it.

## Decision

**The proxy lists and watches only the Secrets that carry the label `gateway.gaon.garam.sh/certificate=true`, under a rule that carries that label selector, and it reports a pair it cannot load in its own log and never in a status.**

1. **The label is the contract.** A Secret a listener's `certificateRefs` names carries `gateway.gaon.garam.sh/certificate: "true"`. The proxy's ClusterRole `gaon:gateway-proxy` allows `list` and `watch` of `secrets` under `labelSelector: gateway.gaon.garam.sh/certificate=true`, and the proxy sends the same selector. A Secret without the label is not read, and the listener fails its handshakes as one with no Secret does: deleting a Secret or removing its label revokes the certificate, and the proxy stops serving it at once.
2. **A pair that does not load.** A Secret that is not among the labelled ones, or whose `tls.crt` and `tls.key` do not parse as a pair, is logged at warning level once for each distinct problem, with the Secret's name and the error text of the standard library, never a value. A Secret that is not among the labelled ones leaves no certificate (Decision 1). A Secret that is present and does not parse leaves the last pair that loaded for it in use; a listener that never had one fails its handshakes.
3. **No status.** The listener's `ResolvedRefs` keeps meaning a well formed reference ([gateway.md](../gateway.md) §Certificates).
4. **Flags.** The proxy takes `-gateway`, `-server` and `-tls-ca`, all required, and `-token-file`, which defaults to the ServiceAccount token path of [ADR 0029](./0029-a-microvms-serviceaccount-token-is-a-file-at-a-fixed-path-and-the-node-agent-replaces-it-and-the-files-that-reference-a-secret-or-configmap-in-place-while-the-microvm-runs.md). They are the arguments the controller's Deployment already passes. The idle timeout of 1 hour and the keep-alive of 60 seconds are fixed and have no flag.

## Why

- **The label is the one narrowing the rule language has.** A rule that names objects cannot be written, and a rule on all Secrets gives a proxy every Secret of the cluster.
- **A log line needs no new write path.** A status written by the proxy would give a second writer to the kinds, which the controller alone writes.
- **Keeping the last pair for bad content** means a renewal that writes a bad Secret does not take a listener down. **Dropping it for a Secret that is gone** keeps deletion and unlabelling usable as the operator's way to revoke a certificate.

## Consequences

- A certificate stays in use after its Secret is overwritten with bad content, and stops being served only when the Secret is deleted or unlabelled.
- An operator who forgets the label sees a log line that the Secret is not among the Secrets labelled for certificates, and the manifest and gateway.md state the label.
- A certificate problem is visible in the proxy's log only, and not in `status`.
- Every certificate Secret of every Gateway is readable by every proxy.

## Signs the decision was wrong

- Operators ask the API for why a listener serves no certificate and find nothing, which would call for the controller to read the Secret or for a proxy status.
- A rule can name an object, which would let the label be dropped.
- A proxy needs its idle timeouts changed per Gateway, which would call for flags or a field.
