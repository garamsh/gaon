package microvms_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/microvms"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func failedWith(t *testing.T, e *env, name, reason string) api.Condition {
	t.Helper()
	eventually(t, name+" is Failed", e.phase(name, 1, api.MicroVMFailed))
	s := e.gaon.MicroVM(name).Status
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, reason, s.Conditions[0].Reason)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	return s.Conditions[0]
}

// fixedNetwork returns one policy for every MicroVM.
type fixedNetwork struct{ policy *plugin.NetworkPolicy }

func (n fixedNetwork) Policy(context.Context, *api.MicroVM) (*plugin.NetworkPolicy, error) {
	return n.policy, nil
}

func TestNetwork_CreatesAMicroVMWithThePolicyThePluginReturnedInMvmdsTerms(t *testing.T) {
	policy := &plugin.NetworkPolicy{
		Egress: []plugin.NetworkRule{{Prefix: "10.0.0.0/8", Protocol: plugin.Any}, {Prefix: "2001:db8::/32", Protocol: plugin.UDP, Ports: []plugin.PortRange{{First: 53, Last: 53}}}},
		Ingress: []plugin.NetworkRule{{Prefix: "192.168.0.0/16", Protocol: plugin.TCP, Ports: []plugin.PortRange{{First: 80, Last: 80}, {First: 8000, Last: 8100}}},
			{Prefix: "10.1.0.0/16", Protocol: plugin.ICMP}, {Prefix: "fd00::/8", Protocol: plugin.ICMPv6}},
	}
	e := start(t, true, nil, func(e *env) { e.network = fixedNetwork{policy} })
	e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))

	eventually(t, "web-e1 runs", e.running("web-e1"))

	want := &v1alpha2.NetworkPolicy{
		Egress: []*v1alpha2.NetworkRule{
			{Prefix: "10.0.0.0/8", Protocol: v1alpha2.Protocol_PROTOCOL_ANY},
			{Prefix: "2001:db8::/32", Protocol: v1alpha2.Protocol_PROTOCOL_UDP, Ports: []*v1alpha2.PortRange{{First: 53, Last: 53}}},
		},
		Ingress: []*v1alpha2.NetworkRule{
			{Prefix: "192.168.0.0/16", Protocol: v1alpha2.Protocol_PROTOCOL_TCP, Ports: []*v1alpha2.PortRange{{First: 80, Last: 80}, {First: 8000, Last: 8100}}},
			{Prefix: "10.1.0.0/16", Protocol: v1alpha2.Protocol_PROTOCOL_ICMP},
			{Prefix: "fd00::/8", Protocol: v1alpha2.Protocol_PROTOCOL_ICMPV6},
		},
	}
	got, _ := e.mvmd.Machine("web-e1")
	assert.True(t, proto.Equal(want, got.GetNetworkPolicy()), "got %v", got.GetNetworkPolicy())
}

func TestNetwork_ANilPolicyCreatesTheMicroVMIsolated(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.network = fixedNetwork{nil} })
	e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))

	eventually(t, "web-e1 runs", e.running("web-e1"))
	got, _ := e.mvmd.Machine("web-e1")
	assert.Empty(t, got.GetNetworkPolicy().GetEgress())
	assert.Empty(t, got.GetNetworkPolicy().GetIngress())
}

// absentNetwork fails every call as a plugin that is not running does, until
// it is set to answer.
type absentNetwork struct {
	mu     sync.Mutex
	absent bool
}

func (n *absentNetwork) Policy(context.Context, *api.MicroVM) (*plugin.NetworkPolicy, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.absent {
		return nil, errors.New("rpc error: code = Unavailable desc = connection refused")
	}
	return &plugin.NetworkPolicy{}, nil
}

func (n *absentNetwork) set(absent bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.absent = absent
}

func TestNetwork_ANetworkPluginThatIsNotRunningLeavesTheMicroVMPendingWithTheReasonUntilItAnswers(t *testing.T) {
	n := &absentNetwork{absent: true}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(microvm("web", 1, node, api.RunAlways))

	eventually(t, "web is Pending with the plugin's reason", func() bool {
		s := e.gaon.MicroVM("web").Status
		return len(s.Conditions) == 1 && s.Conditions[0].Reason == microvms.ReasonNetworkPluginUnavailable
	})
	s := e.gaon.MicroVM("web").Status
	assert.Equal(t, api.MicroVMPending, s.Phase)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	assert.Contains(t, s.Conditions[0].Message, "connection refused")
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine web-e1", "no MicroVM is created before its network policy is known")

	n.set(false)
	eventually(t, "web runs once the plugin answers", e.running("web-e1"))
	eventually(t, "the condition follows", e.phase("web", 1, api.MicroVMRunning))
	assert.Equal(t, microvms.ReasonRunning, e.gaon.MicroVM("web").Status.Conditions[0].Reason)
}

// absentStorage fails every Attach as a plugin that is not running does.
type absentStorage struct{ noDisks }

func (absentStorage) Attach(context.Context, *api.MicroVM) ([]plugin.DiskAttachment, error) {
	return nil, errors.New("rpc error: code = Unavailable desc = connection refused")
}

