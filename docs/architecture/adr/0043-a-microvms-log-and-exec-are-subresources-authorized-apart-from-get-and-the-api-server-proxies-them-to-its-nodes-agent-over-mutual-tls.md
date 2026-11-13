# ADR 0043: A MicroVM's log and exec are subresources authorized apart from `get`, and the API server proxies them to the agent of its Node over mutual TLS

> Status: accepted
> Date: 2026-11-13

Drafted for garamsh/gaon#275, part of goal #117, whose decision D5 is the PM's: "Logs and exec go from the CLI to the API server, and the API server proxies them to the Node Agent of the MicroVM's Node." This ADR settles what D5 leaves open and defines the contract only. It amends [ADR 0007](./0007-a-node-gets-its-client-certificate-from-gaons-own-ca-through-a-signing-request-a-bootstrap-token-may-create.md) and [ADR 0022](./0022-a-nodes-certificate-carries-the-organization-gaon-nodes-and-the-signer-signs-no-other-organization.md) in one place (Decision 7). No code serves these paths yet.

## Context

- **There is no way to read what a MicroVM printed, or to run a command in it.** The API server serves objects only, the Node Agent is a client of the API server and serves no endpoint, and [resource-api.md](../resource-api.md) §Open questions lists "Data-plane route: how exec, file and console traffic reaches a node" as undecided.
- **What mvmd offers.** The statements in this list hold for garamsh/mvmd at commit `eebb760d62370c1ebf49bb86c5a9413d76c71650`, read from `lib/runtimeapi/proto/mvmd/runtime/v1alpha2/runtime.proto`, `docs/architecture/runtime-api.md` §Guest access and `docs/architecture/guest.md` §Current decisions:
  - `Exec {exec_id, request_id, machine_id, command, env, working_dir}` starts a command in the guest. There is no user, no standard input and no terminal: the command runs as root with exactly the environment given. An exec runs at most once per request ID, and mvmd never ends one by itself.
  - `ReadExecOutput {machine_id, exec_id, after_offset}` streams `OutputChunk {stream, offset, data}`, standard output and standard error in one sequence of offsets, then an `ExecRecord {status, reason, exit_code, term_signal, ended_at}` whose status is `STARTED`, `NOT_STARTED`, `CUT_SHORT` or `EXITED`. An offset no longer held is answered `OutputGone {earliest_offset}`.
  - The MicroVM's process is the exec whose ID is `entrypoint`. Its output is carried by this call and not by the serial console. The guest keeps the newest 16 MiB of output per command and ends the Machine within 5 s of the entrypoint's exit.
  - The call needs a Running Machine, unless the exec's record is already final, which is answered from the record. For a Stopped Machine the entrypoint's record is answered from the console log, and its output is not.
  - `Console` is a bidirectional serial console. It does not replay earlier output.
  - A `tail_bytes` field of `ReadExecOutputRequest`, with the rules garamsh/mvmd#438 point 5 states (a non-zero offset with it is invalid, a value larger than what is held starts at the oldest byte held, zero means unset), is garamsh/mvmd#619. **It is an open issue and is not merged.** The guest-agent half merged as `df306f633d99575a88003478987255db3cfc5d80`.
  - The stream carries no marker that the reader has caught up with what is held.
- **What gaon holds.** A MicroVM's Machine is held by the Node Agent as a name and a binding epoch ([node-agent.md](../node-agent.md) §MicroVMs). `spec.nodeName` names the Node. A Node's `status.addresses` is a list of strings the agent is to write and does not yet, and the Node has no port.
- **How callers are authenticated today.** The API server presents only a server certificate ([resource-api.md](../resource-api.md) §Transport). A caller is a certificate (CN is the user, O the groups), a bootstrap token or a ServiceAccount token. A Node's certificate is issued by the csr-signer for client authentication only, with CN the Node's name, O `gaon:nodes` and no subject alternative name. One cluster CA signs everything ([deployment.md](../deployment.md) §Certificates). The API server runs three-fold behind a VIP.
- **The semantics follow Kubernetes' `pods/log` and `pods/exec`,** where nothing here argues otherwise. Where gaon departs on purpose:
  - **no `previous`, `sinceTime`, `sinceSeconds`, `limitBytes`, `timestamps` or `container`:** one process per MicroVM, no kept earlier container, and mvmd keeps no timestamps;
  - **`tailBytes` where Kubernetes has `tailLines`:** mvmd can bound a read by bytes and not by lines;
  - **exec has no standard input, no terminal and no resize,** and its transport is not SPDY or WebSocket. Output is a stream of JSON objects, as [resource-api.md](../resource-api.md) §Watch is;
  - **exec has no user:** mvmd runs every command as root, so `create` on `microvms/exec` is root in the guest. Kubernetes' `pods/exec` runs as the container's user;
  - **a finished MicroVM's output is readable** while the MicroVM object exists, as Kubernetes' is while the Pod is. Consequence 1 says what that needs.

