# ADR 0007: A Node gets its client certificate from gaon's own CA through a signing request that a bootstrap token may create

> Status: accepted; the subject of a Node's certificate, in Decision 4 and Decision 5, is superseded by ADR-0022
> Date: 2026-11-12

Drafted for garamsh/gaon#64, under goal garamsh/gaon#63. The design is the PM's, taken under the project owner's decision 2 in garamsh/naru#34 and recorded in #63 §"CSR design (PM, under naru#34 decision 2)" and the PM's detail comment there. It follows the Kubernetes TLS-bootstrap pattern. It amends [ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md), Decision 1, and answers the question [ADR 0006](./0006-a-gaon-deployment-is-one-control-plane-node-configured-by-argv-and-files.md) left open, its Decision 5.

## Context

- **A Node's first certificate has no source inside gaon.** ADR 0005 has an operator or tooling issue every certificate, and ADR 0006 has naru deliver the files, so a Node that cannot be handed a file cannot join, and a certificate that expires stops its Node Agent until someone replaces it.
- **The API server requires a client certificate on every request** (ADR 0005, Decision 1), so a Node with no certificate cannot even ask for one.
- **A Node Agent acts for one Node**, and its certificate's CN is its Node name (ADR 0005, Decision 1).
- **naru's decision 2** asks for gaon's own CA with a bootstrap and CSR flow, and for one cluster CA.

## Decision

**A Node Agent holding only a bootstrap token creates a CertificateSigningRequest. `gaon-controllers` signs it with the cluster CA, and the Node Agent then uses the certificate, and renews it, over the same path.**

1. **Bootstrap token.**
   - Its format is `<id>.<secret>`, matching `[a-z0-9]{6}\.[a-z0-9]{16}`.
   - The API server learns the valid tokens from a file, `--bootstrap-token-file`, one token per line, read at start like its other files.
   - A token is revoked by removing it from the file and restarting the server.
2. **The narrowed listener.**
   - The TLS listener verifies a client certificate when one is given and no longer requires one (`tls.VerifyClientCertIfGiven` in place of `tls.RequireAndVerifyClientCert`). This narrows ADR 0005, Decision 1; the rest of ADR 0005 stands.
   - A request with a verified client certificate is authenticated as before, and its CN is its identity.
   - A request with no client certificate and a valid `Authorization: Bearer <token>` may only create a CertificateSigningRequest and read one it created. Its identity is `bootstrap:<id>`.
   - Any other request without a client certificate is refused, including one with an invalid token.
   - A token is honoured over TLS only.
3. **The CertificateSigningRequest resource.**
   - `spec` holds a PEM PKCS#10 request and `username`, which the API server sets from authentication: `bootstrap:<id>` for a token, or the certificate's CN. The requester cannot set it.
   - `status` holds the conditions `Approved` and `Denied` and the issued PEM certificate.
   - The contract is [resource-api.md](../resource-api.md) §CertificateSigningRequest.
4. **The signer.**
   - `gaon-controllers` approves and signs, with the cluster CA's certificate and key as files, `--signing-cert` and `--signing-key`.
   - The same CA certificate is the API server's `--tls-client-ca`: one cluster CA.
   - It approves a Node Agent's request when both hold:
     - the request is client-auth only, with a CN equal to a Node name;
     - the requester is a bootstrap token, or a certificate whose CN equals that same Node name, which is a renewal.
   - It records `Denied`, with the failed condition as the reason, for a request that fails either.
   - The validity of what it signs is `--signing-duration`, 8760h by default.
5. **The Node Agent.**
   - It takes `--bootstrap-token-file` (`GAON_BOOTSTRAP_TOKEN_FILE`) and `--cert-dir` (`GAON_CERT_DIR`). The token travels as a file, never as argv.
   - `--cert-dir` is a writable directory where the agent keeps its key and issued certificate. The agent creates it with mode 0700 if it is missing and writes the key with mode 0600.
   - At start it uses the pair in `--cert-dir` if the pair is valid. Otherwise it generates a key and bootstraps with the token, verifying the server against `--tls-ca`.
   - It renews at two thirds of the certificate's validity, over its own certificate, through the same request path, and swaps the pair without a restart.
   - `--tls-cert` and `--tls-key` keep working as operator-supplied files and are mutually exclusive with the bootstrap flags. `--tls-ca` still verifies the server.
6. **Callers other than Node Agents are unchanged.** The scheduler, the controllers and the CLI keep operator-issued certificates under ADR 0005, signed by the same CA. The signer approves only Node Agent requests.

## Consequences

- **A token can obtain a certificate for any Node name.** The signer binds a token to no Node, so whoever holds a valid token can become any Node, write that Node's resources and run what is bound to it, until the token is removed and the server restarted. Certificates already issued stay valid until they expire, because gaon has no revocation.
- The API server has three kinds of caller, the certificate holder, the token holder and the anonymous one, and tells them apart before it routes. The one path a certificate-less caller may write is the one that mints a credential.
- The CA's key is a file on the control-plane Node that `gaon-controllers` reads. Whoever reads it can sign for any caller, the scheduler's and the controllers' included. This adds to the single point of failure ADR 0006 records.
- A Node needs only a token to join, and renews without an operator, which ends ADR 0006's consequence that renewal is the operator's work for Node Agents. For the other callers it remains the operator's.
- The Node Agent's key is written to disk on the Node, in a directory the agent owns.
- The token file and the signing files are read at start, so replacing them takes a restart, as ADR 0005 states for the TLS files.
- **A CertificateSigningRequest has no delete path** in the contract, so requests stay in the Store. Their garbage collection is not decided.
- This narrows ADR 0005's rejection of bearer tokens: a token is accepted for one create and one read, and for nothing else.
- **Rejected: a token bound to one Node name.** It needs a store of token-to-Node pairs and an issuing step per Node, which is more than a file of tokens and a rule on the request.
- **Rejected: approval by a person.** Every join would wait for an operator, which the flow exists to remove.
- **Rejected: keeping operator-issued certificates for Node Agents.** It leaves a Node without a source for its first certificate and renewal manual, as ADR 0006 records.

## Signs the decision was wrong

- A leaked or shared token is used to impersonate a Node, and operators cannot rotate tokens by restarting the API server.
- Requests accumulate in the Store until it is the cause of slow lists and watches.
- The CA key on the control-plane Node is exposed or lost, and the replacement of the CA costs every caller's certificate.
- The one-third renewal margin proves too short, and Node Agents lapse between a failed renewal and their next attempt.
- A caller other than a Node Agent needs the token path or a signed certificate from the flow.
