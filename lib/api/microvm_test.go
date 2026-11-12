package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newMicroVM(name string) *api.MicroVM {
	return &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.MicroVMSpec{
			Image:       "alpine",
			RunStrategy: api.RunAlways,
			Resources:   api.ResourceRequirements{Requests: api.ResourceList{CPU: "500m", Memory: "4Gi"}},
		},
	}
}

func TestMicroVMValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *api.MicroVM)
		valid  bool
	}{
		{"complete microvm", func(m *api.MicroVM) {}, true},
		{"wrong group version", func(m *api.MicroVM) { m.APIVersion = "compute.gaon.garam.sh/v1" }, false},
		{"wrong kind", func(m *api.MicroVM) { m.Kind = api.KindVolume }, false},
		{"empty name", func(m *api.MicroVM) { m.Metadata.Name = "" }, false},
		{"run strategy Always", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunAlways }, true},
		{"run strategy RerunOnFailure", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunRerunOnFailure }, true},
		{"run strategy Once", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunOnce }, true},
		{"run strategy OnDemand", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunOnDemand }, true},
		{"run strategy Halted", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunHalted }, true},
		{"run strategy unknown", func(m *api.MicroVM) { m.Spec.RunStrategy = "Sometimes" }, false},
		{"run strategy unset", func(m *api.MicroVM) { m.Spec.RunStrategy = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMicroVM("m1")
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

func TestMicroVMValidateUpdate(t *testing.T) {
	bound := newMicroVM("m1")
	bound.Spec.NodeName = "node-a"
	bound.Spec.BindingEpoch = 3

	tests := []struct {
		name   string
		mutate func(m *api.MicroVM)
		valid  bool
	}{
		{"nothing changed", func(m *api.MicroVM) {}, true},
		{"runStrategy changed", func(m *api.MicroVM) { m.Spec.RunStrategy = api.RunHalted }, true},
		{"idleTimeoutSeconds changed", func(m *api.MicroVM) { m.Spec.IdleTimeoutSeconds = 60 }, true},
		{"resources changed", func(m *api.MicroVM) { m.Spec.Resources.Limits.Memory = "8Gi" }, true},
		{"volumes changed", func(m *api.MicroVM) {
			m.Spec.Volumes = []api.MicroVMVolume{{Name: "data", MountPath: "/data"}}
		}, true},
		{"image changed", func(m *api.MicroVM) { m.Spec.Image = "debian" }, false},
		{"kernel changed", func(m *api.MicroVM) { m.Spec.Kernel = "6.12" }, false},
		{"root disk size changed", func(m *api.MicroVM) { m.Spec.RootDiskSize = "10Gi" }, false},
		{"networks changed", func(m *api.MicroVM) { m.Spec.Networks = []string{"default"} }, false},
		{"ports changed", func(m *api.MicroVM) { m.Spec.Ports = []int32{22} }, false},
		{"nodeSelector changed", func(m *api.MicroVM) { m.Spec.NodeSelector = map[string]string{"zone": "a"} }, false},
		{"requiredCapabilities changed", func(m *api.MicroVM) { m.Spec.RequiredCapabilities = []string{"snapshot"} }, false},
		{"nodeName changed", func(m *api.MicroVM) { m.Spec.NodeName = "node-b" }, false},
		{"bindingEpoch changed", func(m *api.MicroVM) { m.Spec.BindingEpoch = 4 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := newMicroVM("m1")
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

func TestMicroVMBind(t *testing.T) {
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
			m := newMicroVM("m1")
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

func TestMicroVMValidateStatusUpdate(t *testing.T) {
	current := newMicroVM("m1")
	current.Spec.BindingEpoch = 2

	tests := []struct {
		name   string
		phase  api.MicroVMPhase
		epoch  api.Epoch
		wanted bool
	}{
		{"current epoch", api.MicroVMRunning, 2, true},
		{"stale epoch", api.MicroVMRunning, 1, false},
		{"future epoch", api.MicroVMRunning, 3, false},
		{"unknown phase", "Dreaming", 2, false},
		{"unset phase", "", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := newMicroVM("m1")
			next.Status = api.MicroVMStatus{Phase: tt.phase, BindingEpoch: tt.epoch}
			err := next.ValidateStatusUpdate(current)
			if tt.wanted {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestMicroVMValidateStatusUpdateAcceptsEveryPhase(t *testing.T) {
	phases := []api.MicroVMPhase{
		api.MicroVMPending, api.MicroVMRunning, api.MicroVMSuspended,
		api.MicroVMSucceeded, api.MicroVMFailed, api.MicroVMStopped,
	}
	for _, phase := range phases {
		m := newMicroVM("m1")
		m.Status.Phase = phase
		assert.NoError(t, m.ValidateStatusUpdate(m), string(phase))
	}
}

func TestMicroVMEffectivePhase(t *testing.T) {
	m := newMicroVM("m1")
	m.Spec.BindingEpoch = 2
	m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, BindingEpoch: 2}
	assert.Equal(t, api.MicroVMRunning, m.EffectivePhase())

	m.Spec.BindingEpoch = 3
	assert.Equal(t, api.MicroVMPending, m.EffectivePhase(), "a status from an older epoch is void")
}
