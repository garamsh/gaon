package api_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newLease(name string) *api.Lease {
	return &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.LeaseSpec{Holder: "agent-1", RenewTime: time.Now().UTC(), DurationSeconds: 15},
	}
}

func TestClientLeaseGetAndList(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetLease(ctx, "node-a")
	assert.ErrorIs(t, err, api.ErrNotFound)

	_, err = c.PutLease(ctx, newLease("node-a"))
	require.NoError(t, err)
	second, err := c.PutLease(ctx, newLease("node-b"))
	require.NoError(t, err)

	got, err := c.GetLease(ctx, "node-b")
	require.NoError(t, err)
	assert.Equal(t, "node-b", got.Metadata.Name)
	assert.Equal(t, "agent-1", got.Spec.Holder)

	list, err := c.ListLeases(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindLeaseList, list.Kind)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, second.Metadata.ResourceVersion, list.Metadata.ResourceVersion)
}

func TestClientWatchLeasesDeliversLeaseEventsOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutNode(ctx, newNode("node-a"))
	require.NoError(t, err)
	created, err := c.PutLease(ctx, newLease("node-a"))
	require.NoError(t, err)

	w, err := c.WatchLeases(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "node-a", ev.Object.Metadata.Name, "the Node's event is not delivered to a Lease watch")

	renew := *created
	renew.Spec.RenewTime = renew.Spec.RenewTime.Add(5 * time.Second)
	renewed, err := c.PutLease(ctx, &renew)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, renewed.Metadata.ResourceVersion, ev.Object.Metadata.ResourceVersion)

	s.compact()
	stale, err := c.WatchLeases(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)

	_, err = c.WatchLeases(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}
