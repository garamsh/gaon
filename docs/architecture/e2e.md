# End-to-end test

How the two end-to-end tests run, what each asserts, and what the host needs: the failover test of M1 on QEMU Nodes, in `tooling/e2e/tests/`, and the extension-points test on this host without KVM, in `tooling/e2e/extensions/`. Unless a section names the second, it describes the first.

Basis: [node-lifecycle.md](./node-lifecycle.md), [resource-api.md](./resource-api.md) §Lease and fencing, [scheduler.md](./scheduler.md), [node-agent.md](./node-agent.md) and [deployment.md](./deployment.md). naru's side holds for garamsh/naru release `v0.3.0-rc.1`, commit `5b42b429c6b41447f2ea267fe606c2b5d44ea63e`, whose `docs/architecture/harness.md` is the contract of `naru-cluster`.

## Current decisions

### What the test shows

- **The M1 done-when.** On QEMU Nodes, a MicroVM defined in YAML and applied with `gaon apply` runs on mvmd on its bound Node, and when that Node is killed it runs on another Node.
- **Steps**, in `tooling/e2e/tests/failover_test.go`:
  - boot two Nodes; node-1 runs etcd and the control plane, node-2 only the Node Agent;
  - admit node-2 alone with `gaon apply`, so the Scheduler can bind to no other Node, and apply the MicroVM;
  - wait for the MicroVM to be Running on node-2 with `status.bindingEpoch` equal to `spec.bindingEpoch`;
  - admit node-1, wait for it to be Admitted and Ready, then `kill -KILL` node-2's QEMU process, which naru's harness defines as a power-off;
  - poll until the MicroVM is Running on node-1 at epoch 2.
- **A MicroVM on two Nodes at once** is checked at every poll after the kill from what the API shows: the epoch never decreases and rises by exactly one, the MicroVM's node changes only with a new epoch, the Node killed stays down, and a binding at epoch 2 is seen only once the killed Node's `LeaseExpired` condition is True. The test cannot read mvmd on a Node, so it does not observe the guests directly.
- **The node lifecycle controller.** The test fails at the failover step when `gaon-controllers` is disabled: no `LeaseExpired` condition appears within the bound below, and the MicroVM is never rebound.

### The extension-points test

`TestExtensions` in `tooling/e2e/extensions/`, behind the build tag `extensions`, runs the built services with authorization on and mutual TLS, and no KVM. The test process holds embedded etcd and a minimal mvmd for each Node; the apiserver (`--authorization-mode=RBAC`), `gaon-controllers` (also the CSR signer), the scheduler (`--config` naming LeastAllocated and an extender) and two node-agents are the built binaries, each started as its own process on `:0` ports. The subtests run in order against one cluster. Each refusal has its control in the same subtest.

- **Existing clients under RBAC.** Each agent gets its certificate by a CertificateSigningRequest that `gaon-controllers` signs (the certificates carry the node names), it registers its own Node, which the test then admits, the Node becomes Admitted and Ready, its Lease renews, and a MicroVM is bound and Running. At the end, no service's log contains `Forbidden`.
- **Custom resource.** A CustomResourceDefinition becomes Established, and its objects are created, read, listed and watched.
- **External controller.** A controller with its own certificate, bound to a ClusterRole on the group and the plural, watches the kind and writes status. A certificate with no binding is Forbidden the same status write, and so is the controller before its binding is created; the control is the controller's identical write after it, which succeeds.
- **Webhook.** A ValidatingWebhookConfiguration served by the test refuses an object carrying a marked field with Forbidden and the webhook's message, and the object is not stored. The control is the same write without the field, which is accepted.
- **Extender.** The extender refuses the Node LeastAllocated would choose for MicroVMs whose name starts with `filtered-`, so the MicroVM is bound to the other Node. The control is a MicroVM the extender does not filter, which goes to LeastAllocated's choice. Before each, the test asserts the Nodes' capacities differ (4 CPU and 8Gi against 8 CPU and 16Gi) and that the larger Node is the freer one. The filtered MicroVM goes first, so the choice of the larger Node is a score and not the name-order tie it would be after one MicroVM on each.

### The failover bound

