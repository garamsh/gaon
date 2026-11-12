# ADR 0022: A Node's certificate carries the Organization gaon:nodes, and the signer signs no other Organization

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#122, step of goal garamsh/gaon#117, decision D12. It supersedes [ADR 0007](./0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) as to the subject of a Node's certificate, which Decision 4 gives the signer's approval and Decision 5 gives the agent's request, and in no other part. It narrows the refusal that [ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md) §Consequences, "The signer must refuse an Organization in a request's subject", puts on the signer: the signer still refuses every Organization but one. ADR 0008's Decision stands.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **A Node's certificate today is a CN and nothing else.** The signer approves a subject of exactly one CN, so a Node Agent's certificate cannot be told from any other certificate whose CN is a name, by anyone who reads the subject.
- **A verifier outside gaon needs to tell a Node from the other identities the cluster CA signs.** The one named in the goal is an external condition on the certificate's subject, such as a cloud provider's role-trust policy for pulling images, which can match an Organization and has no way to know a Node's name.
- **An Organization is a group to the API server** ([ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md), Decision 1). A signer that signs the Organization a requester chooses lets a token holder obtain `O=gaon:admins`, so the signer must decide which Organization it signs.
- **The Node authorizer keys on the CN** and on the Node existing ([ADR 0008](./0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md), Decision 5). It reads no Organization.

## Decision

**A Node Agent requests, and the signer signs, a subject of exactly one CN, the Node's name, and exactly one Organization, `gaon:nodes`. The signer refuses a subject with any other Organization, none, or more than one.**

1. **The agent's request.** Its subject is `CN=<node name>` and `O=gaon:nodes`, for the bootstrap and for every renewal.
2. **The signer's subject rule.**
   - The subject has exactly two attributes: one CN and one Organization.
   - The Organization is `gaon:nodes`. Any other value, a missing Organization, a repeated one or an attribute of another type is `Denied` with the reason `InvalidSubject`.
   - The CN rule is unchanged: non-empty, not starting with `bootstrap:`.
   - The certificate carries the subject it was asked for, so `gaon:nodes` appears in a certificate only where the signer approved it. The signer approves only a Node Agent's request ([ADR 0007](./0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md), Decision 6), so it issues no certificate for another identity.
3. **A renewal changes the subject.** A Node Agent holding a certificate issued before this decision, with no Organization, renews over it as before, because the requester rule compares the CN only. The new request carries the Organization, so the renewed certificate has it. The agent renews at the point ADR 0007, Decision 5, gives, and is not restarted.
4. **Authorization does not change.** The Node authorizer and RBAC read the CN and the Organization as before. `gaon:nodes` is bound to no role, so a certificate with `O=gaon:nodes` and a CN that is not an existing Node is allowed nothing beyond what any such CN had.
5. **The name is a constant in `lib/api`**, `api.NodeGroup`, because the signer in `controllers/` and the agent in `node-agent/` are separate modules that both write it and a mismatch denies every request.

## Consequences

- **A Node's certificate is told from the others the CA signs** by `O=gaon:nodes`. The scheduler's, the controllers', the administrator's and the CLI's certificates are operator-issued with their own Organizations and never carry it, unless an operator issues one with it.
- **The signer and the agent change together.** A signer with this rule denies a request without the Organization, so an agent from before this decision cannot bootstrap against it, and a renewal from one is denied too. Agents running a commit before this change keep their pair until it expires and must be upgraded before then. A deployment pins gaon by commit and promises no skew between its services ([deployment.md](../deployment.md) §Versioning).
- **A certificate issued before this decision lacks the Organization until its Node renews it,** which is two thirds into its validity at most 8760h, so a verifier that requires `gaon:nodes` refuses a Node until then. A Node restarted onto a new agent does not renew early, because a pair with no Organization is still valid to the agent.
- **A token holder cannot obtain a group.** Whoever holds a token can still obtain a certificate for any Node name (ADR 0007, Consequences), now with `gaon:nodes`, which no role grants.
- **An operator-supplied Node certificate with no Organization still works,** because `--tls-cert` and `--tls-key` are not issued by the signer, and it is not told from other callers by the verifier.
- **An operator can bind a role to `gaon:nodes`,** which would then be every Node's, as it can bind a role to any group. The bootstrap roles do not.
- **Rejected: the signer adds the Organization to a request that has none.** The request would then not state what is signed, and a requester's subject would be changed without its knowledge. A denied request names the failed rule.
- **Rejected: the Node authorizer requiring `gaon:nodes`.** Its behavior is the issue's to keep, and the CN with an existing Node is a stronger check than a group that the signer gives every Node name.
- **Rejected: renewing at start a pair that lacks the Organization.** It needs a token or a still-valid pair and a new rule for what makes a pair valid, which ADR 0007, Decision 5, does not state; the renewal that already exists carries the subject.

## Signs the decision was wrong

- A caller other than a Node Agent needs a certificate through the signer, and the Organization cannot tell the two apart.
- A deployment runs a Node Agent from before this decision against a signer with it, and Nodes lapse because renewals are denied.
- A verifier needs a Node's certificate sooner than its renewal gives it, and operators rotate every Node by hand.
- An operator binds a role to `gaon:nodes` and a token holder uses it.
