# CertificateSigningRequest signer

How the signer in `gaon-controllers` decides a CertificateSigningRequest, what it signs and how it is configured. The signer is in `controllers/` and runs in the binary `gaon-controllers`.

Basis: [resource-api.md](./resource-api.md) §CertificateSigningRequest and §Paths, and [ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) Decision 4.

## Current decisions

### Responsibility

- **The signer decides requests.** It approves a Node Agent's request and signs it with the cluster CA, or denies it. It is the one writer of a request's `status` ([resource-api.md](./resource-api.md) §CertificateSigningRequest).
- **It writes one thing**: a request's `status`, through `PUT /certificatesigningrequests/{name}/status`, by compare-and-swap on the request it read. It writes the `Approved` condition, status `True`, together with `status.certificate`, or the `Denied` condition, status `True`, with a reason and a message.
- It uses the public resource API through `lib/api` and imports no other unit. It runs beside the node lifecycle controller in one process, and a failure of either ends the process.

### Which requests it decides

- **Every request with no `Approved` and no `Denied` condition**, whether it is pending when the signer starts or arrives later. It lists, then watches from the list's version, as the node lifecycle controller does for Nodes.
- **A decided request is never written again**, whatever the status of its condition.
- **On a `Conflict`** it reads the request again and decides only if it is still undecided. A request that keeps changing is given up after three attempts and retried with the next list.
- **A failed list, watch or write** lists again after a fixed delay (`csrsigner.RetryDelay`). A `Deleted` event is ignored.

### The approval rule

The rules are checked in order and the first failure is recorded as `Denied`, with its reason:

| Order | Rule | Reason |
|---|---|---|
| 1 | `spec.request` parses as PKCS#10 and its signature verifies | `InvalidRequest` |
| 2 | The subject is exactly two attributes: a CN, which is non-empty and does not start with `bootstrap:`, and one Organization, `gaon:nodes` | `InvalidSubject` |
| 3 | The request carries no subject alternative name and no extension other than an extended key usage of exactly client auth, or of exactly client auth and server auth in either order | `DisallowedExtension` |
| 4 | `spec.username` starts with `bootstrap:`, or equals the CN, which is a renewal | `UnauthorizedRequester` |

- **The CN has no other format rule than rule 2's**, and its Node need not exist: an agent asks for its certificate before it registers its Node. A token can therefore obtain a certificate for any Node name ([ADR 0007](./adr/0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) §Consequences).
- **An empty username fails rule 4.** A certificate holder asking for another CN fails it too.

### What it signs

- **The subject and public key of the request**, with a random 128-bit serial, key usage digital signature and exactly the extended key usage the request states: client auth, or client auth and server auth in the order the request lists them. A request with no extended key usage gets client auth. The certificate is not a CA and carries no name but the subject.
- **`NotBefore` is the signing time minus 5 minutes**, for clock skew between Nodes, as the Kubernetes signer does. **`NotAfter` is the signing time plus `--signing-duration`**, never later than the CA certificate's `NotAfter`.

### Configuration

- **`--signing-cert` and `--signing-key`** are the PEM files of the cluster CA, the same certificate the API server takes as `--tls-client-ca`. Both are given or neither. With neither, the signer does not run and the start logs that. One of the two, or files that do not load or do not match, stop the start.
- **`--signing-duration`** defaults to 8760h. A value of zero or less stops the start, whether or not the signer runs.
- The files are read at start, so replacing them takes a restart.

## Rationale

- The approval rule is the one the contract states ([resource-api.md](./resource-api.md) §CertificateSigningRequest), split into four checks so each denial names the one that failed.
- Rule 3 keeps a certificate to what a Node Agent asks for, client authentication under its CN and, to serve a MicroVM's log and exec, server authentication ([ADR 0043](./adr/0043-a-microvms-log-and-exec-are-subresources-authorized-apart-from-get-and-the-api-server-proxies-them-to-its-nodes-agent-over-mutual-tls.md) Decision 7), and nothing a request could add to it. The signer signs the usage the request states and adds none ([ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md) §Rejected alternatives).
- Rule 2 allows one Organization, `gaon:nodes`, because a certificate's Organization values are its groups ([ADR 0008](./adr/0008-the-api-server-authorizes-certificate-callers-with-cluster-roles-bindings-and-a-node-authorizer.md)) and the signer copies the subject it is given; the one value marks a Node to a verifier outside gaon and no role grants it ([ADR 0022](./adr/0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md)). Refusing every other attribute, a second Organization included, means no attribute a later authorizer reads can be requested.
- A request is decided once, because `status` is the signer's only output and a second decision could replace a certificate the Node Agent already holds.
- `NotBefore` is set back so that a Node whose clock is behind the signer's does not refuse the certificate it has just been given.

## Open questions

- **Garbage collection of requests** is not decided, as the contract has no delete path ([deployment.md](./deployment.md) §Open questions).
- **A CA certificate that has expired** is not checked at start: the signer then signs certificates that are already expired.
