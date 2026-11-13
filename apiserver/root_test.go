package apiserver_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// contractPaths is resource-api.md §Paths.
var contractPaths = []struct{ method, path string }{
	{http.MethodPut, "/microvms/m1"},
	{http.MethodPost, "/microvms/m1/binding"},
	{http.MethodPut, "/microvms/m1/status"},
	{http.MethodDelete, "/microvms/m1"},
	{http.MethodGet, "/microvms"},
	{http.MethodGet, "/microvms/m1"},
	{http.MethodGet, "/microvms?watch=true&resourceVersion=abc"},
	{http.MethodPut, "/volumes/v1"},
	{http.MethodDelete, "/volumes/v1"},
	{http.MethodGet, "/volumes"},
	{http.MethodGet, "/volumes/v1"},
	{http.MethodGet, "/volumes?watch=true&resourceVersion=abc"},
	{http.MethodPut, "/volumes/v1/status"},
	{http.MethodPut, "/storageclasses/local"},
	{http.MethodDelete, "/storageclasses/local"},
	{http.MethodGet, "/storageclasses"},
	{http.MethodGet, "/storageclasses/local"},
	{http.MethodGet, "/storageclasses?watch=true&resourceVersion=abc"},
	{http.MethodPut, "/nodes/n1"},
	{http.MethodPut, "/nodes/n1/status"},
	{http.MethodGet, "/nodes"},
	{http.MethodGet, "/nodes/n1"},
	{http.MethodGet, "/nodes?watch=true&resourceVersion=abc"},
	{http.MethodPut, "/leases/n1"},
	{http.MethodGet, "/leases"},
	{http.MethodGet, "/leases/n1"},
	{http.MethodGet, "/leases?watch=true&resourceVersion=abc"},
}

// routeMiss is the message of the NotFound that answers a path no route
// serves, which tells it from the NotFound of a resource that is absent.
const routeMiss = "no such path"

func TestRoot_ServesEveryContractPathOnlyUnderIt(t *testing.T) {
	e := newEnv(t)
	for _, c := range contractPaths {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			body := ""
			if c.method == http.MethodPut || c.method == http.MethodPost {
				body = "{"
			}
			_, under := e.send(t, c.method, c.path, body)
			assert.NotContains(t, under, routeMiss, "under the root")

			status, outside := e.sendAbsolute(t, c.method, c.path, body)
			assert.Equal(t, http.StatusNotFound, status, "without the root")
			assert.Contains(t, outside, routeMiss, "without the root")
		})
	}
}

func TestRoot_PathOutsideItAnswersTheNotFoundObject(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/",
		"/microvms",
		apiRoot,
		"/apis/compute.gaon.garam.sh/v1/microvms",
		"/apis/other.group/v1alpha1/microvms",
		"/apis/compute.gaon.garam.sh/v1alpha1x/microvms",
	} {
		status, body := e.sendAbsolute(t, http.MethodGet, path, "")
		assert.Equal(t, http.StatusNotFound, status, path)
		var got api.APIError
		require.NoError(t, json.NewDecoder(strings.NewReader(body)).Decode(&got), path)
		assert.Equal(t, api.ReasonNotFound, got.Reason, path)
		assert.Equal(t, http.StatusNotFound, got.Code, path)
	}

	status, _ := e.sendAbsolute(t, http.MethodGet, apiRoot+"/microvms", "")
	assert.Equal(t, http.StatusOK, status, "the same collection under the root")
}
