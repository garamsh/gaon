package apiserver_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// seedVolume stores a Volume the way the controller that creates Volumes
// would. The contract has no create path for one, so the test writes etcd.
func (e *env) seedVolume(t *testing.T, name string) *api.Volume {
	t.Helper()
	v := &api.Volume{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
		Metadata: api.ObjectMeta{Name: name},
		Status:   api.VolumeStatus{Phase: api.VolumeAvailable},
	}
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	resp, err := e.etcd.Put(ctx(t), "/registry/volumes/"+name, string(raw))
	require.NoError(t, err)
	v.Metadata.ResourceVersion = api.ResourceVersion(strconv.FormatInt(resp.Header.Revision, 10))
	return v
}

// boundMachine creates a Machine bound to a node at the given epoch.
func (e *env) boundMachine(t *testing.T, name string, epoch api.Epoch) *api.Machine {
	t.Helper()
	m := e.createMachine(t, machine(name))
	for i := api.Epoch(1); i <= epoch; i++ {
		m = e.bind(t, m, "n1", i)
	}
	return m
}

func attached(v *api.Volume, machine string, epoch api.Epoch) *api.Volume {
	next := *v
	next.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &api.Attachment{MachineName: machine, BindingEpoch: epoch}}
	return &next
}

func TestPutVolumeStatus_AbsentVolumeIsNotFound(t *testing.T) {
	e := newEnv(t)
	v := &api.Volume{TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume}, Metadata: api.ObjectMeta{Name: "v1", ResourceVersion: "5"}, Status: api.VolumeStatus{Phase: api.VolumePending}}
	_, err := e.api.PutVolumeStatus(ctx(t), v)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestPutVolumeStatus_IsCompareAndSwapAndValidated(t *testing.T) {
	e := newEnv(t)
	v := e.seedVolume(t, "v1")

	bogus := *v
	bogus.Status.Phase = "Soggy"
	_, err := e.api.PutVolumeStatus(ctx(t), &bogus)
	assert.ErrorIs(t, err, api.ErrInvalid)

	stale := *v
	stale.Metadata.ResourceVersion = "1"
	stale.Status.Phase = api.VolumeBound
	_, err = e.api.PutVolumeStatus(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict)

	ok := *v
	ok.Status.Phase = api.VolumeReleased
	got, err := e.api.PutVolumeStatus(ctx(t), &ok)
	require.NoError(t, err)
	assert.Equal(t, api.VolumeReleased, got.Status.Phase)
}

func TestPutVolumeStatus_AttachesAtTheMachinesEpoch(t *testing.T) {
	e := newEnv(t)
	e.boundMachine(t, "m1", 1)
	v := e.seedVolume(t, "v1")

	got, err := e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 1))
	require.NoError(t, err)
	require.NotNil(t, got.Status.Attachment)
	assert.Equal(t, api.Epoch(1), got.Status.Attachment.BindingEpoch)
}

func TestPutVolumeStatus_AttachmentNamesCurrentEpochOfAnExistingMachine(t *testing.T) {
	e := newEnv(t)
	e.boundMachine(t, "m1", 2)
	v := e.seedVolume(t, "v1")

	_, err := e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 1))
	assert.ErrorIs(t, err, api.ErrInvalid, "an epoch that is not the machine's current one")
	_, err = e.api.PutVolumeStatus(ctx(t), attached(v, "absent", 1))
	assert.ErrorIs(t, err, api.ErrInvalid, "no such machine")
	_, err = e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 2))
	assert.NoError(t, err)
}

func TestPutVolumeStatus_AttachedAtAnEqualOrHigherEpochIsAConflict(t *testing.T) {
	e := newEnv(t)
	e.boundMachine(t, "m1", 1)
	e.boundMachine(t, "m2", 1)
	v := e.seedVolume(t, "v1")
	held, err := e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 1))
	require.NoError(t, err)

	_, err = e.api.PutVolumeStatus(ctx(t), attached(held, "m2", 1))
	assert.ErrorIs(t, err, api.ErrConflict, "an equal epoch does not take the volume")

	e.bind(t, mustGet(t, e, "m1"), "n2", 2)
	moved, err := e.api.PutVolumeStatus(ctx(t), attached(held, "m1", 2))
	require.NoError(t, err, "a higher epoch takes it")
	assert.Equal(t, api.Epoch(2), moved.Status.Attachment.BindingEpoch)

	e.boundMachine(t, "m3", 1)
	_, err = e.api.PutVolumeStatus(ctx(t), attached(moved, "m3", 1))
	assert.ErrorIs(t, err, api.ErrConflict, "a lower epoch does not take it")
}

func mustGet(t *testing.T, e *env, name string) *api.Machine {
	t.Helper()
	m, err := e.api.GetMachine(ctx(t), name)
	require.NoError(t, err)
	return m
}

func TestPutVolumeStatus_UnchangedAttachmentIsNotReattached(t *testing.T) {
	e := newEnv(t)
	e.boundMachine(t, "m1", 1)
	v := e.seedVolume(t, "v1")
	held, err := e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 1))
	require.NoError(t, err)

	next := *held
	next.Status.Phase = api.VolumeReleased
	_, err = e.api.PutVolumeStatus(ctx(t), &next)
	assert.NoError(t, err, "a status write that keeps the attachment is not an attach")
}
