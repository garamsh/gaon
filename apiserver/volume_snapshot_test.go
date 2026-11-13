package apiserver_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func volumeSnapshot(name, volumeName string) *api.VolumeSnapshot {
	return &api.VolumeSnapshot{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolumeSnapshot},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.VolumeSnapshotSpec{VolumeName: volumeName},
	}
}

// readyStatus is the status of a snapshot of a local Volume held by nodeName.
func readyStatus(nodeName string) api.VolumeSnapshotStatus {
	return api.VolumeSnapshotStatus{ReadyToUse: true, RestoreSize: "1Gi", StorageClass: api.StorageClassLocal, NodeName: nodeName}
}

// createVolumeSnapshot creates a snapshot of the Volume v and fails the test
// if that does not work.
func (e *env) createVolumeSnapshot(t *testing.T, name, v string) *api.VolumeSnapshot {
	t.Helper()
	created, err := e.api.PutVolumeSnapshot(ctx(t), volumeSnapshot(name, v))
	require.NoError(t, err)
	return created
}

func TestPutVolumeSnapshot_CreateStartsNotReadyWhateverStatusItCarries(t *testing.T) {
	e := newEnv(t)
	s := volumeSnapshot("s1", "v1")
	s.Status = readyStatus("node-1")
	s.Metadata.Generation = 7
	then := time.Now().UTC()
	s.Metadata.DeletionTimestamp = &then

	created, err := e.api.PutVolumeSnapshot(ctx(t), s)
	require.NoError(t, err)
	assert.Equal(t, api.VolumeSnapshotStatus{}, created.Status)
	assert.Equal(t, int64(1), created.Metadata.Generation)
	assert.Nil(t, created.Metadata.DeletionTimestamp, "a snapshot is not created already deleting")
	assert.NotEmpty(t, created.Metadata.ResourceVersion)

	got, err := e.api.GetVolumeSnapshot(ctx(t), "s1")
	require.NoError(t, err)
	assert.Equal(t, created, got, "the snapshot read is the snapshot stored")
}

