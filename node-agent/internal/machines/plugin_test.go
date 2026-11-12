package machines_test

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
	"github.com/garamsh/gaon/node-agent/internal/machines"
	"github.com/garamsh/gaon/node-agent/internal/machines/network"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// withNetwork starts the service with the built-in default network plugin
// holding the given prefixes.
func withNetwork(t *testing.T, egress, ingress []string) func(*env) {
	t.Helper()
	n, err := network.NewDefault(egress, ingress)
	require.NoError(t, err)
	return func(e *env) { e.network = n }
}

func failedWith(t *testing.T, e *env, name, reason string) api.Condition {
	t.Helper()
	eventually(t, name+" is Failed", e.phase(name, 1, api.MachineFailed))
	s := e.gaon.Machine(name).Status
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, reason, s.Conditions[0].Reason)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	return s.Conditions[0]
}

func TestNetwork_CreatesAMachineWithThePolicyOfTheFlagsAndItsPorts(t *testing.T) {
	e := start(t, true, nil, withNetwork(t, []string{"10.0.0.0/8", "2001:db8::/32"}, []string{"192.168.0.0/16"}))
	web := machine("web", 1, node, api.RunAlways)
	web.Spec.Ports = []int32{80, 443}
	e.gaon.SetMachine(web)
	e.gaon.SetMachine(machine("quiet", 1, node, api.RunAlways))

	eventually(t, "web-e1 runs", e.running("web-e1"))
	eventually(t, "quiet-e1 runs", e.running("quiet-e1"))

	tcp := func(port uint32) *v1alpha2.NetworkRule {
		return &v1alpha2.NetworkRule{
			Prefix: "192.168.0.0/16", Protocol: v1alpha2.Protocol_PROTOCOL_TCP,
			Ports: []*v1alpha2.PortRange{{First: port, Last: port}},
		}
	}
	want := &v1alpha2.NetworkPolicy{
		Egress: []*v1alpha2.NetworkRule{
			{Prefix: "10.0.0.0/8", Protocol: v1alpha2.Protocol_PROTOCOL_ANY},
			{Prefix: "2001:db8::/32", Protocol: v1alpha2.Protocol_PROTOCOL_ANY},
		},
		Ingress: []*v1alpha2.NetworkRule{tcp(80), tcp(443)},
	}
	got, _ := e.mvmd.Machine("web-e1")
	assert.True(t, proto.Equal(want, got.GetNetworkPolicy()), "got %v", got.GetNetworkPolicy())

	quiet, _ := e.mvmd.Machine("quiet-e1")
	assert.Len(t, quiet.GetNetworkPolicy().GetEgress(), 2, "control: egress does not depend on the ports")
	assert.Empty(t, quiet.GetNetworkPolicy().GetIngress(), "a Machine with no ports accepts nothing")
}

func TestNetwork_WithNoPrefixesThePolicyIsEmpty(t *testing.T) {
	e := start(t, true, nil)
	web := machine("web", 1, node, api.RunAlways)
	web.Spec.Ports = []int32{80}
	e.gaon.SetMachine(web)

	eventually(t, "web-e1 runs", e.running("web-e1"))
	got, _ := e.mvmd.Machine("web-e1")
	assert.Empty(t, got.GetNetworkPolicy().GetEgress())
	assert.Empty(t, got.GetNetworkPolicy().GetIngress(), "a port opens nothing until an ingress prefix is given")
}

func TestNetwork_FailsAMachineNamingANetworkItDoesNotServe(t *testing.T) {
	e := start(t, true, nil)
	other := machine("other", 1, node, api.RunAlways)
	other.Spec.Networks = []string{"default", "tenant-b"}
	e.gaon.SetMachine(other)
	named := machine("named", 1, node, api.RunAlways)
	named.Spec.Networks = []string{"default"}
	e.gaon.SetMachine(named)
	e.gaon.SetMachine(machine("none", 1, node, api.RunAlways))

	c := failedWith(t, e, "other", network.ReasonUnknownNetwork)
	assert.Contains(t, c.Message, "tenant-b")
	eventually(t, "control: a Machine naming default runs", e.running("named-e1"))
	eventually(t, "control: a Machine naming no network runs", e.running("none-e1"))
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine other-e1", "a refused Machine is never created, so it never runs unprotected")
}

func TestNetwork_FailsAMachineWithAPortMvmdWouldRefuse(t *testing.T) {
	e := start(t, true, nil, withNetwork(t, nil, []string{"10.0.0.0/8"}))
	bad := machine("bad", 1, node, api.RunAlways)
	bad.Spec.Ports = []int32{70000}
	e.gaon.SetMachine(bad)
	good := machine("good", 1, node, api.RunAlways)
	good.Spec.Ports = []int32{65535}
	e.gaon.SetMachine(good)

	failedWith(t, e, "bad", machines.ReasonInvalidSpec)
	eventually(t, "control: the highest valid port runs", e.running("good-e1"))
}

