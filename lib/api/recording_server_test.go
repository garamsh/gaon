package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newRecordingServer serves an empty JSON object to every request and passes
// each request to seen first.
func newRecordingServer(t *testing.T, seen func(*http.Request)) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(ts.Close)
	return ts
}
