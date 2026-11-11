package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newVolume(name string) *api.Volume {
	return &api.Volume{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: api.ObjectMeta{Name: name},
	}
}

func TestVolumeAttach(t *testing.T) {
	tests := []struct {
		name     string
		attached *api.Attachment
		epoch    api.Epoch
		valid    bool
	}{
		{"unattached", nil, 3, true},
		{"attached at a lower epoch", &api.Attachment{MachineName: "old", BindingEpoch: 2}, 3, true},
		{"attached at the same epoch", &api.Attachment{MachineName: "old", BindingEpoch: 3}, 3, false},
		{"attached at a higher epoch", &api.Attachment{MachineName: "old", BindingEpoch: 4}, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newVolume("data")
			v.Status.Attachment = tt.attached
			err := v.Attach("m1", tt.epoch)
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, &api.Attachment{MachineName: "m1", BindingEpoch: tt.epoch}, v.Status.Attachment)
				return
			}
			assert.ErrorIs(t, err, api.ErrConflict)
			assert.Equal(t, tt.attached, v.Status.Attachment, "a refused attach leaves the attachment as it was")
		})
	}
}

func TestVolumeValidate(t *testing.T) {
	assert.NoError(t, newVolume("data").Validate())

	wrongKind := newVolume("data")
	wrongKind.Kind = api.KindMachine
	assert.ErrorIs(t, wrongKind.Validate(), api.ErrInvalid)

	unnamed := newVolume("")
	assert.ErrorIs(t, unnamed.Validate(), api.ErrInvalid)
}

func TestVolumeValidateStatus(t *testing.T) {
	phases := []api.VolumePhase{api.VolumePending, api.VolumeAvailable, api.VolumeBound, api.VolumeReleased, api.VolumeFailed}
	for _, phase := range phases {
		v := newVolume("data")
		v.Status.Phase = phase
		assert.NoError(t, v.ValidateStatus(), string(phase))
	}
	for _, phase := range []api.VolumePhase{"", "Mounted"} {
		v := newVolume("data")
		v.Status.Phase = phase
		assert.ErrorIs(t, v.ValidateStatus(), api.ErrInvalid, "%q", phase)
	}
}

func TestNodeAndLeaseValidate(t *testing.T) {
	node := &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: "node-a"},
		Status:   api.NodeStatus{State: api.NodeStateDiscovered},
	}
	assert.NoError(t, node.Validate())
	node.Kind = api.KindLease
	assert.ErrorIs(t, node.Validate(), api.ErrInvalid)

	lease := &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: "node-a"},
	}
	assert.NoError(t, lease.Validate())
	lease.Metadata.Name = ""
	assert.ErrorIs(t, lease.Validate(), api.ErrInvalid)
}