- **Lease expiry** is confirmed within the Lease duration (30s), the margin (5s) and one renew period (5s) of the kill: 40s.
- **Running elsewhere** adds the Scheduler's poll interval (5s), the Node Agent's resync period (10s) and an allowance of 30s for mvmd to start the MicroVM: 85s.
- **The allowance is the test's own figure.** No document states how long mvmd takes to start a MicroVM.
- The numbers are the provisional values of [resource-api.md](./resource-api.md) §Lease and fencing and move with them.

### Which binaries run

- **The gaon binaries are built from the checkout**: the four services as static linux/amd64 executables, and the `gaon` CLI for the host. They are given to `naru-cluster` with `-gaon-bin`, so the Nodes run them in place of the ones pinned in naru's image.
- **`naru-cluster` and the release** are those of the pinned naru commit and release. The Make target verifies the release's `SHA256SUMS`.
- **The default run** uses the boot PKI, so each Node Agent gets its certificate through a CertificateSigningRequest that `gaon-controllers` signs ([csr-signer.md](./csr-signer.md)).
- **A run with services disabled** passes `-boot-pki=false -disable <services>`, as naru's harness requires for `gaon-controllers`, so every Node Agent holds an operator-issued pair and no signer is needed.

### Running it

- **`make e2e-extensions`** from the repository root runs the extension-points test. It builds the four services for the host into `EXT_DIR` (default `build/e2e-extensions`), vets the module with `-tags extensions`, runs the test with `E2E_EXT_SERVICES` naming the binaries, and reports how many tests passed, failing when none did. It is part of `make ci`, because it needs no KVM. Its processes, files and sockets are named `gaon-93-*`; the files and sockets sit in a directory `/tmp/gaon-93-*`, short because a unix socket path is limited to about 100 bytes, and removed when the test ends.
- **`make e2e`** from the repository root. It is not part of `make ci`, because hosted CI has no nested KVM.
- **`make e2e E2E_DISABLE=gaon-controllers`** is the run that must fail.
- **`E2E_DIR`** names where the run keeps the binaries, the release, `naru-cluster`'s `-dir` with the Nodes' disks and consoles, `naru-cluster.log` and `test.log`; it defaults to `build/e2e`. The cluster's directory is emptied at the start of each run.
- **The target reports** the commit the binaries were built from, the naru versions, the services disabled and the number of tests that passed, and fails when none did.
- **`make ci` runs the unit tests of `cluster_file.go`** and vets and builds the module, and runs `make e2e-extensions`; the QEMU test is behind the build tag `e2e`, which `make e2e` vets before it runs, and the extension-points test behind `extensions`, which its target vets.

### What the host needs

- **For `make e2e-extensions`:** Go, `make`, loopback networking and about 1 GiB of memory. The module `tooling/e2e` requires etcd's server library and mvmd's runtime API, so the first run needs them in the module cache.
- **For `make e2e`:**
  - `/dev/kvm`, with nested virtualization when the host is itself a VM, and `qemu-system-x86_64`, `qemu-img`, the OVMF files and `e2fsprogs`, as `naru-cluster` requires ([harness.md](https://github.com/garamsh/naru/blob/5b42b429c6b41447f2ea267fe606c2b5d44ea63e/docs/architecture/harness.md) §What the command needs).
  - About 3 GiB of memory per Node, so 6 GiB for the two Nodes.
  - Go, `make`, and an authenticated `gh` that can read garamsh/naru releases.
  - Network access to download the naru module and release; the Nodes need none beyond loopback.

## Rationale

- The test runs the binaries built from the checkout because the e2e layer exercises what the build produces ([testing.md](../convention/testing.md) §Three layers).
- Only node-2 is admitted first because the Scheduler binds to the first fitting Node by name, which is node-1, the control plane; killing it would end the API server and no failover could be seen.
- The harness is naru's `naru-cluster`, not code in this repository, so gaon does not carry its own NoCloud writer, PKI or QEMU launcher.

## Open questions

- **The MicroVM start allowance** is unmeasured.
- **Observing mvmd directly.** The "never on two Nodes" check reads the API; reading each Node's mvmd waits for a way naru's harness exposes it.
