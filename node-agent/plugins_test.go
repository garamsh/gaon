package nodeagent_test

// These tests run the agent against plugin processes: the two built-in
// plugins, built from their own modules with go build and started as
// binaries, and one plugin written for the test. Nothing of a plugin is
// compiled into the agent, so what they show is what the plugin API carries.

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin/plugintest"
	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/microvms"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

var binaries struct {
	dir                          string
	networkDefault, storageLocal string
	foreign                      string
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "gaon-plugins-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binaries.dir = dir
	for _, b := range []struct {
		out    *string
		module string
		pkg    string
	}{
		{&binaries.networkDefault, "../network-default", "./cmd/gaon-network-default"},
		{&binaries.storageLocal, "../storage-local", "./cmd/gaon-storage-local"},
		{&binaries.foreign, ".", "./testdata/foreignplugin"},
	} {
		*b.out = filepath.Join(dir, filepath.Base(b.pkg))
		cmd := exec.Command("go", "build", "-o", *b.out, b.pkg)
		cmd.Dir = b.module
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", b.pkg, err, out)
			return 1
		}
	}
	return m.Run()
}

// process is a plugin started by a test.
type process struct {
	cmd  *exec.Cmd
	log  *lockedBuffer
	done chan struct{}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startPlugin starts the binary with args and returns once its socket
// answers connections. It is stopped when the test ends.
func startPlugin(t *testing.T, socket, binary string, args ...string) *process {
	t.Helper()
	p := &process{cmd: exec.Command(binary, args...), log: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd.Stderr, p.cmd.Stdout = p.log, p.log
	require.NoError(t, p.cmd.Start())
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(p.stop)
	require.Eventually(t, func() bool {
		_, err := os.Stat(socket)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the plugin serves %s; its log:\n%s", socket, p.log)
	return p
}

// stop ends the process and waits for it to exit.
func (p *process) stop() {
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func bound(name string, volumes ...string) api.MicroVM {
	m := api.MicroVM{
		Metadata: api.ObjectMeta{Name: name, Generation: 1},
		Spec: api.MicroVMSpec{
			Image:        "registry.example/app@sha256:" + strings.Repeat("b", 64),
			Resources:    api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "512Mi"}},
			RunStrategy:  api.RunAlways,
			NodeName:     "n1",
			BindingEpoch: 1,
		},
	}
	for _, v := range volumes {
		m.Spec.Volumes = append(m.Spec.Volumes, api.MicroVMVolume{Name: v, MountPath: "/mnt/" + v})
	}
	return m
}

type world struct {
	gaon *fakegaon.Server
	mvmd *fakemvmd.Server
	dir  string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{gaon: fakegaon.New(t), mvmd: fakemvmd.New(t, clock.System{}), dir: plugintest.SocketDir(t)}
}

func (w *world) socket(kind, name string) string { return filepath.Join(w.dir, kind+"-"+name+".sock") }

func (w *world) agent(t *testing.T, network, storage string) {
	t.Helper()
	runAgentWith(t, w.gaon, w.mvmd, nodeagent.Config{PluginDir: w.dir, NetworkPlugin: network, StoragePlugin: storage})
}

// networkDefault starts the built-in network plugin with the prefixes.
func (w *world) networkDefault(t *testing.T, args ...string) *process {
	return startPlugin(t, w.socket("network", "default"), binaries.networkDefault, append([]string{"--plugin-dir=" + w.dir}, args...)...)
}

// storageLocal starts the built-in storage plugin for the Node n1.
func (w *world) storageLocal(t *testing.T) *process {
	return startPlugin(t, w.socket("storage", "local"), binaries.storageLocal,
		"--plugin-dir="+w.dir, "--api-server="+w.gaon.URL, "--node-name=n1", "--mvmd-socket="+w.mvmd.Socket, "--resync-period=20ms")
}

func (w *world) running(id string) func() bool {
	return func() bool {
		m, ok := w.mvmd.Machine(id)
		return ok && m.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING
	}
}

func (w *world) ready(name string) api.Condition {
	s := w.gaon.MicroVM(name).Status
	if len(s.Conditions) != 1 {
		return api.Condition{}
	}
	return s.Conditions[0]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 10*time.Second, 10*time.Millisecond, what)
}

func TestPlugins_TheDefaultNetworkBinaryGivesAMicroVMTheEgressAndIngressOfItsFlags(t *testing.T) {
	w := newWorld(t)
	w.networkDefault(t, "--egress-prefix=10.0.0.0/8", "--egress-prefix=2001:db8::/32", "--ingress-prefix=192.168.0.0/16")
	w.agent(t, "default", "")
	web := bound("web")
	web.Spec.Ports = []api.MicroVMPort{{Port: 80, Protocol: api.ProtocolTCP}, {Port: 443, Protocol: api.ProtocolTCP}}
	w.gaon.SetMicroVM(web)
	w.gaon.SetMicroVM(bound("quiet"))

	eventually(t, "web-e1 runs", w.running("web-e1"))
	eventually(t, "quiet-e1 runs", w.running("quiet-e1"))

	tcp := func(port uint32) *v1alpha2.NetworkRule {
		return &v1alpha2.NetworkRule{Prefix: "192.168.0.0/16", Protocol: v1alpha2.Protocol_PROTOCOL_TCP, Ports: []*v1alpha2.PortRange{{First: port, Last: port}}}
	}
	want := &v1alpha2.NetworkPolicy{
		Egress: []*v1alpha2.NetworkRule{
			{Prefix: "10.0.0.0/8", Protocol: v1alpha2.Protocol_PROTOCOL_ANY},
			{Prefix: "2001:db8::/32", Protocol: v1alpha2.Protocol_PROTOCOL_ANY},
		},
		Ingress: []*v1alpha2.NetworkRule{tcp(80), tcp(443)},
	}
	got := mustMicroVM(t, w.mvmd, "web-e1").GetNetworkPolicy()
	assert.True(t, proto.Equal(want, got), "got %v", got)
	quiet := mustMicroVM(t, w.mvmd, "quiet-e1").GetNetworkPolicy()
	assert.Len(t, quiet.GetEgress(), 2, "control: egress does not depend on the ports")
	assert.Empty(t, quiet.GetIngress(), "a MicroVM with no ports accepts nothing")
}

func TestPlugins_TheDefaultNetworkBinaryRefusesAMicroVMNamingANetworkItDoesNotServe(t *testing.T) {
	w := newWorld(t)
	w.networkDefault(t)
	w.agent(t, "default", "")
	other := bound("other")
	other.Spec.Networks = []string{"default", "tenant-b"}
	w.gaon.SetMicroVM(other)
	w.gaon.SetMicroVM(bound("fine"))

	eventually(t, "other is Failed", func() bool { return w.gaon.MicroVM("other").Status.Phase == api.MicroVMFailed })
	c := w.ready("other")
	assert.Equal(t, "UnknownNetwork", c.Reason)
	assert.Contains(t, c.Message, "tenant-b")
	eventually(t, "control: a MicroVM naming no network runs", w.running("fine-e1"))
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine other-e1", "a refused MicroVM is never created, so it never runs unprotected")
}

func TestPlugins_ANetworkPluginThatIsNotRunningLeavesTheMicroVMPendingWithTheReasonAndItIsCreatedOnceThePluginStarts(t *testing.T) {
	w := newWorld(t)
	w.agent(t, "default", "")
	w.gaon.SetMicroVM(bound("web"))

	eventually(t, "web is Pending, waiting for the plugin", func() bool {
		return w.ready("web").Reason == "NetworkPluginUnavailable"
	})
	assert.Equal(t, api.MicroVMPending, w.gaon.MicroVM("web").Status.Phase)
	assert.Equal(t, api.ConditionFalse, w.ready("web").Status)
	time.Sleep(100 * time.Millisecond)
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine web-e1", "no MicroVM is created before its network policy is known")

	w.networkDefault(t, "--egress-prefix=10.0.0.0/8")
	eventually(t, "web runs once the plugin serves", w.running("web-e1"))
	eventually(t, "web is Running", func() bool { return w.gaon.MicroVM("web").Status.Phase == api.MicroVMRunning })
	assert.Equal(t, "Running", w.ready("web").Reason)
	assert.Len(t, mustMicroVM(t, w.mvmd, "web-e1").GetNetworkPolicy().GetEgress(), 1)
}

func TestPlugins_APluginThatStopsAndStartsAgainServesTheNextMicroVM(t *testing.T) {
	w := newWorld(t)
	p := w.networkDefault(t)
	w.agent(t, "default", "")
	w.gaon.SetMicroVM(bound("first"))
	eventually(t, "first runs", w.running("first-e1"))

	p.stop()
	w.gaon.SetMicroVM(bound("second"))
	eventually(t, "second waits", func() bool { return w.ready("second").Reason == "NetworkPluginUnavailable" })

	w.networkDefault(t)
	eventually(t, "second runs after the plugin restarted", w.running("second-e1"))
}

func TestPlugins_APluginTheAgentDidNotBuildServesAndRefusesThroughTheSameSocketDirectory(t *testing.T) {
	w := newWorld(t)
	startPlugin(t, w.socket("network", "acme"), binaries.foreign, w.dir)
	w.agent(t, "acme", "")
	w.gaon.SetMicroVM(bound("web"))
	w.gaon.SetMicroVM(bound("forbidden-one"))

	eventually(t, "web runs", w.running("web-e1"))
	want := &v1alpha2.NetworkPolicy{Ingress: []*v1alpha2.NetworkRule{{Prefix: "10.9.0.0/16", Protocol: v1alpha2.Protocol_PROTOCOL_ICMP}}}
	got := mustMicroVM(t, w.mvmd, "web-e1").GetNetworkPolicy()
	assert.True(t, proto.Equal(want, got), "got %v", got)

	eventually(t, "forbidden-one is Failed", func() bool { return w.gaon.MicroVM("forbidden-one").Status.Phase == api.MicroVMFailed })
	assert.Equal(t, "AcmeForbidden", w.ready("forbidden-one").Reason, "the plugin's own reason reaches the condition")
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine forbidden-one-e1")
}

func TestPlugins_WithNoPluginNamedAMicroVMRunsIsolatedAndOneNamingAVolumeIsRefused(t *testing.T) {
	w := newWorld(t)
	w.agent(t, "", "")
	w.gaon.SetMicroVM(bound("plain"))
	w.gaon.SetMicroVM(bound("needs-disk", "data"))

	eventually(t, "plain runs", w.running("plain-e1"))
	policy := mustMicroVM(t, w.mvmd, "plain-e1").GetNetworkPolicy()
	assert.Empty(t, policy.GetEgress())
	assert.Empty(t, policy.GetIngress(), "isolated")
	eventually(t, "needs-disk is Failed", func() bool { return w.gaon.MicroVM("needs-disk").Status.Phase == api.MicroVMFailed })
	assert.Equal(t, "NoStoragePlugin", w.ready("needs-disk").Reason)
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine needs-disk-e1")
}

func localVolume(name, nodeName string) api.Volume {
	return api.Volume{
		Metadata: api.ObjectMeta{Name: name, Generation: 1, Finalizers: []string{api.NodeAgentFinalizer}},
		Spec: api.VolumeSpec{
			StorageClass: api.StorageClassLocal, AccessModes: []string{api.AccessReadWriteOnce},
			Storage: "10Mi", NodeName: nodeName,
		},
		Status: api.VolumeStatus{Phase: api.VolumePending},
	}
}

func TestPlugins_TheLocalStorageBinaryServesAVolumeThroughTheAgentFromCreateToDelete(t *testing.T) {
	w := newWorld(t)
	w.storageLocal(t)
	w.agent(t, "", "local")
	w.gaon.SetVolume(localVolume("data", "n1"))
	w.gaon.SetVolume(localVolume("elsewhere", "n2"))

	eventually(t, "data is Available", func() bool {
		v, _ := w.gaon.Volume("data")
		return v.Status.Phase == api.VolumeAvailable
	})
	_, ok := w.mvmd.Disk("volume-elsewhere")
	assert.False(t, ok, "control: a Volume pinned to another Node gets no disk")

	w.gaon.SetMicroVM(bound("db", "data"))
	eventually(t, "db runs with the disk", func() bool {
		m, ok := w.mvmd.Machine("db-e1")
		return ok && m.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING && len(m.GetDisks()) == 1
	})
	assert.Equal(t, "volume-data", mustMicroVM(t, w.mvmd, "db-e1").GetDisks()[0].GetDiskId())
	eventually(t, "data is Bound to db", func() bool {
		v, _ := w.gaon.Volume("data")
		return v.Status.Phase == api.VolumeBound && v.Status.Attachment != nil && v.Status.Attachment.MicroVMName == "db"
	})

	w.gaon.DeleteMicroVM("db")
	eventually(t, "data is Released once db is gone", func() bool {
		v, _ := w.gaon.Volume("data")
		return v.Status.Phase == api.VolumeReleased
	})
	v, _ := w.gaon.Volume("data")
	assert.Equal(t, &api.Attachment{MicroVMName: "db", BindingEpoch: 1}, v.Status.Attachment)
	_, ok = w.mvmd.Disk("volume-data")
	assert.True(t, ok, "the disk outlives the MicroVM")
	assert.NotContains(t, w.mvmd.IDs(), "db-e1", "the MicroVM is removed from mvmd")
}

func TestPlugins_TheLocalStorageBinaryRefusesAMicroVMNamingAVolumeThatIsNotThereAndServesTheOthers(t *testing.T) {
	w := newWorld(t)
	w.storageLocal(t)
	w.agent(t, "", "local")
	w.gaon.SetVolume(localVolume("data", "n1"))
	w.gaon.SetMicroVM(bound("lost", "missing"))
	w.gaon.SetMicroVM(bound("found", "data"))

	eventually(t, "lost is Failed", func() bool { return w.gaon.MicroVM("lost").Status.Phase == api.MicroVMFailed })
	assert.Equal(t, "VolumeNotFound", w.ready("lost").Reason)
	eventually(t, "control: a MicroVM whose Volume exists runs", w.running("found-e1"))
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine lost-e1")
}

func TestPlugins_ANetworkPluginThatNeverAnswersLeavesTheMicroVMPendingWithTheReasonWithinTheTimeout(t *testing.T) {
	w := newWorld(t)
	lis, err := net.Listen("unix", w.socket("network", "hung"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	runAgentWith(t, w.gaon, w.mvmd, nodeagent.Config{PluginDir: w.dir, NetworkPlugin: "hung", PluginTimeout: 200 * time.Millisecond})
	w.gaon.SetMicroVM(bound("web"))

	require.Eventually(t, func() bool {
		s := w.gaon.MicroVM("web").Status
		return len(s.Conditions) == 1 && s.Conditions[0].Reason == microvms.ReasonNetworkPluginUnavailable
	}, 5*time.Second, 20*time.Millisecond, "web is Pending with the plugin-unavailable reason")
	s := w.gaon.MicroVM("web").Status
	assert.Equal(t, api.MicroVMPending, s.Phase)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	assert.NotContains(t, w.mvmd.Calls(), "CreateMachine web-e1")
}
