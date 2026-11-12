package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newClusterEvent(name string) *api.ClusterEvent {
	first := time.Now().UTC()
	return &api.ClusterEvent{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEvent},
		Metadata: api.ObjectMeta{Name: name},
		Spec: api.ClusterEventSpec{
			InvolvedObject: api.ObjectReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "vm-1", UID: "u1"},
			Reason:         "Scheduled",
			Message:        "bound to node-a",
			Type:           api.SeverityNormal,
			Count:          1,
			FirstTimestamp: first,
			LastTimestamp:  first,
		},
	}
}

func TestClusterEventValidateChecksTheSpec(t *testing.T) {
	require.NoError(t, newClusterEvent("e1").Validate(), "control")

	for name, edit := range map[string]func(*api.ClusterEvent){
		"another kind":           func(e *api.ClusterEvent) { e.Kind = "Node" },
		"another group":          func(e *api.ClusterEvent) { e.APIVersion = api.RBACGroupVersion },
		"no name":                func(e *api.ClusterEvent) { e.Metadata.Name = "" },
		"a bad label":            func(e *api.ClusterEvent) { e.Metadata.Labels = map[string]string{"bad key": "v"} },
		"no involved apiVersion": func(e *api.ClusterEvent) { e.Spec.InvolvedObject.APIVersion = "" },
		"no involved kind":       func(e *api.ClusterEvent) { e.Spec.InvolvedObject.Kind = "" },
		"no involved name":       func(e *api.ClusterEvent) { e.Spec.InvolvedObject.Name = "" },
		"no reason":              func(e *api.ClusterEvent) { e.Spec.Reason = "" },
		"a long message":         func(e *api.ClusterEvent) { e.Spec.Message = strings.Repeat("m", 1025) },
		"no type":                func(e *api.ClusterEvent) { e.Spec.Type = "" },
		"an unknown type":        func(e *api.ClusterEvent) { e.Spec.Type = "Error" },
		"a zero count":           func(e *api.ClusterEvent) { e.Spec.Count = 0 },
		"a negative count":       func(e *api.ClusterEvent) { e.Spec.Count = -1 },
		"no first timestamp":     func(e *api.ClusterEvent) { e.Spec.FirstTimestamp = time.Time{} },
		"last before first": func(e *api.ClusterEvent) {
			e.Spec.LastTimestamp = e.Spec.FirstTimestamp.Add(-time.Second)
		},
	} {
		e := newClusterEvent("e1")
		edit(e)
		assert.ErrorIs(t, e.Validate(), api.ErrInvalid, name)
	}

	warn := newClusterEvent("e1")
	warn.Spec.Type = api.SeverityWarning
	warn.Spec.Message = strings.Repeat("m", 1024)
	warn.Spec.InvolvedObject.UID = ""
	assert.NoError(t, warn.Validate(), "control: a warning, a message at the limit, an involved object without a uid")
}

func TestClusterEventValidateUpdateKeepsWhatItIsAbout(t *testing.T) {
	old := newClusterEvent("e1")

	again := newClusterEvent("e1")
	again.Spec.Count = 3
	again.Spec.LastTimestamp = old.Spec.FirstTimestamp.Add(time.Minute)
	again.Spec.Message = "a new message"
	require.NoError(t, again.ValidateUpdate(old), "control: a repeat raises the count and moves the last timestamp")

	for name, edit := range map[string]func(*api.ClusterEvent){
		"another involved name": func(e *api.ClusterEvent) { e.Spec.InvolvedObject.Name = "vm-2" },
		"another involved uid":  func(e *api.ClusterEvent) { e.Spec.InvolvedObject.UID = "u2" },
		"another reason":        func(e *api.ClusterEvent) { e.Spec.Reason = "Failed" },
		"another type":          func(e *api.ClusterEvent) { e.Spec.Type = api.SeverityWarning },
	} {
		e := newClusterEvent("e1")
		e.Spec.FirstTimestamp, e.Spec.LastTimestamp = old.Spec.FirstTimestamp, old.Spec.LastTimestamp
		edit(e)
		assert.ErrorIs(t, e.ValidateUpdate(old), api.ErrInvalid, name)
	}

	raised := newClusterEvent("e1")
	raised.Spec.Count = 5
	stored := newClusterEvent("e1")
	stored.Spec.Count = 5
	stored.Spec.FirstTimestamp, stored.Spec.LastTimestamp = raised.Spec.FirstTimestamp, raised.Spec.LastTimestamp
	require.NoError(t, raised.ValidateUpdate(stored), "control: the same count")
	raised.Spec.Count = 4
	assert.ErrorIs(t, raised.ValidateUpdate(stored), api.ErrInvalid, "a count that falls")
}

func TestClientClusterEventUsesItsPath(t *testing.T) {
	ctx := testContext(t)
	var method, path, query string
	ts := newRecordingServer(t, func(r *http.Request) { method, path, query = r.Method, r.URL.Path, r.URL.RawQuery })
	c := api.NewClient(ts.URL+"/compute", nil)

	_, err := c.PutClusterEvent(ctx, newClusterEvent("e1"))
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "/compute/events/e1", path)

	_, err = c.GetClusterEvent(ctx, "e1")
	require.NoError(t, err)
	assert.Equal(t, "/compute/events/e1", path)

	_, err = c.ListClusterEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, "/compute/events", path)

	_, err = c.DeleteClusterEvent(ctx, "e1", "4")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "resourceVersion=4", query)

	_, err = c.DeleteClusterEvent(ctx, "e1", "")
	assert.Error(t, err, "a delete without a version is not sent")
}
