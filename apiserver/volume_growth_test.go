package apiserver_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// withCapacity sets the capacity of the Volume's disk, as its provisioner does.
func (e *env) withCapacity(t *testing.T, v *api.Volume, capacity string) *api.Volume {
	t.Helper()
	next := *v
	next.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: api.Quantity(capacity)}
	out, err := e.api.PutVolumeStatus(ctx(t), &next)
	require.NoError(t, err)
	require.Equal(t, api.Quantity(capacity), out.Status.Capacity, "the setup wrote the capacity it was asked to")
	return out
}

func withStorage(v *api.Volume, storage string) *api.Volume {
	next := *v
	next.Spec.Storage = api.Quantity(storage)
	return &next
}

func TestPutVolume_StorageGrowsUnderAClassThatAllowsItAndTheGenerationAdvances(t *testing.T) {
	e := newEnv(t)
	created := e.withCapacity(t, e.createVolume(t, "v1"), "1Gi")

	grown, err := e.api.PutVolume(ctx(t), withStorage(created, "2Gi"))
	require.NoError(t, err, "the class local allows expansion")
	assert.Equal(t, api.Quantity("2Gi"), grown.Spec.Storage)
	assert.Equal(t, int64(2), grown.Metadata.Generation, "a change of spec is a new generation")
	assert.Equal(t, created.Status, grown.Status, "the status is the provisioner's, not the write's")

	got, err := e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	assert.Equal(t, grown, got, "the growth is stored")

	same, err := e.api.PutVolume(ctx(t), withStorage(grown, "2048Mi"))
	require.NoError(t, err, "the same size in another unit")
	assert.Equal(t, int64(2), same.Metadata.Generation, "the same size is no new generation")
}

func TestPutVolume_AGrowthWithNoClassOrAClassThatForbidsItIsInvalid(t *testing.T) {
	e := newEnv(t)
	ceph := volume("v1")
	ceph.Spec.StorageClass = "ceph"
	ceph.Spec.AccessModes = nil
	created, err := e.api.PutVolume(ctx(t), ceph)
	require.NoError(t, err)

	_, err = e.api.PutVolume(ctx(t), withStorage(created, "2Gi"))
	assert.ErrorIs(t, err, api.ErrInvalid, "no StorageClass named ceph")

	code, body := e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", false, ""))
	require.Equal(t, http.StatusOK, code, body)
	_, err = e.api.PutVolume(ctx(t), withStorage(created, "2Gi"))
	assert.ErrorIs(t, err, api.ErrInvalid, "a class that forbids expansion")

	var class api.StorageClass
	require.NoError(t, json.Unmarshal([]byte(body), &class))
	code, body = e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", true, string(class.Metadata.ResourceVersion)))
	require.Equal(t, http.StatusOK, code, body)
	grown, err := e.api.PutVolume(ctx(t), withStorage(created, "2Gi"))
	require.NoError(t, err, "control: the same write once the class allows expansion")
	assert.Equal(t, api.Quantity("2Gi"), grown.Spec.Storage)

	same, err := e.api.PutVolume(ctx(t), withStorage(grown, "2048Mi"))
	require.NoError(t, err, "control: a size in another unit is no growth")
	assert.Equal(t, grown.Metadata.Generation, same.Metadata.Generation)
}

func TestPutVolume_AnyOtherChangeOfSpecIsStillInvalid(t *testing.T) {
	e := newEnv(t)
	created := e.createVolume(t, "v1")

	moved := *created
	moved.Spec.NodeName = "n1"
	_, err := e.api.PutVolume(ctx(t), &moved)
	assert.ErrorIs(t, err, api.ErrInvalid, "a node name")
	reclassed := *created
	reclassed.Spec.StorageClass = "ceph"
	_, err = e.api.PutVolume(ctx(t), &reclassed)
	assert.ErrorIs(t, err, api.ErrInvalid, "a class")

	_, err = e.api.PutVolume(ctx(t), withStorage(created, "2Gi"))
	assert.NoError(t, err, "control: storage alone")
}

func TestPutVolume_StorageBelowTheCapacityIsInvalidAndAboveItMayBeLowered(t *testing.T) {
	e := newEnv(t)
	v := e.withCapacity(t, e.createVolume(t, "v1"), "1Gi")
	v, err := e.api.PutVolume(ctx(t), withStorage(v, "4Gi"))
	require.NoError(t, err)

	_, err = e.api.PutVolume(ctx(t), withStorage(v, "512Mi"))
	assert.ErrorIs(t, err, api.ErrInvalid, "below status.capacity: a volume does not shrink")
	got, err := e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	assert.Equal(t, api.Quantity("4Gi"), got.Spec.Storage, "the refused write changed nothing")

	lowered, err := e.api.PutVolume(ctx(t), withStorage(got, "2Gi"))
	require.NoError(t, err, "control: a request above the disk, lowered")
	lowered, err = e.api.PutVolume(ctx(t), withStorage(lowered, "1Gi"))
	require.NoError(t, err, "control: a request lowered to the capacity")
	assert.Equal(t, api.Quantity("1Gi"), lowered.Spec.Storage)
}

func TestPutVolume_StorageOfAVolumeBeingDeletedIsAConflict(t *testing.T) {
	e := newEnv(t)
	held := volume("v1")
	held.Metadata.Finalizers = []string{holdFinalizer, "example.com/other"}
	created, err := e.api.PutVolume(ctx(t), held)
	require.NoError(t, err)
	_, err = e.api.DeleteVolume(ctx(t), "v1", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	deleting, err := e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	require.NotNil(t, deleting.Metadata.DeletionTimestamp, "the setup left a Volume being deleted")

	_, err = e.api.PutVolume(ctx(t), withStorage(deleting, "2Gi"))
	assert.ErrorIs(t, err, api.ErrConflict)

	same := *deleting
	same.Metadata.Finalizers = []string{holdFinalizer}
	_, err = e.api.PutVolume(ctx(t), &same)
	assert.NoError(t, err, "control: a change that leaves storage alone is not refused")
}

func TestPutVolumeStatus_TakesTheConditionsValidateStatusAccepts(t *testing.T) {
	e := newEnv(t)
	v := e.createVolume(t, "v1")
	now := time.Now().UTC().Truncate(time.Second)

	resizing := *v
	resizing.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "1Gi", Conditions: []api.Condition{
		{Type: api.VolumeConditionResizing, Status: api.ConditionTrue, Reason: "InProgress", Message: "growing to 2Gi", LastTransitionTime: &now},
	}}
	out, err := e.api.PutVolumeStatus(ctx(t), &resizing)
	require.NoError(t, err)
	assert.Equal(t, resizing.Status.Conditions, out.Status.Conditions)
	got, err := e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	assert.Equal(t, resizing.Status.Conditions, got.Status.Conditions, "the condition is stored")

	bad := *got
	bad.Status.Conditions = []api.Condition{{Type: api.VolumeConditionResizing, Status: "Maybe"}}
	_, err = e.api.PutVolumeStatus(ctx(t), &bad)
	assert.ErrorIs(t, err, api.ErrInvalid, "a condition whose status is none of True, False and Unknown")

	done := *got
	done.Status.Conditions = nil
	out, err = e.api.PutVolumeStatus(ctx(t), &done)
	require.NoError(t, err, "control: the condition is removed when the growth is done")
	assert.Empty(t, out.Status.Conditions)
}