func TestPutVolumeSnapshot_CreateRefusesWhatIsInvalidAndAnExistingNameAndAMismatchedPath(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutVolumeSnapshot(ctx(t), volumeSnapshot("s1", ""))
	assert.ErrorIs(t, err, api.ErrInvalid, "no source Volume")
	wrongKind := volumeSnapshot("s1", "v1")
	wrongKind.Kind = api.KindVolume
	_, err = e.api.PutVolumeSnapshot(ctx(t), wrongKind)
	assert.ErrorIs(t, err, api.ErrInvalid, "another kind")
	_, err = e.api.GetVolumeSnapshot(ctx(t), "s1")
	assert.ErrorIs(t, err, api.ErrNotFound, "a refused create stores nothing")

	e.createVolumeSnapshot(t, "s1", "v1")
	_, err = e.api.PutVolumeSnapshot(ctx(t), volumeSnapshot("s1", "v1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "the same name again")

	code, _ := e.send(t, http.MethodPut, "/volumesnapshots/other", mustJSON(t, volumeSnapshot("s2", "v1")))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a name that is not the path's")
	code, _ = e.send(t, http.MethodPut, "/volumesnapshots/s2", mustJSON(t, volumeSnapshot("s2", "v1")))
	assert.Equal(t, http.StatusOK, code, "control: the name of the path")
}

func TestPutVolumeSnapshot_UpdateKeepsTheSpecAndTheStatusAndTheDeletionTime(t *testing.T) {
	e := newEnv(t)
	s := e.createVolumeSnapshot(t, "s1", "v1")

	retarget := *s
	retarget.Spec.VolumeName = "v2"
	_, err := e.api.PutVolumeSnapshot(ctx(t), &retarget)
	assert.ErrorIs(t, err, api.ErrInvalid, "the spec is immutable")

	stale := *s
	stale.Metadata.ResourceVersion = "1"
	_, err = e.api.PutVolumeSnapshot(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")

	then := time.Now().UTC()
	held := *s
	held.Metadata.Finalizers = []string{holdFinalizer}
	held.Metadata.DeletionTimestamp = &then
	held.Status = readyStatus("node-1")
	updated, err := e.api.PutVolumeSnapshot(ctx(t), &held)
	require.NoError(t, err, "control: a finalizer is added")
	assert.Equal(t, []string{holdFinalizer}, updated.Metadata.Finalizers)
	assert.Nil(t, updated.Metadata.DeletionTimestamp, "the deletion time is the server's")
	assert.Equal(t, api.VolumeSnapshotStatus{}, updated.Status, "the status is written at /status")
	assert.Equal(t, s.Metadata.UID, updated.Metadata.UID)
}

func TestPutVolumeSnapshotStatus_ChecksTheStatusAndWhatAReadySnapshotKeeps(t *testing.T) {
	e := newEnv(t)
	s := e.createVolumeSnapshot(t, "s1", "v1")

	noSize := *s
	noSize.Status = api.VolumeSnapshotStatus{ReadyToUse: true, StorageClass: api.StorageClassLocal, NodeName: "node-1"}
	_, err := e.api.PutVolumeSnapshotStatus(ctx(t), &noSize)
	assert.ErrorIs(t, err, api.ErrInvalid, "ready without a size")
	badCondition := *s
	badCondition.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "Maybe"}}
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &badCondition)
	assert.ErrorIs(t, err, api.ErrInvalid, "a condition with no known status")
	stale := *s
	stale.Metadata.ResourceVersion = "1"
	stale.Status = readyStatus("node-1")
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")

	good := *s
	good.Status = readyStatus("node-1")
	good.Spec.VolumeName = "other"
	good.Metadata.Finalizers = []string{holdFinalizer}
	readied, err := e.api.PutVolumeSnapshotStatus(ctx(t), &good)
	require.NoError(t, err, "control: a ready status")
	assert.Equal(t, readyStatus("node-1"), readied.Status)
	assert.Equal(t, "v1", readied.Spec.VolumeName, "a status write changes only the status")
	assert.Empty(t, readied.Metadata.Finalizers)

	undo := *readied
	undo.Status = api.VolumeSnapshotStatus{}
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &undo)
	assert.ErrorIs(t, err, api.ErrInvalid, "readyToUse is not undone")
	moved := *readied
	moved.Status.NodeName = "node-2"
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &moved)
	assert.ErrorIs(t, err, api.ErrInvalid, "nodeName does not change")
	resized := *readied
	resized.Status.RestoreSize = "2Gi"
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &resized)
	assert.ErrorIs(t, err, api.ErrInvalid, "restoreSize does not change")
	reclassed := *readied
	reclassed.Status.StorageClass = "ceph"
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &reclassed)
	assert.ErrorIs(t, err, api.ErrInvalid, "storageClass does not change")
	same := *readied
	same.Status.RestoreSize = "1024Mi"
	same.Status.Conditions = []api.Condition{{Type: api.VolumeSnapshotConditionSnapshotting, Status: "False"}}
	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), &same)
	assert.NoError(t, err, "control: the same size in other units, and a condition")

	_, err = e.api.PutVolumeSnapshotStatus(ctx(t), volumeSnapshot("absent", "v1"))
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestVolumeSnapshots_ListAndWatchTakeALabelSelector(t *testing.T) {
	e := newEnv(t)
	web := volumeSnapshot("a", "v1")
	web.Metadata.Labels = map[string]string{"app": "web"}
	_, err := e.api.PutVolumeSnapshot(ctx(t), web)
	require.NoError(t, err)
	e.createVolumeSnapshot(t, "b", "v1")

	all, err := e.api.ListVolumeSnapshots(ctx(t))
	require.NoError(t, err)
	require.Len(t, all.Items, 2, "control: no selector lists every snapshot")
	assert.Equal(t, "a", all.Items[0].Metadata.Name)

	selected := api.NewClient(e.base+apiRoot, nil, api.WithLabelSelector(mustSelector(t, "app=web")))
	list, err := selected.ListVolumeSnapshots(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "the selector narrows the list")
	assert.Equal(t, "a", list.Items[0].Metadata.Name)

	w, err := selected.WatchVolumeSnapshots(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	e.createVolumeSnapshot(t, "c", "v1")
	d := volumeSnapshot("d", "v1")
	d.Metadata.Labels = map[string]string{"app": "web"}
	_, err = e.api.PutVolumeSnapshot(ctx(t), d)
	require.NoError(t, err)
	ev := next(t, w)
	assert.Equal(t, "d", ev.Object.Metadata.Name, "the unlabelled c is not sent")

	_, err = e.api.GetVolumeSnapshot(ctx(t), "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestDeleteVolumeSnapshot_WithAFinalizerSetsTheDeletionTimeAndWithoutRemovesAtOnce(t *testing.T) {
	e := newEnv(t)
	plain := e.createVolumeSnapshot(t, "plain", "v1")
	held := volumeSnapshot("held", "v1")
	held.Metadata.Finalizers = []string{holdFinalizer}
	heldCreated, err := e.api.PutVolumeSnapshot(ctx(t), held)
	require.NoError(t, err)

	_, err = e.api.DeleteVolumeSnapshot(ctx(t), "held", "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	deleting, err := e.api.DeleteVolumeSnapshot(ctx(t), "held", heldCreated.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.NotNil(t, deleting.Metadata.DeletionTimestamp)
	got, err := e.api.GetVolumeSnapshot(ctx(t), "held")
	require.NoError(t, err, "a snapshot with a finalizer stays")
	assert.NotNil(t, got.Metadata.DeletionTimestamp)

	_, err = e.api.DeleteVolumeSnapshot(ctx(t), "plain", plain.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetVolumeSnapshot(ctx(t), "plain")
	assert.ErrorIs(t, err, api.ErrNotFound, "a snapshot with no finalizer is removed")
	_, err = e.api.DeleteVolumeSnapshot(ctx(t), "absent", "1")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestPutVolumeSnapshot_ClearingTheLastFinalizerOfASnapshotBeingDeletedRemovesIt(t *testing.T) {
	e := newEnv(t)
	held := volumeSnapshot("s1", "v1")
	held.Metadata.Finalizers = []string{holdFinalizer, "example.com/other"}
	created, err := e.api.PutVolumeSnapshot(ctx(t), held)
	require.NoError(t, err)
	deleting, err := e.api.DeleteVolumeSnapshot(ctx(t), "s1", created.Metadata.ResourceVersion)
	require.NoError(t, err)

	added := *deleting
	added.Metadata.Finalizers = []string{holdFinalizer, "example.com/other", "example.com/new"}
	_, err = e.api.PutVolumeSnapshot(ctx(t), &added)
	assert.ErrorIs(t, err, api.ErrInvalid, "a finalizer cannot be added to a snapshot being deleted")

	one := *deleting
	one.Metadata.Finalizers = []string{"example.com/other"}
	remaining, err := e.api.PutVolumeSnapshot(ctx(t), &one)
	require.NoError(t, err, "control: one finalizer is cleared")
	_, err = e.api.GetVolumeSnapshot(ctx(t), "s1")
	require.NoError(t, err, "the snapshot stays while a finalizer is left")

	none := *remaining
	none.Metadata.Finalizers = nil
	_, err = e.api.PutVolumeSnapshot(ctx(t), &none)
	require.NoError(t, err)
	_, err = e.api.GetVolumeSnapshot(ctx(t), "s1")
	assert.ErrorIs(t, err, api.ErrNotFound, "the last finalizer was cleared")
}

func TestPutVolume_ALocalDataSourceIsStillRefusedWhileSnapshotsAreServed(t *testing.T) {
	e := newEnv(t)
	e.createVolumeSnapshot(t, "s1", "v1")
	v := volume("restored")
	v.Spec.DataSource = "s1"

	_, err := e.api.PutVolume(ctx(t), v)
	assert.ErrorIs(t, err, api.ErrInvalid, "a local Volume from a snapshot waits for #253")
	v.Spec.DataSource = ""
	_, err = e.api.PutVolume(ctx(t), v)
	assert.NoError(t, err, "control: the same Volume with no data source")
}