// refusingNetwork refuses the Machines named in refuse and fails with a plain
// error the Machines named in flaky, the first time.
type refusingNetwork struct {
	mu     sync.Mutex
	refuse map[string]string
	flaky  map[string]bool
}

func (n *refusingNetwork) Policy(_ context.Context, m *api.Machine) (*v1alpha2.NetworkPolicy, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r, ok := n.refuse[m.Metadata.Name]; ok {
		return nil, &machines.Refusal{Reason: r, Message: "refused by the test plugin"}
	}
	if n.flaky[m.Metadata.Name] {
		n.flaky[m.Metadata.Name] = false
		return nil, errors.New("the plugin is not ready")
	}
	return &v1alpha2.NetworkPolicy{}, nil
}

func TestNetwork_ARefusalFailsTheMachineWithThePluginsReasonAndAnotherErrorIsRetried(t *testing.T) {
	n := &refusingNetwork{refuse: map[string]string{"no": "Quota"}, flaky: map[string]bool{"later": true}}
	e := start(t, true, nil, func(e *env) { e.network = n })
	e.gaon.SetMachine(machine("no", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("later", 1, node, api.RunAlways))

	c := failedWith(t, e, "no", "Quota")
	assert.Equal(t, "refused by the test plugin", c.Message)
	eventually(t, "a plugin error that is not a refusal is retried until it clears", e.running("later-e1"))
	assert.Equal(t, api.MachineRunning, e.gaon.Machine("later").Status.Phase)
}

// ledgerStorage is a storage plugin that records what it was asked, and
// attaches one disk named for each Machine.
type ledgerStorage struct {
	mu       sync.Mutex
	refuse   map[string]bool
	attached []string
	released []string
}

func (s *ledgerStorage) Attach(_ context.Context, m *api.Machine) ([]*v1alpha2.DiskAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse[m.Metadata.Name] {
		return nil, &machines.Refusal{Reason: "VolumeNotFound", Message: "no volume for " + m.Metadata.Name}
	}
	s.attached = append(s.attached, m.Metadata.Name)
	return []*v1alpha2.DiskAttachment{{DiskId: "disk-" + m.Metadata.Name, ReadOnly: true}}, nil
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

func TestStorage_CreatesAMachineWithTheDisksItAttached(t *testing.T) {
	s := &ledgerStorage{}
	e := start(t, true, nil, func(e *env) { e.storage = s })
	e.gaon.SetMachine(machine("db", 1, node, api.RunAlways))

	eventually(t, "db-e1 runs", e.running("db-e1"))
	got, _ := e.mvmd.Machine("db-e1")
	require.Len(t, got.GetDisks(), 1)
	assert.Equal(t, "disk-db", got.GetDisks()[0].GetDiskId())
	assert.True(t, got.GetDisks()[0].GetReadOnly())
}

func TestStorage_ARefusalFailsTheMachineBeforeMvmdIsAskedToCreateIt(t *testing.T) {
	s := &ledgerStorage{refuse: map[string]bool{"lost": true}}
	e := start(t, true, nil, func(e *env) { e.storage = s })
	e.gaon.SetMachine(machine("lost", 1, node, api.RunAlways))
	e.gaon.SetMachine(machine("found", 1, node, api.RunAlways))

	c := failedWith(t, e, "lost", "VolumeNotFound")
	assert.Equal(t, "no volume for lost", c.Message)
	eventually(t, "control: a Machine the plugin serves runs", e.running("found-e1"))
	assert.NotContains(t, e.mvmd.Calls(), "CreateMachine lost-e1")
}

func TestStorage_ReleasesWhatItAttachedWhenTheMachineIsRemovedAndWhenMvmdRejectsTheCreate(t *testing.T) {
	s := &ledgerStorage{}
	e := start(t, true, nil, func(e *env) {
		e.storage = s
		e.mvmd.MaxVCPUs = 2
	})
	over := machine("over", 1, node, api.RunAlways)
	over.Spec.Resources.Limits.CPU = "4"
	e.gaon.SetMachine(over)
	e.gaon.SetMachine(machine("kept", 1, node, api.RunAlways))

	eventually(t, "over is Failed", e.phase("over", 1, api.MachineFailed))
	eventually(t, "kept runs", e.running("kept-e1"))
	assert.Contains(t, s.releasedIDs(), "over-e1", "the disks of a create mvmd rejected are given back")
	assert.NotContains(t, s.releasedIDs(), "kept-e1", "control: a running Machine keeps its disks")

	e.gaon.SetMachine(machine("kept", 2, "n2", api.RunAlways))
	eventually(t, "kept-e1 is removed", e.gone("kept-e1"))
	eventually(t, "its disks are released with it", func() bool { return slices.Contains(s.releasedIDs(), "kept-e1") })
}
