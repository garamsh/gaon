package command_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// subresourceCall is one request the stream server received.
type subresourceCall struct {
	method string
	path   string
	query  url.Values
	body   string
}

// subresourceServer stands in for the API server's log and exec subresources. It
// answers every request with handle and records what it received.
type subresourceServer struct {
	srv *httptest.Server

	mu    sync.Mutex
	calls []subresourceCall
}

func newSubresourceServer(t *testing.T, handle http.HandlerFunc) *subresourceServer {
	s := &subresourceServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		path, _ := strings.CutPrefix(r.URL.Path, computePrefix)
		s.mu.Lock()
		s.calls = append(s.calls, subresourceCall{r.Method, path, r.URL.Query(), string(body)})
		s.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *subresourceServer) server() string { return s.srv.URL + computePrefix }

func (s *subresourceServer) received() []subresourceCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]subresourceCall(nil), s.calls...)
}

// only returns the one request received, or fails.
func (s *subresourceServer) only(t *testing.T) subresourceCall {
	t.Helper()
	calls := s.received()
	require.Len(t, calls, 1)
	return calls[0]
}

// failWith answers with the error object of reason, as the API server does.
func failWith(w http.ResponseWriter, reason api.Reason, message string) {
	e := api.NewAPIError(reason, "%s", message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	_ = json.NewEncoder(w).Encode(e)
}

// abort sends what has been written so far, then ends the response without
// completing its body, as the API server does when the agent's stream fails.
func abort(w http.ResponseWriter) {
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}
