//go:build e2e

package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/tooling/e2e"
)

// The failover bound is built from the numbers the architecture documents
// state: a Lease is confirmed expired within its duration, the margin and one
// renew period of the kill (node-lifecycle.md, resource-api.md §Lease and
// fencing); the Scheduler then polls every interval (scheduler.md) and the
// Node Agent reconciles every resync period (node-agent.md). The time mvmd
// takes to start a MicroVM is not stated anywhere, so startAllowance is the
// test's own figure.
const (
	leaseDuration     = 30 * time.Second
	leaseMargin       = 5 * time.Second
	renewPeriod       = 5 * time.Second
	schedulerInterval = 5 * time.Second
	resyncPeriod      = 10 * time.Second
	startAllowance    = 30 * time.Second

	expiryBound   = leaseDuration + leaseMargin + renewPeriod
	failoverBound = expiryBound + schedulerInterval + resyncPeriod + startAllowance

	pollInterval = 500 * time.Millisecond
	setupTimeout = 3 * time.Minute
)

const microvmName = "e2e-web"

func TestFailover_AMicroVMAppliedWithTheCLIRunsOnAnotherNodeAfterItsNodeIsKilled(t *testing.T) {
	cluster := bootCluster(t)
	victim, keeper := cluster.Workers[0], cluster.ControlPlane
	require.NotEqual(t, victim.Name, keeper.Name)
	client := apiClient(t, cluster)
	ctx := context.Background()

	// Only the victim is admitted, so the Scheduler can bind the MicroVM nowhere else.
	apply(t, cluster, nodeYAML(victim.Name))
	first := waitMicroVM(t, ctx, client, setupTimeout, "the MicroVM Running on "+victim.Name, func(m *api.MicroVM) bool {
		return m.Spec.NodeName == victim.Name && m.EffectivePhase() == api.MicroVMRunning
	}, func() { apply(t, cluster, microvmYAML(cluster.Image)) })
	require.Equal(t, api.Epoch(1), first.Spec.BindingEpoch, "the first binding is epoch 1")
	t.Logf("microvm %s is Running on %s at epoch %d", microvmName, victim.Name, first.Spec.BindingEpoch)

	// The other Node is admitted now, so it is where the MicroVM can go.
	apply(t, cluster, nodeYAML(keeper.Name))
	require.Eventually(t, func() bool {
		n, err := client.GetNode(ctx, keeper.Name)
		return err == nil && n.Status.State == api.NodeStateAdmitted && condition(n, api.NodeReady) == api.ConditionTrue
	}, setupTimeout, time.Second, keeper.Name+" is Admitted and Ready")

	require.True(t, alive(victim.PID), "%s runs before the kill", victim.Name)
	killed := time.Now()
	require.NoError(t, syscall.Kill(victim.PID, syscall.SIGKILL))
	require.Eventually(t, func() bool { return !alive(victim.PID) }, time.Minute, 50*time.Millisecond, victim.Name+"'s QEMU process is gone")
	t.Logf("killed %s (pid %d)", victim.Name, victim.PID)

	var expiredAfter time.Duration
	last := first
	for {
		elapsed := time.Since(killed)
		// The MicroVM is read before the Node: a binding seen at epoch 2 was
		// written after LeaseExpired, which stays True while the Node is down.
		m, err := client.GetMicroVM(ctx, microvmName)
		require.NoError(t, err)
		n, err := client.GetNode(ctx, victim.Name)
		require.NoError(t, err)

		require.GreaterOrEqual(t, m.Spec.BindingEpoch, last.Spec.BindingEpoch, "the epoch never goes down")
		if m.Spec.BindingEpoch == last.Spec.BindingEpoch {
			require.Equal(t, last.Spec.NodeName, m.Spec.NodeName, "the MicroVM moves only with a new epoch")
		} else {
			require.Equal(t, last.Spec.BindingEpoch+1, m.Spec.BindingEpoch, "the epoch goes up by one")
			require.Equal(t, keeper.Name, m.Spec.NodeName, "the MicroVM is rebound to the other Node")
			require.Equal(t, api.ConditionTrue, condition(n, api.NodeLeaseExpired), "the MicroVM is rebound only after %s's Lease expiry is confirmed", victim.Name)
		}
		require.False(t, alive(victim.PID), "%s stays down, so it runs nothing", victim.Name)

		if expiredAfter == 0 && condition(n, api.NodeLeaseExpired) == api.ConditionTrue {
			expiredAfter = elapsed
			t.Logf("LeaseExpired is True on %s %s after the kill", victim.Name, elapsed.Round(time.Millisecond))
		}
		if expiredAfter == 0 && elapsed > expiryBound {
			require.FailNow(t, "the node lifecycle controller did not confirm the Lease expiry",
				"%s has no LeaseExpired condition True %s after its kill, the bound node-lifecycle.md gives", victim.Name, expiryBound)
		}
		last = m
		if m.Spec.NodeName == keeper.Name && m.EffectivePhase() == api.MicroVMRunning {
			require.Equal(t, first.Spec.BindingEpoch+1, m.Status.BindingEpoch)
			t.Logf("microvm %s is Running on %s at epoch %d, %s after the kill", microvmName, keeper.Name, m.Status.BindingEpoch, elapsed.Round(time.Millisecond))
			return
		}
		if elapsed > failoverBound {
			require.FailNow(t, "the MicroVM is not Running on another Node in time",
				"after %s: nodeName %q, epoch %d, phase %q", failoverBound, m.Spec.NodeName, m.Spec.BindingEpoch, m.EffectivePhase())
		}
		time.Sleep(pollInterval)
	}
}

