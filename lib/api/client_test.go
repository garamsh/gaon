package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClientPutMachineCreatesOnceAndUpdatesByVersion(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	created, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	assert.Equal(t, api.MachinePending, created.Status.Phase)

	_, err = c.PutMachine(ctx, newMachine("m1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "an empty version creates, and refuses an existing machine")

	changed := *created
	changed.Spec.RunStrategy = api.RunHalted
	updated, err := c.PutMachine(ctx, &changed)
	require.NoError(t, err)
	assert.Equal(t, api.RunHalted, updated.Spec.RunStrategy)
	assert.NotEqual(t, created.Metadata.ResourceVersion, updated.Metadata.ResourceVersion)

	stale := *created
	stale.Spec.RunStrategy = api.RunOnce
	_, err = c.PutMachine(ctx, &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "a write at an older version loses the compare-and-swap")

	got, err := c.GetMachine(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, api.RunHalted, got.Spec.RunStrategy, "the losing write changed nothing")
}

func TestClientMapsErrorsToSentinels(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetMachine(ctx, "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)
	assert.NotErrorIs(t, err, api.ErrConflict)

	bad := newMachine("m1")
	bad.Spec.RunStrategy = "Sometimes"
	_, err = c.PutMachine(ctx, bad)
	assert.ErrorIs(t, err, api.ErrInvalid)

	var apiErr *api.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Code)
}

func TestClientReportsAnAnswerThatIsNotAnAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gateway down", http.StatusBadGateway)
	}))
	t.Cleanup(ts.Close)
	c := api.NewClient(ts.URL, ts.Client())

	_, err := c.GetMachine(testContext(t), "m1")
	require.Error(t, err)
	var apiErr *api.APIError
	assert.NotErrorAs(t, err, &apiErr)
	assert.Contains(t, err.Error(), "502")
}

func TestClientBindsMachineByVersionAndEpoch(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	created, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)

	_, err = c.BindMachine(ctx, "m1", api.Binding{ResourceVersion: "999", NodeName: "node-a", BindingEpoch: 1})
	assert.ErrorIs(t, err, api.ErrConflict)

	_, err = c.BindMachine(ctx, "m1", api.Binding{ResourceVersion: created.Metadata.ResourceVersion, NodeName: "node-a", BindingEpoch: 5})
	assert.ErrorIs(t, err, api.ErrInvalid, "the epoch must be the current one plus one")

	bound, err := c.BindMachine(ctx, "m1", api.Binding{ResourceVersion: created.Metadata.ResourceVersion, NodeName: "node-a", BindingEpoch: 1})
	require.NoError(t, err)
	assert.Equal(t, "node-a", bound.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), bound.Spec.BindingEpoch)
}

func TestClientWritesStatusOnlyAtTheCurrentEpoch(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	created, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)
	bound, err := c.BindMachine(ctx, "m1", api.Binding{ResourceVersion: created.Metadata.ResourceVersion, NodeName: "node-a", BindingEpoch: 1})
	require.NoError(t, err)

	report := *bound
	report.Status = api.MachineStatus{Phase: api.MachineRunning, BindingEpoch: 0}
	_, err = c.PutMachineStatus(ctx, &report)
	assert.ErrorIs(t, err, api.ErrInvalid, "a status of a stale epoch is refused")

	report.Status.BindingEpoch = 1
	written, err := c.PutMachineStatus(ctx, &report)
	require.NoError(t, err)
	assert.Equal(t, api.MachineRunning, written.Status.Phase)
	assert.Equal(t, api.MachineRunning, written.EffectivePhase())
}

func TestClientListAndDelete(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	first, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)
	second, err := c.PutMachine(ctx, newMachine("m2"))
	require.NoError(t, err)

	list, err := c.ListMachines(ctx)
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, second.Metadata.ResourceVersion, list.Metadata.ResourceVersion)

	_, err = c.DeleteMachine(ctx, "m1", "999")
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = c.DeleteMachine(ctx, "m1", "")
	assert.Error(t, err, "a delete names the version it deletes")

	deleted, err := c.DeleteMachine(ctx, "m1", first.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.NotNil(t, deleted.Metadata.DeletionTimestamp)
	_, err = c.GetMachine(ctx, "m1")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestClientRenewsLeaseByVersion(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	lease := &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: "node-a"},
		Spec:     api.LeaseSpec{Holder: "agent-1", RenewTime: time.Now().UTC(), DurationSeconds: 15},
	}
	created, err := c.PutLease(ctx, lease)
	require.NoError(t, err)

	renew := *created
	renew.Spec.RenewTime = renew.Spec.RenewTime.Add(5 * time.Second)
	renewed, err := c.PutLease(ctx, &renew)
	require.NoError(t, err)
	assert.True(t, renewed.Spec.RenewTime.Equal(renew.Spec.RenewTime))

	_, err = c.PutLease(ctx, &renew)
	assert.ErrorIs(t, err, api.ErrConflict, "a second renewal from the old version loses")
}

