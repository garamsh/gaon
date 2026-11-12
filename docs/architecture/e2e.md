# End-to-end test

How the end-to-end test of M1 runs, what it asserts, and what the host needs. The test is in `tooling/e2e/`.

Basis: [node-lifecycle.md](./node-lifecycle.md), [resource-api.md](./resource-api.md) §Lease and fencing, [scheduler.md](./scheduler.md), [node-agent.md](./node-agent.md) and [deployment.md](./deployment.md). naru's side holds for garamsh/naru release `v0.3.0-rc.1`, commit `5b42b429c6b41447f2ea267fe606c2b5d44ea63e`, whose `docs/architecture/harness.md` is the contract of `naru-cluster`.

## Current decisions

### What the test shows

- **The M1 done-when.** On QEMU Nodes, a Machine defined in YAML and applied with `gaon apply` runs on mvmd on its bound Node, and when that Node is killed it runs on another Node.
- **Steps**, in `tooling/e2e/tests/failover_test.go`:
  - boot two Nodes; node-1 runs etcd and the control plane, node-2 only the Node Agent;
  - admit node-2 alone with `gaon apply`, so the Scheduler can bind to no other Node, and apply the Machine;
  - wait for the Machine to be Running on node-2 with `status.bindingEpoch` equal to `spec.bindingEpoch`;
  - admit node-1, wait for it to be Admitted and Ready, then `kill -KILL` node-2's QEMU process, which naru's harness defines as a power-off;
  - poll until the Machine is Running on node-1 at epoch 2.
- **A Machine on two Nodes at once** is checked at every poll after the kill from what the API shows: the epoch never decreases and rises by exactly one, the Machine's node changes only with a new epoch, the Node killed stays down, and a binding at epoch 2 is seen only once the killed Node's `LeaseExpired` condition is True. The test cannot read mvmd on a Node, so it does not observe the guests directly.
- **The node lifecycle controller.** The test fails at the failover step when `gaon-controllers` is disabled: no `LeaseExpired` condition appears within the bound below, and the Machine is never rebound.

### The failover bound

- **Lease expiry** is confirmed within the Lease duration (30s), the margin (5s) and one renew period (5s) of the kill: 40s.
- **Running elsewhere** adds the Scheduler's poll interval (5s), the Node Agent's resync period (10s) and an allowance of 30s for mvmd to start the Machine: 85s.
- **The allowance is the test's own figure.** No document states how long mvmd takes to start a Machine.
- The numbers are the provisional values of [resource-api.md](./resource-api.md) §Lease and fencing and move with them.

### Which binaries run

- **The gaon binaries are built from the checkout**: the four services as static linux/amd64 executables, and the `gaon` CLI for the host. They are given to `naru-cluster` with `-gaon-bin`, so the Nodes run them in place of the ones pinned in naru's image.
- **`naru-cluster` and the release** are those of the pinned naru commit and release. The Make target verifies the release's `SHA256SUMS`.
- **The default run** uses the boot PKI, so each Node Agent gets its certificate through a CertificateSigningRequest that `gaon-controllers` signs ([csr-signer.md](./csr-signer.md)).
- **A run with services disabled** passes `-boot-pki=false -disable <services>`, as naru's harness requires for `gaon-controllers`, so every Node Agent holds an operator-issued pair and no signer is needed.

### Running it

- **`make e2e`** from the repository root. It is not part of `make ci`, because hosted CI has no nested KVM.
- **`make e2e E2E_DISABLE=gaon-controllers`** is the run that must fail.
- **`E2E_DIR`** names where the run keeps the binaries, the release, `naru-cluster`'s `-dir` with the Nodes' disks and consoles, `naru-cluster.log` and `test.log`; it defaults to `build/e2e`. The cluster's directory is emptied at the start of each run.
- **The target reports** the commit the binaries were built from, the naru versions, the services disabled and the number of tests that passed, and fails when none did.
- **`make ci` runs the unit tests of `cluster_file.go`** and vets and builds the module; the end-to-end test is behind the build tag `e2e`, which `make e2e` vets before it runs.

### What the host needs

- `/dev/kvm`, with nested virtualization when the host is itself a VM, and `qemu-system-x86_64`, `qemu-img`, the OVMF files and `e2fsprogs`, as `naru-cluster` requires ([harness.md](https://github.com/garamsh/naru/blob/5b42b429c6b41447f2ea267fe606c2b5d44ea63e/docs/architecture/harness.md) §What the command needs).
- About 3 GiB of memory per Node, so 6 GiB for the two Nodes.
- Go, `make`, and an authenticated `gh` that can read garamsh/naru releases.
- Network access to download the naru module and release; the Nodes need none beyond loopback.

## Rationale

- The test runs the binaries built from the checkout because the e2e layer exercises what the build produces ([testing.md](../convention/testing.md) §Three layers).
- Only node-2 is admitted first because the Scheduler binds to the first fitting Node by name, which is node-1, the control plane; killing it would end the API server and no failover could be seen.
- The harness is naru's `naru-cluster`, not code in this repository, so gaon does not carry its own NoCloud writer, PKI or QEMU launcher.

## Open questions

- **The Machine start allowance** is unmeasured.
- **Observing mvmd directly.** The "never on two Nodes" check reads the API; reading each Node's mvmd waits for a way naru's harness exposes it.
