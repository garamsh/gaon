package apiserver_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

const coordinationAPIRoot = "/apis/coordination.gaon.garam.sh/v1alpha1"

func (e *env) coordination() *api.Client { return api.NewClient(e.base+coordinationAPIRoot, nil) }

func coordinationLease(name, holder string) *api.CoordinationLease {
	return &api.CoordinationLease{
		TypeMeta: api.TypeMeta{APIVersion: api.CoordinationGroupVersion, Kind: api.KindCoordinationLease},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CoordinationLeaseSpec{Holder: holder, RenewTime: time.Now().UTC(), DurationSeconds: 15},
	}
}

func clusterEvent(name string) *api.ClusterEvent {
	now := time.Now().UTC()
	return &api.ClusterEvent{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEvent},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.ClusterEventSpec{
			InvolvedObject: api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "m1"},
			Reason:         "Scheduled",
			Type:           api.SeverityNormal,
			Count:          1,
			FirstTimestamp: now,
			LastTimestamp:  now,
		},
	}
}

func TestCoordinationLease_IsServedWithCompareAndSwap(t *testing.T) {
	e := newEnv(t)
	c := e.coordination()

	created, err := c.PutCoordinationLease(ctx(t), coordinationLease("scheduler", "a"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.UID)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	_, err = c.PutCoordinationLease(ctx(t), coordinationLease("scheduler", "b"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists, "a second create of the name")

	taken := *created
	taken.Spec.Holder = "b"
	updated, err := c.PutCoordinationLease(ctx(t), &taken)
	require.NoError(t, err, "control: the update at the current version")
	stale := *created
	stale.Spec.Holder = "c"
	_, err = c.PutCoordinationLease(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "an update at a version that is no longer current")

	got, err := c.GetCoordinationLease(ctx(t), "scheduler")
	require.NoError(t, err)
	assert.Equal(t, "b", got.Spec.Holder)
	list, err := c.ListCoordinationLeases(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1)

	_, err = c.DeleteCoordinationLease(ctx(t), "scheduler", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a delete at a stale version")
	_, err = c.DeleteCoordinationLease(ctx(t), "scheduler", updated.Metadata.ResourceVersion)
	require.NoError(t, err, "control: the delete at the current version")
	_, err = c.GetCoordinationLease(ctx(t), "scheduler")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestCoordinationLease_DoesNotShareANameWithTheNodeLease(t *testing.T) {
	e := newEnv(t)
	_, err := e.api.PutNode(ctx(t), node("n1"))
	require.NoError(t, err)
	_, err = e.api.PutLease(ctx(t), lease("n1"))
	require.NoError(t, err)

	_, err = e.coordination().PutCoordinationLease(ctx(t), coordinationLease("n1", "a"))
	require.NoError(t, err, "the same name in the other group is not a clash")
	nodeLease, err := e.api.GetLease(ctx(t), "n1")
	require.NoError(t, err)
	assert.Equal(t, "n1", nodeLease.Spec.Holder, "the node's Lease is untouched")
	list, err := e.api.ListLeases(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "the node Leases list does not carry the coordination one")
}

func TestCoordinationLease_ARequestIsValidated(t *testing.T) {
	e := newEnv(t)
	bad := coordinationLease("bad", "a")
	bad.Spec.DurationSeconds = 0
	_, err := e.coordination().PutCoordinationLease(ctx(t), bad)
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = e.coordination().PutCoordinationLease(ctx(t), coordinationLease("good", "a"))
	assert.NoError(t, err, "control: a valid Lease")
}

func TestCoordinationLease_ASelectorAndAWatchApply(t *testing.T) {
	e := newEnv(t)
	c := e.coordination()
	list, err := c.ListCoordinationLeases(ctx(t))
	require.NoError(t, err)
	web := coordinationLease("web", "a")
	web.Metadata.Labels = map[string]string{"app": "web"}
	_, err = c.PutCoordinationLease(ctx(t), web)
	require.NoError(t, err)
	_, err = c.PutCoordinationLease(ctx(t), coordinationLease("bare", "a"))
	require.NoError(t, err)

	selected := api.NewClient(e.base+coordinationAPIRoot, nil, api.WithLabelSelector(mustSelector(t, "app=web")))
	got, err := selected.ListCoordinationLeases(ctx(t))
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	assert.Equal(t, "web", got.Items[0].Metadata.Name)

	w, err := selected.WatchCoordinationLeases(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "web", ev.Object.Metadata.Name, "the unlabeled Lease is not replayed")
}

func TestEvent_IsServedWithCompareAndSwap(t *testing.T) {
	e := newEnv(t)

	created, err := e.api.PutClusterEvent(ctx(t), clusterEvent("ev1"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.UID)
	_, err = e.api.PutClusterEvent(ctx(t), clusterEvent("ev1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	again := *created
	again.Spec.Count = 2
	again.Spec.LastTimestamp = again.Spec.LastTimestamp.Add(time.Second)
	updated, err := e.api.PutClusterEvent(ctx(t), &again)
	require.NoError(t, err, "control: the count rises at the current version")
	assert.Equal(t, int32(2), updated.Spec.Count)
	stale := *created
	stale.Spec.Count = 3
	_, err = e.api.PutClusterEvent(ctx(t), &stale)
	assert.ErrorIs(t, err, api.ErrConflict, "an update at a stale version")

	list, err := e.api.ListClusterEvents(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1)
	_, err = e.api.DeleteClusterEvent(ctx(t), "ev1", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = e.api.DeleteClusterEvent(ctx(t), "ev1", updated.Metadata.ResourceVersion)
	assert.NoError(t, err, "control: the delete at the current version")
}

func TestEvent_AnUpdateMayNotChangeWhatItIsAbout(t *testing.T) {
	e := newEnv(t)
	created, err := e.api.PutClusterEvent(ctx(t), clusterEvent("ev1"))
	require.NoError(t, err)

	other := *created
	other.Spec.InvolvedObject.Name = "m2"
	_, err = e.api.PutClusterEvent(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrInvalid, "another object")

	fewer := *created
	fewer.Spec.Count = 0
	_, err = e.api.PutClusterEvent(ctx(t), &fewer)
	assert.ErrorIs(t, err, api.ErrInvalid, "a count that falls")

	message := *created
	message.Spec.Message = "more detail"
	_, err = e.api.PutClusterEvent(ctx(t), &message)
	assert.NoError(t, err, "control: the message may change")
}

func TestEvent_AWatchSeesAnAddedEvent(t *testing.T) {
	e := newEnv(t)
	list, err := e.api.ListClusterEvents(ctx(t))
	require.NoError(t, err)
	w, err := e.api.WatchClusterEvents(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	_, err = e.api.PutClusterEvent(ctx(t), clusterEvent("ev1"))
	require.NoError(t, err)
	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "ev1", ev.Object.Metadata.Name)
}