func TestStorage_AStoragePluginThatIsNotRunningLeavesTheMicroVMPendingWithTheReason(t *testing.T) {
	e := start(t, true, nil, func(e *env) { e.storage = absentStorage{} })
	e.gaon.SetMicroVM(microvm("db", 1, node, api.RunAlways))

	eventually(t, "db is Pending with the plugin's reason", func() bool {
		s := e.gaon.MicroVM("db").Status
		return len(s.Conditions) == 1 && s.Conditions[0].Reason == microvms.ReasonStoragePluginUnavailable
	})
	assert.Equal(t, api.MicroVMPending, e.gaon.MicroVM("db").Status.Phase)
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine db-e1")
}

// refusingNetwork refuses the MicroVMs named in refuse and fails with a plain
// error the MicroVMs named in flaky, the first time.
type refusingNetwork struct {
	mu     sync.Mutex
	refuse map[string]string
	flaky  map[string]bool
}

func (n *refusingNetwork) Policy(_ context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r, ok := n.refuse[m.Metadata.Name]; ok {
		return nil, &microvms.Refusal{Reason: r, Message: "refused by the test plugin"}
	}
	if n.flaky[m.Metadata.Name] {
		n.flaky[m.Metadata.Name] = false
		return nil, errors.New("the plugin is not ready")
	}
	return &plugin.NetworkPolicy{}, nil
}

func TestNetwork_ARefusalFailsTheMicroVMWithThePluginsReasonAndAnotherErrorIsRetried(t *testing.T) {
	n := &refusingNetwork{refuse: map[string]string{"no": "Quota"}, flaky: map[string]bool{"later": true}}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMicroVM(microvm("no", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("later", 1, node, api.RunAlways))

	c := failedWith(t, e, "no", "Quota")
	assert.Equal(t, "refused by the test plugin", c.Message)
	eventually(t, "a plugin error that is not a refusal is retried until it clears", e.running("later-e1"))
	eventually(t, "the status follows mvmd", e.phase("later", 1, api.MicroVMRunning))
}

// ledgerStorage is a storage plugin that records what it was asked, and
// attaches one disk named for each MicroVM, which it makes on mvmd as a real
// plugin does.
type ledgerStorage struct {
	mvmd     *fakemvmd.Server
	mu       sync.Mutex
	refuse   map[string]bool
	attached []string
	released []string
}

func (s *ledgerStorage) Attach(_ context.Context, m *api.MicroVM) ([]plugin.DiskAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse[m.Metadata.Name] {
		return nil, &microvms.Refusal{Reason: "VolumeNotFound", Message: "no volume for " + m.Metadata.Name}
	}
	s.attached = append(s.attached, m.Metadata.Name)
	s.mvmd.AddDisk("disk-"+m.Metadata.Name, 1<<20)
	return []plugin.DiskAttachment{{DiskID: "disk-" + m.Metadata.Name, ReadOnly: true}}, nil
}

func (s *ledgerStorage) Release(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, id)
	return nil
}

func (s *ledgerStorage) releasedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.released)
}

func TestStorage_CreatesAMicroVMWithTheDisksItAttached(t *testing.T) {
	s := &ledgerStorage{}
	e := start(t, true, nil, func(e *env) { e.storage, s.mvmd = s, e.mvmd })
	e.gaon.SetMicroVM(microvm("db", 1, node, api.RunAlways))

	eventually(t, "db-e1 runs", e.running("db-e1"))
	got, _ := e.mvmd.Machine("db-e1")
	require.Len(t, got.GetDisks(), 1)
	assert.Equal(t, "disk-db", got.GetDisks()[0].GetDiskId())
	assert.True(t, got.GetDisks()[0].GetReadOnly())
}

func TestStorage_ARefusalFailsTheMicroVMBeforeMvmdIsAskedToCreateIt(t *testing.T) {
	s := &ledgerStorage{refuse: map[string]bool{"lost": true}}
	e := start(t, true, nil, func(e *env) { e.storage, s.mvmd = s, e.mvmd })
	e.gaon.SetMicroVM(microvm("lost", 1, node, api.RunAlways))
	e.gaon.SetMicroVM(microvm("found", 1, node, api.RunAlways))

	c := failedWith(t, e, "lost", "VolumeNotFound")
	assert.Equal(t, "no volume for lost", c.Message)
	eventually(t, "control: a MicroVM the plugin serves runs", e.running("found-e1"))
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine lost-e1")
}

func TestStorage_ReleasesWhatItAttachedWhenTheMicroVMIsRemovedAndWhenMvmdRejectsTheCreate(t *testing.T) {
	s := &ledgerStorage{}
	e := start(t, true, nil, func(e *env) {
		e.storage, s.mvmd = s, e.mvmd
		e.mvmd.MaxVCPUs = 2
	})
	over := microvm("over", 1, node, api.RunAlways)
	over.Spec.Resources.Limits.CPU = "4"
	e.gaon.SetMicroVM(over)
	e.gaon.SetMicroVM(microvm("kept", 1, node, api.RunAlways))

	eventually(t, "over is Failed", e.phase("over", 1, api.MicroVMFailed))
	eventually(t, "kept runs", e.running("kept-e1"))
	assert.Contains(t, s.releasedIDs(), "over-e1", "the disks of a create mvmd rejected are given back")
	assert.NotContains(t, s.releasedIDs(), "kept-e1", "control: a running MicroVM keeps its disks")

	e.gaon.SetMicroVM(microvm("kept", 2, "n2", api.RunAlways))
	eventually(t, "kept-e1 is removed", e.gone("kept-e1"))
	eventually(t, "its disks are released with it", func() bool { return slices.Contains(s.releasedIDs(), "kept-e1") })
}
