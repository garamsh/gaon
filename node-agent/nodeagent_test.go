package nodeagent_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/liveness"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var required = map[string]string{
	nodeagent.EnvAPIServer:  "http://127.0.0.1:1",
	nodeagent.EnvNodeName:   "n1",
	nodeagent.EnvMvmdSocket: "/run/mvmd.sock",
	nodeagent.EnvDelta:      "3s",
}

func TestParseConfig_ReadsEveryValueFromItsEnvironmentVariable(t *testing.T) {
	cfg, err := nodeagent.ParseConfig(nil, env(map[string]string{
		nodeagent.EnvAPIServer:      "http://api",
		nodeagent.EnvNodeName:       "node-a",
		nodeagent.EnvMvmdSocket:     "/s.sock",
		nodeagent.EnvDelta:          "4s",
		nodeagent.EnvLeaseDuration:  "40s",
		nodeagent.EnvRenewPeriod:    "6s",
		nodeagent.EnvDeadlineLength: "20s",
		nodeagent.EnvRPCTimeout:     "3s",
		nodeagent.EnvResyncPeriod:   "7s",

		nodeagent.EnvRestartBackoff:    "3s",
		nodeagent.EnvRestartBackoffMax: "2m",
		nodeagent.EnvRestartResetAfter: "8m",
	}), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, nodeagent.Config{
		APIServer: "http://api", HTTPClient: &http.Client{}, NodeName: "node-a", MvmdSocket: "/s.sock", ResyncPeriod: 7 * time.Second,
		RestartBackoff: 3 * time.Second, RestartBackoffMax: 2 * time.Minute, RestartResetAfter: 8 * time.Minute,
		PluginDir: plugin.DefaultDir,
		Liveness:  liveness.Params{LeaseDuration: 40 * time.Second, RenewPeriod: 6 * time.Second, DeadlineLength: 20 * time.Second, Delta: 4 * time.Second, RPCTimeout: 3 * time.Second},
	}, cfg)
}

func TestParseConfig_NamesThePluginsAndTheirDirectoryByFlagOrEnvironmentAndDefaultsToNone(t *testing.T) {
	required := map[string]string{nodeagent.EnvAPIServer: "http://api", nodeagent.EnvNodeName: "n1", nodeagent.EnvMvmdSocket: "/s.sock"}
	byDefault, err := nodeagent.ParseConfig(nil, env(required), io.Discard)
	require.NoError(t, err)
	assert.Empty(t, byDefault.NetworkPlugin, "no network plugin unless one is named")
	assert.Empty(t, byDefault.StoragePlugin, "no storage plugin unless one is named")
	assert.Equal(t, plugin.DefaultDir, byDefault.PluginDir)

	byFlag, err := nodeagent.ParseConfig([]string{"--network-plugin=default", "--storage-plugin=local", "--plugin-dir=/d"}, env(required), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "default", byFlag.NetworkPlugin)
	assert.Equal(t, "local", byFlag.StoragePlugin)
	assert.Equal(t, "/d", byFlag.PluginDir)

	required[nodeagent.EnvNetworkPlugin], required[nodeagent.EnvStoragePlugin], required[nodeagent.EnvPluginDir] = "acme-net", "acme-disk", "/e"
	byEnv, err := nodeagent.ParseConfig(nil, env(required), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "acme-net", byEnv.NetworkPlugin)
	assert.Equal(t, "acme-disk", byEnv.StoragePlugin)
	assert.Equal(t, "/e", byEnv.PluginDir)

	byBoth, err := nodeagent.ParseConfig([]string{"--network-plugin=other"}, env(required), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "other", byBoth.NetworkPlugin, "a flag given wins over its variable")
}

func TestParseConfig_DefaultsDeltaToOneSecondAndEveryDefaultPassesTheChecks(t *testing.T) {
	cfg, err := nodeagent.ParseConfig(nil, env(map[string]string{
		nodeagent.EnvAPIServer:  "http://api",
		nodeagent.EnvNodeName:   "n1",
		nodeagent.EnvMvmdSocket: "/s.sock",
	}), io.Discard)
	require.NoError(t, err, "the start-time duration checks hold with every default")
	assert.Equal(t, time.Second, cfg.Liveness.Delta)
	assert.Equal(t, 10*time.Second, cfg.RestartBackoff, "resource-api.md's provisional first wait")
	assert.Equal(t, 5*time.Minute, cfg.RestartBackoffMax)
	assert.Equal(t, 10*time.Minute, cfg.RestartResetAfter)
}

