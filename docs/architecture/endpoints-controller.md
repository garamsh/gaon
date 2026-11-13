# Endpoints controller

How the endpoints controller writes the Endpoints of a Service that has a selector from the Ready MicroVMs the selector selects. The controller is in `controllers/` and runs inside the binary `gaon-controllers`.

Basis: [resource-api.md](./resource-api.md) §Service, Endpoints and NetworkPolicy and [ADR 0027](./adr/0027-a-service-allocates-its-addresses-from-two-configured-ranges-endpoints-lists-its-ready-microvms-and-a-networkpolicy-selects-microvms-by-label.md) Decision 3. It depends on the `gaon:controllers` grant of `create` and `update` on `endpoints`.

## Current decisions

### Responsibility

- **It writes the Endpoints of every Service with a non-empty `spec.selector`**, named for the Service, through `PUT /endpoints/{name}`: a create when none is stored, otherwise an update at the `resourceVersion` it read.
- **It never writes or deletes the Endpoints of a Service without a selector.** The user writes that one. A Service whose selector is removed keeps its Endpoints as the controller last wrote it, and the user owns it from then on.
- **It never deletes an Endpoints.** The Endpoints it writes carries an owner reference to its Service, with `controller` true and the Service's `uid`, so the garbage collector deletes it with the Service ([garbage-collector.md](./garbage-collector.md)). Any other owner reference and any label on a stored Endpoints are kept.
- **It overwrites the Endpoints a user wrote for a Service that has a selector**, and adds the owner reference to it.
- **It acts only while the controllers hold their Lease,** inside the term of `gaon-controllers` ([deployment.md](./deployment.md); [ADR 0023](./adr/0023-the-control-plane-runs-on-three-nodes-over-a-three-member-etcd-and-the-scheduler-and-the-controllers-act-only-while-holding-a-coordination-lease.md)). A term starts from a list, so a new holder keeps no state of the old one.
- It uses the public resource API through `lib/api` and imports no other unit.

### What an Endpoints lists

- **A MicroVM is listed when all four hold:** the selector selects it (every pair of `spec.selector` is a label of the MicroVM), it has no `deletionTimestamp`, its `Ready` condition is `True`, and its `status.addresses` has an address.
- **The address is the first of `status.addresses` that is a global unicast IP without a zone**, because the API server refuses an Endpoints holding any other. A MicroVM with none is not listed.
- **`targetRef`** names the MicroVM, with its `uid`.
- **A Service port is resolved per MicroVM.** An absent `targetPort` is `port`, a number is that number, and a name is the `port` of the MicroVM's `spec.ports` entry with that name and the Service port's protocol. A MicroVM that does not declare the name for that protocol does not serve that port.
- **A MicroVM goes into the subset of the ports it resolves,** so MicroVMs whose named `targetPort` differs fall into different subsets. A MicroVM that resolves none of the Service's ports is not listed.
- **The order is fixed**: addresses by MicroVM name within a subset, subsets by their resolved ports, so a write that changes nothing is not made.
- **A Service with no Ready MicroVM gets an Endpoints with no subsets.** A Service with no ports, which only a headless Service may be, gets none either, because a subset needs a port.
- **The controller writes nothing when the stored object already holds the subsets and the owner reference it would write.**

### Timing

- **The controller lists Services and MicroVMs, then watches both** from the lists' versions, and writes the Endpoints of every Service with a selector after each list.
- **A Service event marks that Service. A MicroVM event marks each Service whose selector selects the MicroVM before or after the change,** so a MicroVM that stops matching, is deleted, or becomes or stops being Ready is followed.
- **A failed write is retried** after `endpoints.RetryDelay` or at the next event, whichever is first. A failed list or watch, an `Expired` error and a stream that ends list again after the same delay.
- **A write the API refuses as invalid is not retried.** It waits for the next change to the Service or to a MicroVM it selects, and is logged.

## Rationale

- ADR 0027 puts the writer of a selector Service's Endpoints in a controller under a coordination Lease, so that three API servers do not each write it.
- Marking by the MicroVM's labels before and after a change is what makes a MicroVM that leaves a selector leave the Endpoints, without keeping which Service lists which MicroVM.
- An Endpoints refused as invalid is not retried because a retry sends the same body.

## Open questions

- **A MicroVM with a stale status is listed.** The controller reads the four conditions as the contract states them and does not compare `status.bindingEpoch` with `spec.bindingEpoch`, which [resource-api.md](./resource-api.md) §MicroVM calls a void status read as `Pending`. Between a rebind and the new Node Agent's first status write, the old address stays listed.
- **A Service that selects more than 1000 Ready MicroVMs,** or whose ports resolve to one port number twice for a MicroVM, gets an Endpoints the API server refuses, so none is written until that changes (ADR 0027 §Consequences).
- **Every event scans every Service's selector.** The cost grows with the number of Services.
