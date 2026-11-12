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

// next reads one event, failing the test if none arrives.
func next[T any](t *testing.T, w *api.Watch[T]) api.Event[T] {
	t.Helper()
	type result struct {
		ev  api.Event[T]
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ev, err := w.Next()
		ch <- result{ev, err}
	}()
	select {
	case r := <-ch:
		require.NoError(t, r.err)
		return r.ev
	case <-time.After(15 * time.Second):
		t.Fatal("no event arrived")
		return api.Event[T]{}
	}
}

func TestList_ReturnsAVersionToWatchFrom(t *testing.T) {
	e := newEnv(t)
	e.createMicroVM(t, microvm("a"))
	e.createMicroVM(t, microvm("b"))

	list, err := e.api.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.NotEmpty(t, list.Metadata.ResourceVersion)

	w, err := e.api.WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	e.createMicroVM(t, microvm("c"))
	ev := next(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "c", ev.Object.Metadata.Name, "nothing listed is replayed")
}

func TestList_EmptyKindIsAnEmptyListWithAVersion(t *testing.T) {
	e := newEnv(t)
	list, err := e.api.ListNodes(ctx(t))
	require.NoError(t, err)
	assert.Empty(t, list.Items)
	assert.NotEmpty(t, list.Metadata.ResourceVersion)
	_, err = e.api.GetNode(ctx(t), "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestWatch_ResumesFromAGivenVersion(t *testing.T) {
	e := newEnv(t)
	m := e.createMicroVM(t, microvm("m1"))
	from := m.Metadata.ResourceVersion

	update := *m
	update.Spec.RunStrategy = api.RunHalted
	updated, err := e.api.PutMicroVM(ctx(t), &update)
	require.NoError(t, err)
	_, err = e.api.DeleteMicroVM(ctx(t), "m1", updated.Metadata.ResourceVersion)
	require.NoError(t, err)

	w, err := e.api.WatchMicroVMs(ctx(t), from)
	require.NoError(t, err)
	defer w.Close()

	modified := next(t, w)
	assert.Equal(t, api.EventModified, modified.Type, "the create at the given version is not replayed")
	assert.Equal(t, api.RunHalted, modified.Object.Spec.RunStrategy)
	assert.Equal(t, updated.Metadata.ResourceVersion, modified.Object.Metadata.ResourceVersion)

	deleted := next(t, w)
	assert.Equal(t, api.EventDeleted, deleted.Type)
	assert.Equal(t, "m1", deleted.Object.Metadata.Name)
	assert.NotEqual(t, updated.Metadata.ResourceVersion, deleted.Object.Metadata.ResourceVersion, "a Deleted event carries the version of the deletion")

	e.createMicroVM(t, microvm("m2"))
	added := next(t, w)
	assert.Equal(t, api.EventAdded, added.Type, "the stream stays open for later events")
	assert.Equal(t, "m2", added.Object.Metadata.Name)
}

func TestWatch_CoversNodesAndLeases(t *testing.T) {
	e := newEnv(t)
	list, err := e.api.ListNodes(ctx(t))
	require.NoError(t, err)
	nodes, err := e.api.WatchNodes(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer nodes.Close()
	leases, err := e.api.WatchLeases(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer leases.Close()

	e.createNode(t, "n1")
	_, err = e.api.PutLease(ctx(t), lease("n1"))
	require.NoError(t, err)

	assert.Equal(t, "n1", next(t, nodes).Object.Metadata.Name)
	got := next(t, leases)
	assert.Equal(t, api.EventAdded, got.Type)
	assert.Equal(t, "n1", got.Object.Metadata.Name)
}

func TestWatch_ExpiredVersionEndsWithAnExpiredErrorEvent(t *testing.T) {
	e := newEnv(t)
	m := e.createMicroVM(t, microvm("m1"))
	old := m.Metadata.ResourceVersion
	for i := 0; i < 3; i++ {
		update := *m
		update.Spec.IdleTimeoutSeconds = int32(i + 1)
		var err error
		m, err = e.api.PutMicroVM(ctx(t), &update)
		require.NoError(t, err)
	}
	head, err := strconv.ParseInt(string(m.Metadata.ResourceVersion), 10, 64)
	require.NoError(t, err)
	_, err = e.etcd.Compact(ctx(t), head)
	require.NoError(t, err)

	w, err := e.api.WatchMicroVMs(ctx(t), old)
	require.NoError(t, err, "the response is a stream, not an error status")
	defer w.Close()
	_, err = w.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
	_, err = w.Next()
	assert.Error(t, err, "the Error event is the last of the stream")

	fresh, err := e.api.WatchMicroVMs(ctx(t), m.Metadata.ResourceVersion)
	require.NoError(t, err, "control: the current version is not expired")
	defer fresh.Close()
	e.createMicroVM(t, microvm("m2"))
	assert.Equal(t, "m2", next(t, fresh).Object.Metadata.Name)
}

func TestWatch_RequiresAVersion(t *testing.T) {
	e := newEnv(t)

	status, _ := e.send(t, http.MethodGet, "/microvms?watch=true", "")
	assert.Equal(t, http.StatusUnprocessableEntity, status, "no version")
	status, _ = e.send(t, http.MethodGet, "/microvms?watch=true&resourceVersion=abc", "")
	assert.Equal(t, http.StatusUnprocessableEntity, status, "not a number")
	_, err := e.api.WatchMicroVMs(ctx(t), "abc")
	assert.ErrorIs(t, err, api.ErrInvalid)
	status, _ = e.send(t, http.MethodGet, "/microvms?watch=true&resourceVersion=-3", "")
	assert.Equal(t, http.StatusUnprocessableEntity, status)

	w, err := e.api.WatchMicroVMs(ctx(t), "1")
	require.NoError(t, err, "control: a numeric version is accepted")
	w.Close()
}

func TestPaths_RefuseWhatTheContractDoesNotServe(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/microvms"},
		{http.MethodDelete, "/nodes/n1"},
		{http.MethodGet, "/volumes/v1/status"},
	} {
		status, _ := e.send(t, c.method, c.path, "")
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, status, "%s %s", c.method, c.path)
	}
	status, _ := e.send(t, http.MethodGet, "/microvms/a%2Fb", "")
	assert.NotEqual(t, http.StatusOK, status)
}