func TestParseConfig_FlagsOverrideEnvironment(t *testing.T) {
	cfg, err := nodeagent.ParseConfig([]string{"--node-name=from-flag", "--api-server", "http://flag", "--mvmd-socket=/f.sock", "--delta=2s", "--restart-backoff=4s", "--restart-backoff-max=1m", "--restart-reset-after=2m"}, env(required), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.NodeName)
	assert.Equal(t, "http://flag", cfg.APIServer)
	assert.Equal(t, "/f.sock", cfg.MvmdSocket)
	assert.Equal(t, 2*time.Second, cfg.Liveness.Delta)
	assert.Equal(t, 4*time.Second, cfg.RestartBackoff)
	assert.Equal(t, time.Minute, cfg.RestartBackoffMax)
	assert.Equal(t, 2*time.Minute, cfg.RestartResetAfter)
}

func TestParseConfig_RefusesWhatItCannotRunWith(t *testing.T) {
	tests := []struct {
		name string
		drop string
		set  map[string]string
		want string
	}{
		{"no api server", nodeagent.EnvAPIServer, nil, "api-server"},
		{"no node name", nodeagent.EnvNodeName, nil, "node-name"},
		{"no socket", nodeagent.EnvMvmdSocket, nil, "mvmd-socket"},
		{"a delta the Lease duration cannot cover", "", map[string]string{nodeagent.EnvDelta: "12s"}, "lease duration"},
		{"a duration that is not one", "", map[string]string{nodeagent.EnvRenewPeriod: "soon"}, nodeagent.EnvRenewPeriod},
		{"a restart backoff above its maximum", "", map[string]string{nodeagent.EnvRestartBackoff: "10m"}, "restart backoff"},
		{"a restart backoff of zero", "", map[string]string{nodeagent.EnvRestartBackoff: "0s"}, "restart backoff"},
		{"a restart reset time of zero", "", map[string]string{nodeagent.EnvRestartResetAfter: "0s"}, "restart backoff"},
		{"a network plugin name that cannot be in a socket path", "", map[string]string{nodeagent.EnvNetworkPlugin: "../calico"}, "../calico"},
		{"a storage plugin name that cannot be in a socket path", "", map[string]string{nodeagent.EnvStoragePlugin: "Ceph/1"}, "Ceph/1"},
		{"durations that do not fit", "", map[string]string{nodeagent.EnvLeaseDuration: "10s"}, "lease duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range required {
				if k != tt.drop {
					m[k] = v
				}
			}
			for k, v := range tt.set {
				m[k] = v
			}
			_, err := nodeagent.ParseConfig(nil, env(m), io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	_, err := nodeagent.ParseConfig(nil, env(required), io.Discard)
	assert.NoError(t, err, "control: the same environment with everything present is accepted")
}

func TestRun_RegistersTheNodeRenewsItsLeaseAndRunsABoundMicroVM(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clk)
	cfg := nodeagent.Config{
		PluginDir: t.TempDir(),
		APIServer: gaon.URL, NodeName: "n1", MvmdSocket: mvmd.Socket, ResyncPeriod: 20 * time.Millisecond,
		Liveness: liveness.Params{LeaseDuration: 30 * time.Second, RenewPeriod: 20 * time.Millisecond, DeadlineLength: 5 * time.Second, Delta: time.Second, RPCTimeout: time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- nodeagent.Run(ctx, cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { cancel(); <-done })

	gaon.SetMicroVM(api.MicroVM{
		Metadata: api.ObjectMeta{Name: "m1"},
		Spec: api.MicroVMSpec{
			Image:        "registry.example/app@sha256:" + strings.Repeat("b", 64),
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "512Mi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     "n1",
			BindingEpoch: 1,
		},
	})

	require.Eventually(t, func() bool {
		s := gaon.MicroVM("m1").Status
		return s.Phase == api.MicroVMRunning && s.BindingEpoch == 1
	}, 5*time.Second, 10*time.Millisecond, "the bound MicroVM runs, which needs the deadline armed by the renewal loop")
	require.Eventually(t, func() bool {
		n, ok := gaon.Node("n1")
		return ok && n.Status.Capacity.CPU == "8"
	}, 5*time.Second, 10*time.Millisecond, "the Node's status is written by its own loop, independent of the MicroVM")
	lease, ok := gaon.Lease("n1")
	require.True(t, ok)
	assert.Equal(t, "n1", lease.Spec.Holder)
	renewals := mvmd.Renewals()
	require.NotEmpty(t, renewals)
	assert.Equal(t, 5*time.Second, renewals[0].Length)
	assert.Equal(t, v1alpha2.MachineState_MACHINE_STATE_RUNNING, mustMicroVM(t, mvmd, "m1-e1").GetState())
}

// runAgent runs the agent against gaon and mvmd with fast periods and no
// plugin until the test ends.
func runAgent(t *testing.T, gaon *fakegaon.Server, mvmd *fakemvmd.Server) {
	t.Helper()
	runAgentWith(t, gaon, mvmd, nodeagent.Config{})
}

// runAgentWith is runAgent with the plugin settings of plugins.
func runAgentWith(t *testing.T, gaon *fakegaon.Server, mvmd *fakemvmd.Server, plugins nodeagent.Config) {
	t.Helper()
	clk := clock.System{}
	cfg := nodeagent.Config{
		PluginDir: plugins.PluginDir, NetworkPlugin: plugins.NetworkPlugin, StoragePlugin: plugins.StoragePlugin,
		APIServer: gaon.URL, NodeName: "n1", MvmdSocket: mvmd.Socket, ResyncPeriod: 20 * time.Millisecond,
		RestartBackoff: time.Hour, RestartBackoffMax: time.Hour, RestartResetAfter: time.Hour,
		Liveness: liveness.Params{LeaseDuration: 30 * time.Second, RenewPeriod: 20 * time.Millisecond, DeadlineLength: 5 * time.Second, Delta: time.Second, RPCTimeout: time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- nodeagent.Run(ctx, cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { cancel(); <-done })
}

func statusCode(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestRun_MakesNoRequestThatNeedsItsNodeBeforeTheNodeExists(t *testing.T) {
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clock.System{})
	gaon.RequireNode("n1")
	gaon.HoldNodeCreation(true)
	assert.Equal(t, http.StatusForbidden, statusCode(t, http.MethodGet, gaon.URL+"/microvms"), "control: the server refuses a MicroVM list while the Node is absent")
	assert.Equal(t, http.StatusUnprocessableEntity, statusCode(t, http.MethodPut, gaon.URL+"/leases/n1"), "control: and a Lease write")
	require.Len(t, gaon.Refused(), 2, "the controls were refused for the Node's absence")
	_, ok := gaon.Node("n1")
	require.False(t, ok, "the Node is absent")

	runAgent(t, gaon, mvmd)
	require.Eventually(t, func() bool { return gaon.NodeCreations() >= 3 }, 5*time.Second, 5*time.Millisecond, "the agent keeps asking for its Node")
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, gaon.Refused(), 2, "nothing but the controls was refused")
	assert.Empty(t, gaon.LeaseWrites())

	gaon.HoldNodeCreation(false)
	gaon.SetMicroVM(api.MicroVM{
		Metadata: api.ObjectMeta{Name: "m1"},
		Spec: api.MicroVMSpec{
			Image:        "registry.example/app@sha256:" + strings.Repeat("b", 64),
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "512Mi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     "n1",
			BindingEpoch: 1,
		},
	})
	require.Eventually(t, func() bool {
		_, leased := gaon.Lease("n1")
		return leased && gaon.MicroVM("m1").Status.Phase == api.MicroVMRunning
	}, 5*time.Second, 10*time.Millisecond, "once the Node exists the Lease is written and the MicroVM runs")
	assert.Len(t, gaon.Refused(), 2, "no request of the agent's was refused")
}

func TestRun_StartsAtOnceWhenItsNodeAlreadyExists(t *testing.T) {
	gaon, mvmd := fakegaon.New(t), fakemvmd.New(t, clock.System{})
	gaon.RequireNode("n1")
	gaon.HoldNodeCreation(true)
	gaon.SetNode(api.Node{Metadata: api.ObjectMeta{Name: "n1"}})
	assert.Equal(t, http.StatusOK, statusCode(t, http.MethodGet, gaon.URL+"/microvms"), "control: with the Node present the server answers")

	runAgent(t, gaon, mvmd)
	gaon.SetMicroVM(api.MicroVM{
		Metadata: api.ObjectMeta{Name: "m1"},
		Spec: api.MicroVMSpec{
			Image:        "registry.example/app@sha256:" + strings.Repeat("b", 64),
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "512Mi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     "n1",
			BindingEpoch: 1,
		},
	})
	require.Eventually(t, func() bool {
		_, leased := gaon.Lease("n1")
		return leased && gaon.MicroVM("m1").Status.Phase == api.MicroVMRunning
	}, 5*time.Second, 10*time.Millisecond, "the Lease is written and the MicroVM runs with Node creation held")
	assert.Zero(t, gaon.NodeCreations(), "no create was asked for")
	assert.Empty(t, gaon.Refused())
}

func mustMicroVM(t *testing.T, m *fakemvmd.Server, id string) *v1alpha2.Machine {
	t.Helper()
	got, ok := m.Machine(id)
	require.True(t, ok)
	return got
}
