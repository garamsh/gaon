package apiserver_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutVolume_CreateStartsPendingWhateverStatusItCarries(t *testing.T) {
	e := newEnv(t)
	v := volume("v1")
	v.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &api.Attachment{MachineName: "m1", BindingEpoch: 1}, Capacity: "1Gi"}
	v.Metadata.Generation = 7
	then := time.Now().UTC()
	v.Metadata.DeletionTimestamp = &then

	created, err := e.api.PutVolume(ctx(t), v)
	require.NoError(t, err)
	assert.Equal(t, api.VolumePending, created.Status.Phase)
	assert.Nil(t, created.Status.Attachment)
	assert.Empty(t, created.Status.Capacity)
	assert.Equal(t, int64(1), created.Metadata.Generation)
	assert.Nil(t, created.Metadata.DeletionTimestamp, "a Volume is not created already deleting")
	assert.NotEmpty(t, created.Metadata.ResourceVersion)

	got, err := e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err)
	assert.Equal(t, created, got, "the Volume read is the Volume stored")
}

func TestPutVolume_CreateOfAnExistingNameIsAlreadyExistsAndTheNameIsThePaths(t *testing.T) {
	e := newEnv(t)
	e.createVolume(t, "v1")

	_, err := e.api.PutVolume(ctx(t), volume("v1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	code, _ := e.send(t, http.MethodPut, "/volumes/other", mustJSON(t, volume("v2")))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a name that is not the path's")
	code, _ = e.send(t, http.MethodPut, "/volumes/v2", mustJSON(t, volume("v2")))
	assert.Equal(t, http.StatusOK, code, "control: the name of the path")
}

func TestPutVolume_CreateWithANodeNameOrAnInvalidSpecIsInvalid(t *testing.T) {
	e := newEnv(t)

	pinned := volume("v1")
	pinned.Spec.NodeName = "n1"
	_, err := e.api.PutVolume(ctx(t), pinned)
	assert.ErrorIs(t, err, api.ErrInvalid, "only a binding sets spec.nodeName")
	_, err = e.api.GetVolume(ctx(t), "v1")
	assert.ErrorIs(t, err, api.ErrNotFound, "nothing was stored")

	empty := volume("v1")
	empty.Spec.StorageClass = ""
	_, err = e.api.PutVolume(ctx(t), empty)
	assert.ErrorIs(t, err, api.ErrInvalid, "a spec the contract refuses")

	_, err = e.api.PutVolume(ctx(t), volume("v1"))
	assert.NoError(t, err, "control: the same name with a valid spec and no node")
}

func TestPutVolume_SpecIsImmutableButFinalizersAreNot(t *testing.T) {
	e := newEnv(t)
	created := e.createVolume(t, "v1")

	resized := *created
	resized.Spec.Storage = "2Gi"
	_, err := e.api.PutVolume(ctx(t), &resized)
	assert.ErrorIs(t, err, api.ErrInvalid, "a spec change")
	pinned := *created
	pinned.Spec.NodeName = "n1"
	_, err = e.api.PutVolume(ctx(t), &pinned)
	assert.ErrorIs(t, err, api.ErrInvalid, "a node name, which only a binding sets")

	held := *created
	held.Metadata.Finalizers = []string{holdFinalizer}
	updated, err := e.api.PutVolume(ctx(t), &held)
	require.NoError(t, err, "control: only a finalizer entry changes")
	assert.Equal(t, []string{holdFinalizer}, updated.Metadata.Finalizers)
	assert.Equal(t, int64(1), updated.Metadata.Generation, "no spec change, no new generation")
	assert.NotEqual(t, created.Metadata.ResourceVersion, updated.Metadata.ResourceVersion)
}

func TestPutVolume_UpdateIsCompareAndSwapAndTakesNeitherStatusNorDeletionTime(t *testing.T) {
	e := newEnv(t)
	created := e.createVolume(t, "v1")

	forged := *created
	forged.Status.Phase = api.VolumeBound
	now := time.Now().UTC()
	forged.Metadata.DeletionTimestamp = &now
	forged.Metadata.Finalizers = []string{holdFinalizer}
	forged.Metadata.Generation = 9
	updated, err := e.api.PutVolume(ctx(t), &forged)
	require.NoError(t, err)
	assert.Equal(t, api.VolumePending, updated.Status.Phase, "a status the body carries is not stored")
	assert.Nil(t, updated.Metadata.DeletionTimestamp, "a deletion time the body carries is not stored")
	assert.Equal(t, int64(1), updated.Metadata.Generation, "a generation the body carries is not stored")

	_, err = e.api.PutVolume(ctx(t), &forged)
	assert.ErrorIs(t, err, api.ErrConflict, "the version the first update replaced")

	absent := volume("absent")
	absent.Metadata.ResourceVersion = "5"
	_, err = e.api.PutVolume(ctx(t), absent)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestGetVolume_ListAndGetReadWhatIsStored(t *testing.T) {
	e := newEnv(t)
	_, err := e.api.GetVolume(ctx(t), "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)
	empty, err := e.api.ListVolumes(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, empty.Items)
	assert.NotEmpty(t, empty.Metadata.ResourceVersion)

	e.createVolume(t, "a")
	e.createVolume(t, "b")
	list, err := e.api.ListVolumes(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	assert.Equal(t, "a", list.Items[0].Metadata.Name)
	assert.Equal(t, "b", list.Items[1].Metadata.Name)
}

func TestDeleteVolume_WithAFinalizerSetsTheDeletionTimeAndWithoutRemovesAtOnce(t *testing.T) {
	e := newEnv(t)
	plain := e.createVolume(t, "plain")
	held := volume("held")
	held.Metadata.Finalizers = []string{holdFinalizer}
	heldCreated, err := e.api.PutVolume(ctx(t), held)
	require.NoError(t, err)

	deleting, err := e.api.DeleteVolume(ctx(t), "held", heldCreated.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.NotNil(t, deleting.Metadata.DeletionTimestamp)
	got, err := e.api.GetVolume(ctx(t), "held")
	require.NoError(t, err, "a Volume with a finalizer stays")
	assert.NotNil(t, got.Metadata.DeletionTimestamp)

	_, err = e.api.DeleteVolume(ctx(t), "plain", plain.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetVolume(ctx(t), "plain")
	assert.ErrorIs(t, err, api.ErrNotFound, "a Volume with no finalizer is removed")
}

func TestDeleteVolume_IsCompareAndSwapAndAnAbsentVolumeIsNotFound(t *testing.T) {
	e := newEnv(t)
	created := e.createVolume(t, "v1")

	_, err := e.api.DeleteVolume(ctx(t), "v1", "1")
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = e.api.DeleteVolume(ctx(t), "absent", "1")
	assert.ErrorIs(t, err, api.ErrNotFound)
	_, err = e.api.DeleteVolume(ctx(t), "v1", created.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: the version it is at")
}

func TestPutVolume_ClearingTheLastFinalizerOfAVolumeBeingDeletedRemovesIt(t *testing.T) {
	e := newEnv(t)
	list, err := e.api.ListVolumes(ctx(t))
	require.NoError(t, err)
	w, err := e.api.WatchVolumes(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	held := volume("v1")
	held.Metadata.Finalizers = []string{holdFinalizer, "example.com/other"}
	created, err := e.api.PutVolume(ctx(t), held)
	require.NoError(t, err)
	deleting, err := e.api.DeleteVolume(ctx(t), "v1", created.Metadata.ResourceVersion)
	require.NoError(t, err)

	added := *deleting
	added.Metadata.Finalizers = []string{holdFinalizer, "example.com/other", "example.com/new"}
	_, err = e.api.PutVolume(ctx(t), &added)
	assert.ErrorIs(t, err, api.ErrInvalid, "a finalizer cannot be added to a Volume being deleted")

	one := *deleting
	one.Metadata.Finalizers = []string{"example.com/other"}
	remaining, err := e.api.PutVolume(ctx(t), &one)
	require.NoError(t, err, "control: one finalizer is cleared")
	_, err = e.api.GetVolume(ctx(t), "v1")
	require.NoError(t, err, "the Volume stays while a finalizer is left")

	none := *remaining
	none.Metadata.Finalizers = nil
	_, err = e.api.PutVolume(ctx(t), &none)
	require.NoError(t, err)
	_, err = e.api.GetVolume(ctx(t), "v1")
	assert.ErrorIs(t, err, api.ErrNotFound, "the last finalizer was cleared")

	var types []api.EventType
	for i := 0; i < 4; i++ {
		types = append(types, next(t, w).Type)
	}
	assert.Equal(t, []api.EventType{api.EventAdded, api.EventModified, api.EventModified, api.EventDeleted}, types)
}

func TestWatchVolumes_ResumesFromAVersionAndAnExpiredOneEndsWithAnExpiredError(t *testing.T) {
	e := newEnv(t)
	v := e.createVolume(t, "v1")
	from := v.Metadata.ResourceVersion
	held := *v
	held.Metadata.Finalizers = []string{holdFinalizer}
	updated, err := e.api.PutVolume(ctx(t), &held)
	require.NoError(t, err)

	w, err := e.api.WatchVolumes(ctx(t), from)
	require.NoError(t, err)
	defer w.Close()
	ev := next(t, w)
	assert.Equal(t, api.EventModified, ev.Type, "the create at the given version is not replayed")
	assert.Equal(t, updated.Metadata.ResourceVersion, ev.Object.Metadata.ResourceVersion)

	for i := 0; i < 3; i++ {
		update := *updated
		update.Metadata.Finalizers = []string{holdFinalizer, "example.com/" + strconv.Itoa(i)}
		updated, err = e.api.PutVolume(ctx(t), &update)
		require.NoError(t, err)
	}
	head, err := strconv.ParseInt(string(updated.Metadata.ResourceVersion), 10, 64)
	require.NoError(t, err)
	_, err = e.etcd.Compact(ctx(t), head)
	require.NoError(t, err)

	expired, err := e.api.WatchVolumes(ctx(t), from)
	require.NoError(t, err, "the response is a stream, not an error status")
	defer expired.Close()
	_, err = expired.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
}