func apiClient(t *testing.T, c e2e.Cluster) *api.Client {
	t.Helper()
	httpClient, err := api.NewTLSClient(c.CA, c.Cert, c.Key)
	require.NoError(t, err)
	return api.NewClient(c.APIRoot, httpClient)
}

func condition(n *api.Node, kind string) api.ConditionStatus {
	for _, c := range n.Status.Conditions {
		if c.Type == kind {
			return c.Status
		}
	}
	return api.ConditionFalse
}

// waitMicroVM polls the MicroVM until ok holds and returns it. before runs once
// first. It fails with the last state read when the timeout passes.
func waitMicroVM(t *testing.T, ctx context.Context, client *api.Client, timeout time.Duration, what string, ok func(*api.MicroVM) bool, before func()) *api.MicroVM {
	t.Helper()
	before()
	deadline := time.Now().Add(timeout)
	state := "no MicroVM read yet"
	for time.Now().Before(deadline) {
		if m, err := client.GetMicroVM(ctx, microvmName); err == nil {
			if ok(m) {
				return m
			}
			state = fmt.Sprintf("nodeName %q, epoch %d, phase %q", m.Spec.NodeName, m.Spec.BindingEpoch, m.EffectivePhase())
		}
		time.Sleep(time.Second)
	}
	require.FailNow(t, "timed out waiting for "+what, "%s; last state: %s", timeout, state)
	return nil
}

// apply writes doc to a file and applies it with the gaon CLI built from the checkout.
func apply(t *testing.T, c e2e.Cluster, doc string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	out, err := exec.Command(env.gaon, "--server", c.APIRoot, "--tls-ca", c.CA, "--tls-cert", c.Cert, "--tls-key", c.Key, "apply", "-f", path).CombinedOutput() //nolint:gosec // the CLI make e2e built
	require.NoError(t, err, string(out))
	t.Logf("gaon apply: %s", strings.TrimSpace(string(out)))
}

func nodeYAML(name string) string {
	return "apiVersion: compute.gaon.garam.sh/v1alpha1\nkind: Node\nmetadata:\n  name: " + name + "\nspec:\n  admitted: true\n"
}

func microvmYAML(image string) string {
	return `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: MicroVM
metadata:
  name: ` + microvmName + `
spec:
  image: ` + image + `
  runStrategy: Always
  rootDiskSize: 256Mi
  resources:
    requests:
      cpu: 500m
      memory: 256Mi
`
}