## Decision

**A MicroVM has two subresources, `log` (read, a stream of bytes) and `exec` (create, a stream of JSON objects), each authorized as a resource of its own so that `get` on `microvms` grants neither. The API server judges the caller, finds the agent of `spec.nodeName`, and proxies the call to it over mutual TLS in which the agent accepts only a caller certificate of the `gaon:apiservers` group; when there is no machine to reach it answers with a reason that says which of two things is missing.**

1. **Paths.** Relative to `/apis/compute.gaon.garam.sh/v1alpha1`, as every path in [resource-api.md](../resource-api.md) §Paths is:

   | Verb and path | Authorized as | Answer |
   |---|---|---|
   | `GET /microvms/{name}/log` | `get` on `microvms/log` | `200` with `application/octet-stream` |
   | `POST /microvms/{name}/exec` | `create` on `microvms/exec` | `200` with `application/x-ndjson` |

   Neither is a write of the object, so neither is sent to an admission webhook. Both are judged on the stored MicroVM's labels, as every subresource is.

2. **The log query.**
   - `follow`: boolean, default false. True keeps the response open until the MicroVM's process ends or the client disconnects; false returns what is held and ends.
   - `tailBytes`: integer from 1 to 16 777 216 (16 MiB, the most the guest holds). Absent, the read starts at the oldest byte held. `0`, a negative number, a value above the bound or one that is not an integer is `Invalid`: `0` is refused because mvmd reads it as unset and would answer the whole output where the caller asked for none.
   - Any other parameter is `Invalid`. Unknown parameters are refused, not ignored, so that a parameter a later version adds is not silently dropped by an older server.
   - The body is standard output and standard error interleaved in the order the guest produced them, as one sequence. They cannot be told apart, because the stream of each chunk is not forwarded.
   - Output is readable while the MicroVM object exists, a finished `Once` MicroVM's included. Deleting the MicroVM ends it. What serves it after the Machine stops is Consequence 1.
   - There is no resume offset. A client that was cut reads again with `tailBytes`.

3. **The exec request.**
   - The body is JSON: `command`, a non-empty list of strings whose first element is non-empty; `env`, a list of `{name, value}` objects, default empty; `workingDir`, a string, default the guest's own. Any other field is `Invalid`. The environment is a list and not a map so that its order is the caller's.
   - The response is a stream of JSON objects, one per line, each with a `type` in PascalCase as a Watch event's is:
     - `Output`: `stream` (`Stdout` or `Stderr`) and `data`, the bytes in base64;
     - `Exit`, the last object of a command that ran: `status` (`Exited`, `CutShort` or `NotStarted`), `exitCode`, `termSignal` and `reason`, those that the record has;
     - `Error`: an error object, as in a Watch, for a failure after the response began.
   - The agent makes the `exec_id` and the `request_id`, fresh for each request, and never repeats a call: a command that may have started is not run twice.
   - A client that disconnects does not stop the command. mvmd has no call that ends one.
   - Exec is refused with `NotRunning` when the Machine is not Running or the MicroVM has a `deletionTimestamp`.
   - Interactive exec (standard input, a terminal) is **deferred**: mvmd's `Exec` has neither, and its `Console` is the serial console, not a shell. It waits for mvmd.

4. **RBAC.** The resource names are `microvms/log` and `microvms/exec`. A rule names a resource exactly, so `get` on `microvms` grants neither, and a role that reads MicroVMs gains no output and no shell. `*` as a whole name matches both, as it matches every resource. The Node authorizer is unchanged: it grants a Node nothing on either. Existing bootstrap roles are unchanged, which has a consequence (Consequence 3).

5. **Finding the agent.**
   - The API server reads the MicroVM's `spec.nodeName`, then the Node of that name, and dials the agent at an address of `status.addresses`, in order, on the Node's new `status.agentPort`, an integer from 1 to 65 535 the agent writes with `addresses`.
   - The agent's paths mirror the public ones, under `/microvms/{name}/log` and `/microvms/{name}/exec`, and its query carries `bindingEpoch`, the epoch the API server read from the MicroVM. An agent that holds the Machine at another epoch refuses.
   - The API server does not wait on a Node's conditions: it dials, and a failure to is the answer (Decision 8). It does not retry.
   - The port is a field of the Node and not an API server flag: the agent knows what it listens on, and a flag would couple the settings of two programs deployed apart.

6. **Authentication of both ends.**
   - The agent serves TLS 1.3 and requires a client certificate that chains to the cluster CA and carries Organization `gaon:apiservers`. A certificate with any other organization is refused, a Node's and an administrator's included.
   - `gaon:apiservers` is a subject like `gaon:schedulers`: the operator issues a certificate for it and no role binds it. The API server presents one as its client certificate, a certificate other than its serving one, so that a serving certificate is never accepted as a caller.
   - The agent's serving certificate is its Node certificate (Decision 7). The API server verifies the chain to the cluster CA and that the CN is the name of the Node it meant to reach. It does not check the host name or an address, because the certificate has none.
   - The agent does not authenticate the end user and does not ask the API server who it is. The API server authorized the user, and the agent serves only a Machine it holds at the epoch.

