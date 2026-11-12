//go:build e2e

// Package tests holds the end-to-end test of M1: gaon binaries built from the
// checkout run on QEMU Nodes that naru-cluster boots. `make e2e` sets the
// environment the test reads.
package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/tooling/e2e"
)

const clusterUpTimeout = 8 * time.Minute

// env is what `make e2e` hands the test.
var env struct {
	naruCluster string // the naru-cluster binary
	release     string // naru's release directory
	services    string // the gaon service binaries for the Nodes, as -gaon-bin
	gaon        string // the gaon CLI for the test host
	dir         string // the directory the run keeps its files in
	disable     string // services to leave out of the cluster, empty for none
}

func TestMain(m *testing.M) {
	for name, dst := range map[string]*string{
		"E2E_NARU_CLUSTER": &env.naruCluster,
		"E2E_RELEASE":      &env.release,
		"E2E_SERVICES":     &env.services,
		"E2E_GAON":         &env.gaon,
		"E2E_DIR":          &env.dir,
	} {
		*dst = os.Getenv(name)
		if *dst == "" {
			fmt.Fprintf(os.Stderr, "%s is required; make e2e sets it\n", name)
			os.Exit(1)
		}
	}
	env.disable = os.Getenv("E2E_DISABLE")
	os.Exit(m.Run())
}

// bootCluster runs naru-cluster with two Nodes and the gaon binaries of the
// checkout, waits for cluster.json, and stops every Node when the test ends.
// With E2E_DISABLE set it leaves those services out, which needs operator
// certificates instead of the boot PKI.
func bootCluster(t *testing.T) e2e.Cluster {
	t.Helper()
	clusterDir := filepath.Join(env.dir, "cluster")
	logFile, err := os.Create(filepath.Join(env.dir, "naru-cluster.log"))
	require.NoError(t, err)
	args := []string{"-release", env.release, "-dir", clusterDir, "-n", "2", "-gaon-bin", env.services}
	if env.disable != "" {
		args = append(args, "-boot-pki=false", "-disable", env.disable)
	}
	cmd := exec.Command(env.naruCluster, args...) //nolint:gosec // the binary make e2e installed
	cmd.Stdout, cmd.Stderr = logFile, logFile
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(time.Minute):
			_ = cmd.Process.Kill()
			t.Errorf("naru-cluster did not stop within a minute of SIGTERM")
		}
		for _, pid := range qemuUnder(clusterDir) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("the QEMU process %d outlived naru-cluster", pid)
		}
	})

	path := filepath.Join(clusterDir, "cluster.json")
	deadline := time.After(clusterUpTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			c, err := e2e.ReadClusterFile(path)
			require.NoError(t, err)
			return c
		}
		select {
		case <-exited:
			require.FailNow(t, "naru-cluster exited before cluster.json appeared", "see %s", logFile.Name())
		case <-deadline:
			require.FailNow(t, "no cluster.json in time", "%s; see %s", clusterUpTimeout, logFile.Name())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// alive reports whether the process runs; a zombie waiting to be reaped does not.
func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := strings.LastIndex(string(b), ") ")
	return i >= 0 && len(b) > i+2 && b[i+2] != 'Z'
}

// qemuUnder is the pids of the QEMU processes whose command line names dir.
func qemuUnder(dir string) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !alive(pid) {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if strings.Contains(string(cmdline), "qemu-system") && strings.Contains(string(cmdline), dir) {
			pids = append(pids, pid)
		}
	}
	return pids
}
