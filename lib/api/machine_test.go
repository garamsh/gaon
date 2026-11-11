package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newMachine(name string) *api.Machine {
	return &api.Machine{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.MachineSpec{
			Image:       "alpine",
			RunStrategy: api.RunAlways,
			Resources:   api.ResourceRequirements{Requests: api.ResourceList{CPU: "500m", Memory: "4Gi"}},
		},
	}
}

func TestMachineValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *api.Machine)
		valid  bool
	}{
		{"complete machine", func(m *api.Machine) {}, true},
		{"wrong group version", func(m *api.Machine) { m.APIVersion = "compute.gaon.garam.sh/v1" }, false},
		{"wrong kind", func(m *api.Machine) { m.Kind = api.KindVolume }, false},
		{"empty name", func(m *api.Machine) { m.Metadata.Name = "" }, false},
		{"run strategy Always", func(m *api.Machine) { m.Spec.RunStrategy = api.RunAlways }, true},
		{"run strategy RerunOnFailure", func(m *api.Machine) { m.Spec.RunStrategy = api.RunRerunOnFailure }, true},
		{"run strategy Once", func(m *api.Machine) { m.Spec.RunStrategy = api.RunOnce }, true},
		{"run strategy OnDemand", func(m *api.Machine) { m.Spec.RunStrategy = api.RunOnDemand }, true},
		{"run strategy Halted", func(m *api.Machine) { m.Spec.RunStrategy = api.RunHalted }, true},
		{"run strategy unknown", func(m *api.Machine) { m.Spec.RunStrategy = "Sometimes" }, false},
		{"run strategy unset", func(m *api.Machine) { m.Spec.RunStrategy = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine("m1")
			tt.mutate(m)
			err := m.Validate()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestMachineValidateUpdate(t *testing.T) {
	bound := newMachine("m1")
	bound.Spec.NodeName = "node-a"
	bound.Spec.BindingEpoch = 3

	tests := []struct {
		name   string
		mutate func(m *api.Machine)
		valid  bool
	}{
		{"nothing changed", func(m *api.Machine) {}, true},
		{"runStrategy changed", func(m *api.Machine) { m.Spec.RunStrategy = api.RunHalted }, true},
		{"idleTimeoutSeconds changed", func(m *api.Machine) { m.Spec.IdleTimeoutSeconds = 60 }, true},
		{"resources changed", func(m *api.Machine) { m.Spec.Resources.Limits.Memory = "8Gi" }, true},
		{"volumes changed", func(m *api.Machine) {
			m.Spec.Volumes = []api.MachineVolume{{Name: "data", MountPath: "/data"}}
		}, true},
		{"image changed", func(m *api.Machine) { m.Spec.Image = "debian" }, false},
		{"kernel changed", func(m *api.Machine) { m.Spec.Kernel = "6.12" }, false},
		{"root disk size changed", func(m *api.Machine) { m.Spec.RootDiskSize = "10Gi" }, false},
		{"networks changed", func(m *api.Machine) { m.Spec.Networks = []string{"default"} }, false},
		{"ports changed", func(m *api.Machine) { m.Spec.Ports = []int32{22} }, false},
		{"nodeSelector changed", func(m *api.Machine) { m.Spec.NodeSelector = map[string]string{"zone": "a"} }, false},
		{"requiredCapabilities changed", func(m *api.Machine) { m.Spec.RequiredCapabilities = []string{"snapshot"} }, false},
		{"nodeName changed", func(m *api.Machine) { m.Spec.NodeName = "node-b" }, false},
		{"bindingEpoch changed", func(m *api.Machine) { m.Spec.BindingEpoch = 4 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := newMachine("m1")
			next.Spec.NodeName, next.Spec.BindingEpoch = bound.Spec.NodeName, bound.Spec.BindingEpoch
			tt.mutate(next)
			err := next.ValidateUpdate(bound)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestMachineBind(t *testing.T) {
	tests := []struct {
		name    string
		epoch   api.Epoch
		binding api.Binding
		valid   bool
	}{
		{"next epoch", 2, api.Binding{NodeName: "node-a", BindingEpoch: 3}, true},
		{"same epoch", 2, api.Binding{NodeName: "node-a", BindingEpoch: 2}, false},
		{"skipped epoch", 2, api.Binding{NodeName: "node-a", BindingEpoch: 4}, false},
		{"lower epoch", 2, api.Binding{NodeName: "node-a", BindingEpoch: 1}, false},
		{"no node", 2, api.Binding{BindingEpoch: 3}, false},
		{"first binding", 0, api.Binding{NodeName: "node-a", BindingEpoch: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine("m1")
			m.Spec.NodeName, m.Spec.BindingEpoch = "old-node", tt.epoch
			err := m.Bind(tt.binding)
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, tt.binding.NodeName, m.Spec.NodeName)
				assert.Equal(t, tt.binding.BindingEpoch, m.Spec.BindingEpoch)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
			assert.Equal(t, "old-node", m.Spec.NodeName)
			assert.Equal(t, tt.epoch, m.Spec.BindingEpoch)
		})
	}
}

func TestMachineValidateStatusUpdate(t *testing.T) {
	current := newMachine("m1")
	current.Spec.BindingEpoch = 2

	tests := []struct {
		name   string
		phase  api.MachinePhase
		epoch  api.Epoch
		wanted bool
	}{
		{"current epoch", api.MachineRunning, 2, true},
		{"stale epoch", api.MachineRunning, 1, false},
		{"future epoch", api.MachineRunning, 3, false},
		{"unknown phase", "Dreaming", 2, false},
		{"unset phase", "", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := newMachine("m1")
			next.Status = api.MachineStatus{Phase: tt.phase, BindingEpoch: tt.epoch}
			err := next.ValidateStatusUpdate(current)
			if tt.wanted {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestMachineValidateStatusUpdateAcceptsEveryPhase(t *testing.T) {
	phases := []api.MachinePhase{
		api.MachinePending, api.MachineRunning, api.MachineSuspended,
		api.MachineSucceeded, api.MachineFailed, api.MachineStopped,
	}
	for _, phase := range phases {
		m := newMachine("m1")
		m.Status.Phase = phase
		assert.NoError(t, m.ValidateStatusUpdate(m), string(phase))
	}
}

func TestMachineEffectivePhase(t *testing.T) {
	m := newMachine("m1")
	m.Spec.BindingEpoch = 2
	m.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 2}
	assert.Equal(t, api.MachineRunning, m.EffectivePhase())

	m.Spec.BindingEpoch = 3
	assert.Equal(t, api.MachinePending, m.EffectivePhase(), "a status from an older epoch is void")
}