7. **The signer adds `serverAuth`.** The csr-signer issues Node certificates with the extended key usage `serverAuth` as well as `clientAuth`. A certificate issued before this change gains it at its next renewal; until then the agent of that Node cannot serve and the answer is `Unavailable`. This amends ADR 0007 and ADR 0022, which issue client authentication only. The change is in the signer and in [csr-signer.md](../csr-signer.md), and lands with the PR that serves the endpoint.

8. **What is answered when there is no machine.** Two reasons are added to [resource-api.md](../resource-api.md) §Watch's list:

   | Reason | HTTP | When |
   |---|---|---|
   | `NotRunning` | 409 | the MicroVM has no `spec.nodeName`; or the agent holds no Machine for it at that epoch; or, for a log, the Machine holds no output; or, for exec, the Machine is not Running or the MicroVM is being deleted |
   | `Unavailable` | 503 | the Node does not exist, has no address or no `agentPort`; or the dial, the TLS handshake or the proxied call fails or times out; or the agent cannot reach mvmd |

   Every other answer is an existing reason: `NotFound` for a MicroVM that does not exist, `Invalid` for a query or a body, `Unauthorized` and `Forbidden` as everywhere. `Conflict` is not used, because clients retry it as a compare-and-swap conflict, and a retry here changes nothing.

## Consequences

1. **The promise that output is readable after the process ends is not yet kept by mvmd.** At the commit above, mvmd answers a Stopped Machine's entrypoint from the console log and not from output, and a `Once` MicroVM's Machine stops within 5 s of the entrypoint's exit. Output "readable until the MicroVM is deleted" needs mvmd to retain it, or the Node Agent to copy it before the Machine stops. Which is the PM's, and the issue that serves the log cannot be filed until it is decided.
2. **A read that does not follow, of a Running Machine, cannot be ended by mvmd's stream,** which has no caught-up marker. It needs one from mvmd, or an idle interval chosen by the serving issue.
3. **`gaon:controllers` can read every MicroVM's output,** because its `get` on `*` matches `microvms/log`. It is left as it is: narrowing a bootstrap role is its own decision.
4. **`create` on `microvms/exec` is root in the guest.** A role that holds it holds the guest. The resource document says so.
5. **The API server is a client of every Node's agent.** It needs a certificate of its own, and deployment gains one more file to deliver. The agent gains a listener, a port and a serving certificate; each is the serving issue's.
6. **A Node certificate that does not carry `serverAuth` makes its Node's logs and exec `Unavailable`** until it renews.
7. **An exec's output is held in the guest and no call discards it,** so each exec adds to a table that is not bounded. It is an open question for mvmd.
8. **Output across a restart is not stitched.** A new binding is a new Machine; a log read after a restart answers that Machine's output only, and a request carrying an earlier epoch is refused.
9. **`tailBytes` waits for garamsh/mvmd#619.** Until it merges, the agent would read from the start and discard, which is the serving issue's to weigh.

## Signs the decision was wrong

- An operator wants a role that reads output and not objects, or the reverse, and the exact resource names are not enough to say it.
- A controller that reads a log is written, and `gaon:controllers`' read of output has become a dependency.
- Two API servers dial one agent so often that its connections are the limit; a reverse tunnel is then worth its registry.
- A Node's address changes and the API server holds a stale one for long enough to matter.
- A client needs to resume a cut log and `tailBytes` makes it re-read what it had.
- `Unavailable` is returned for a case a client should retry and one it should not, and the two cannot be told apart.

## Rejected alternatives

- **A reverse tunnel: the agent dials out to the API server.** Three API servers sit behind one VIP, so the tunnel lands on one of them and a request that arrives at another has to find it. That is a registry of tunnels, which is more than a dial.
- **A second certificate request for the agent's serving certificate.** A second request, a second renewal and a second file, for a key that already identifies the Node.
- **Serving certificates the operator supplies for each agent.** One more file per Node to deliver, and a mismatch with the Node's name is the operator's to find.
- **A shared flag for the agent's port.** It couples two programs' settings and cannot differ between Nodes.
- **Reusing the API server's serving certificate as its client certificate.** Every certificate that can serve would then be able to call, and it carries no organization.
- **The agent asking the API server whether a caller is allowed.** A round trip for each call, and the API server must then be reachable from the Node while the user is already connected to it.
- **`tailLines`.** mvmd cannot start at a line.
- **Keeping the two streams apart in the log.** It would need mvmd to number them separately.
- **Exec as an interactive WebSocket.** mvmd has no standard input and no terminal to attach it to.
- **A single resource, `microvms/stream`, for both.** A role that may read output would hold the guest.