func TestClientWritesVolumeStatus(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	vol := newVolume("data")
	s.seedVolume(vol)

	next := *vol
	next.Status = api.VolumeStatus{Phase: api.VolumeBound, Attachment: &api.Attachment{MachineName: "m1", BindingEpoch: 1}}
	written, err := c.PutVolumeStatus(ctx, &next)
	require.NoError(t, err)
	assert.Equal(t, api.VolumeBound, written.Status.Phase)
	assert.Equal(t, &api.Attachment{MachineName: "m1", BindingEpoch: 1}, written.Status.Attachment)

	_, err = c.PutVolumeStatus(ctx, &next)
	assert.ErrorIs(t, err, api.ErrConflict, "the seeded version is no longer current")

	next = *written
	next.Status.Phase = "Mounted"
	_, err = c.PutVolumeStatus(ctx, &next)
	assert.ErrorIs(t, err, api.ErrInvalid)
}

func nextEvent(t *testing.T, w *api.MachineWatch) api.MachineEvent {
	t.Helper()
	ev, err := w.Next()
	require.NoError(t, err)
	return ev
}

func TestClientWatchResumesFromAVersion(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	first, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)

	changed := *first
	changed.Spec.RunStrategy = api.RunHalted
	second, err := c.PutMachine(ctx, &changed)
	require.NoError(t, err)

	w, err := c.WatchMachines(ctx, first.Metadata.ResourceVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	ev := nextEvent(t, w)
	assert.Equal(t, api.EventModified, ev.Type, "the watch resumes after the version it was given")
	assert.Equal(t, second.Metadata.ResourceVersion, ev.Machine.Metadata.ResourceVersion)

	third, err := c.PutMachine(ctx, newMachine("m2"))
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type, "a change made while watching is delivered")
	assert.Equal(t, third.Metadata.ResourceVersion, ev.Machine.Metadata.ResourceVersion)

	_, err = c.DeleteMachine(ctx, "m2", third.Metadata.ResourceVersion)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventDeleted, ev.Type)
	assert.Equal(t, "m2", ev.Machine.Metadata.Name)
}

func TestClientWatchFromAnExpiredVersionIsExpiredAndRelistRecovers(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	first, err := c.PutMachine(ctx, newMachine("m1"))
	require.NoError(t, err)
	_, err = c.PutMachine(ctx, newMachine("m2"))
	require.NoError(t, err)

	live, err := c.WatchMachines(ctx, first.Metadata.ResourceVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = live.Close() })
	assert.Equal(t, "m2", nextEvent(t, live).Machine.Metadata.Name, "before compaction the same version resumes")

	s.compact()

	stale, err := c.WatchMachines(ctx, first.Metadata.ResourceVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	require.ErrorIs(t, err, api.ErrExpired)
	var apiErr *api.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusGone, apiErr.Code)
	_, err = stale.Next()
	assert.ErrorIs(t, err, io.EOF, "the Error event is the last of its stream")

	list, err := c.ListMachines(ctx)
	require.NoError(t, err)
	recovered, err := c.WatchMachines(ctx, list.Metadata.ResourceVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	added, err := c.PutMachine(ctx, newMachine("m3"))
	require.NoError(t, err)
	ev := nextEvent(t, recovered)
	assert.Equal(t, added.Metadata.ResourceVersion, ev.Machine.Metadata.ResourceVersion, "watching from the list's version delivers what follows it")
}

func TestClientWatchRequiresAVersion(t *testing.T) {
	_, c := newMemoryServer(t)
	_, err := c.WatchMachines(testContext(t), "")
	assert.Error(t, err)
}
