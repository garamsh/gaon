package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// watchOf serves stream as the body of a watch and returns it opened.
func watchOf(t *testing.T, stream string) *api.Watch[api.MicroVM] {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(ts.Close)
	w, err := api.NewClient(ts.URL, ts.Client()).WatchMicroVMs(testContext(t), "1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestWatchEventWireShape(t *testing.T) {
	ev, err := api.NewEvent(api.EventModified, newMicroVM("m1"))
	require.NoError(t, err)
	line, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"Modified","object":`+string(ev.Object)+`}`, string(line))

	var decoded struct {
		Object struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"object"`
	}
	require.NoError(t, json.Unmarshal(line, &decoded))
	assert.Equal(t, "MicroVM", decoded.Object.Kind)
	assert.Equal(t, "m1", decoded.Object.Metadata.Name)

	errEvent, err := api.NewErrorEvent(api.NewAPIError(api.ReasonExpired, "gone"))
	require.NoError(t, err)
	line, err = json.Marshal(errEvent)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"Error","object":{"code":410,"reason":"Expired","message":"gone"}}`, string(line))
}

func TestNewMicroVMEventRefusesTypesThatCarryNoMicroVM(t *testing.T) {
	_, err := api.NewEvent(api.EventError, newMicroVM("m1"))
	assert.Error(t, err)
	for _, ty := range []api.EventType{api.EventAdded, api.EventModified, api.EventDeleted} {
		_, err := api.NewEvent(ty, newMicroVM("m1"))
		assert.NoError(t, err, string(ty))
	}
}

func TestMicroVMWatchDecodesNewlineDelimitedEvents(t *testing.T) {
	var stream strings.Builder
	for _, ty := range []api.EventType{api.EventAdded, api.EventModified, api.EventDeleted} {
		ev, err := api.NewEvent(ty, newMicroVM("m1"))
		require.NoError(t, err)
		line, err := json.Marshal(ev)
		require.NoError(t, err)
		stream.Write(line)
		stream.WriteString("\n")
	}
	w := watchOf(t, stream.String())

	for _, want := range []api.EventType{api.EventAdded, api.EventModified, api.EventDeleted} {
		ev := nextEvent(t, w)
		assert.Equal(t, want, ev.Type)
		assert.Equal(t, "m1", ev.Object.Metadata.Name)
	}
	_, err := w.Next()
	assert.ErrorIs(t, err, io.EOF)
}

func TestMicroVMWatchRefusesWhatItCannotRead(t *testing.T) {
	microvm := `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"MicroVM","metadata":{"name":"m1"}}`
	tests := []struct {
		name   string
		stream string
	}{
		{"unknown event type", `{"type":"Bookmark","object":` + microvm + "}\n"},
		{"object of another kind", `{"type":"Added","object":{"kind":"Volume"}}` + "\n"},
		{"microvm with a malformed quantity", `{"type":"Added","object":{"kind":"MicroVM","spec":{"rootDiskSize":"4GB"}}}` + "\n"},
		{"error event with a non-object", `{"type":"Error","object":"expired"}` + "\n"},
		{"line cut short", `{"type":"Added","obj`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := watchOf(t, tt.stream).Next()
			require.Error(t, err)
			assert.NotErrorIs(t, err, io.EOF)
		})
	}

	// control: the same stream with a well-formed Added event is read.
	ev := nextEvent(t, watchOf(t, `{"type":"Added","object":`+microvm+"}\n"))
	assert.Equal(t, api.EventAdded, ev.Type)
}

func TestMicroVMWatchReturnsTheErrorObjectOfAnErrorEvent(t *testing.T) {
	w := watchOf(t, `{"type":"Error","object":{"code":410,"reason":"Expired","message":"too old"}}`+"\n")
	_, err := w.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
	assert.NotErrorIs(t, err, api.ErrConflict)
	var apiErr *api.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "too old", apiErr.Message)
}
