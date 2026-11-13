package guest_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/guest"
)

func TestLog_RefusesAQueryThatDoesNotParseAndAcceptsTheSameValueEscaped(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "hello")
	base := "bindingEpoch=3&uid=uid-1&"
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"a value escaped", base + "follow=%66alse", true},
		{"an escape that is not hex", base + "follow=%zz", false},
		{"an escape cut short", base + "follow=%6", false},
		{"a semicolon", base + "follow=false;tailBytes=1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := e.client.Get("https://" + e.addr + "/microvms/" + vm + "/log?" + tt.raw)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			if tt.ok {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				return
			}
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
			assert.Equal(t, api.ReasonInvalid, reason(t, resp))
		})
	}
}

// syncLog is a log destination the handler and the test share.
type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stub is a Service whose calls are the functions given.
type stub struct {
	log  func(context.Context, guest.LogRequest, guest.LogOutput) error
	exec func(context.Context, guest.ExecRequest, guest.ExecOutput) error
}

func (s stub) Log(ctx context.Context, r guest.LogRequest, o guest.LogOutput) error {
	return s.log(ctx, r, o)
}

func (s stub) Exec(ctx context.Context, r guest.ExecRequest, o guest.ExecOutput) error {
	return s.exec(ctx, r, o)
}

func serve(t *testing.T, svc guest.Service) (*httptest.Server, *syncLog) {
	t.Helper()
	logs := &syncLog{}
	srv := httptest.NewServer(guest.Handler(svc, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	return srv, logs
}

func TestHandler_AnswersInternalErrorWhenTheServicePanicsAndLogsItWithATraceID(t *testing.T) {
	srv, logs := serve(t, stub{
		log: func(_ context.Context, r guest.LogRequest, _ guest.LogOutput) error {
			if r.Name == "fine" {
				return nil
			}
			panic("boom")
		},
	})
	get := func(name string) *http.Response {
		resp, err := http.Get(srv.URL + "/microvms/" + name + "/log?bindingEpoch=3&uid=uid-1")
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	resp := get("web")

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, api.ReasonInternalError, reason(t, resp))
	assert.Regexp(t, `level=ERROR msg="handler panicked" trace_id=[0-9a-f]{16} `, logs.String())
	assert.Contains(t, logs.String(), "panic=boom")
	assert.Equal(t, http.StatusOK, get("fine").StatusCode, "control: a service that does not panic is answered by its own result")
}

func TestHandler_LogsEachFailureWithTheTraceIDOfItsRequest(t *testing.T) {
	boom := &api.APIError{Code: http.StatusInternalServerError, Reason: api.ReasonInternalError, Message: "x"}
	srv, logs := serve(t, stub{
		log: func(context.Context, guest.LogRequest, guest.LogOutput) error { return boom },
	})
	for range 2 {
		resp, err := http.Get(srv.URL + "/microvms/web/log?bindingEpoch=3&uid=uid-1")
		require.NoError(t, err)
		_ = resp.Body.Close()
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 2)
	re := regexp.MustCompile(`trace_id=([0-9a-f]{16}) `)
	first, second := re.FindStringSubmatch(lines[0]), re.FindStringSubmatch(lines[1])
	require.NotNil(t, first, lines[0])
	require.NotNil(t, second, lines[1])
	assert.NotEqual(t, first[1], second[1], "each request has a trace id of its own")
}
