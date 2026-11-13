package command_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

func logOf(output string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(output))
	}
}

func TestLogs_PrintsTheOutputOfTheMicroVMAndSendsNoQueryByDefault(t *testing.T) {
	s := newSubresourceServer(t, logOf("line 1\nline 2\n"))

	stdout, stderr, code := executeAt(t, s.server(), "logs", "web")

	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "line 1\nline 2\n", stdout)
	call := s.only(t)
	assert.Equal(t, "GET", call.method)
	assert.Equal(t, "/microvms/web/log", call.path)
	assert.Equal(t, url.Values{}, call.query, "bindingEpoch and uid are the API server's to send, and follow and tailBytes are not sent unasked")
}

func TestLogs_MapsFollowAndTailBytesToTheirParameters(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		query url.Values
	}{
		{"-f", []string{"logs", "-f", "web"}, url.Values{"follow": {"true"}}},
		{"--follow", []string{"logs", "--follow", "web"}, url.Values{"follow": {"true"}}},
		{"--tail-bytes", []string{"logs", "--tail-bytes", "512", "web"}, url.Values{"tailBytes": {"512"}}},
		{"flags after the name", []string{"logs", "web", "-f", "--tail-bytes", "7"}, url.Values{"follow": {"true"}, "tailBytes": {"7"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSubresourceServer(t, logOf("x"))

			_, stderr, code := executeAt(t, s.server(), tt.args...)

			require.Equal(t, command.ExitOK, code, stderr)
			assert.Equal(t, tt.query, s.only(t).query)
		})
	}
}

func TestLogs_RefusesATailOfZeroOrLessBeforeAnyRequestAndAcceptsOne(t *testing.T) {
	s := newSubresourceServer(t, logOf("x"))

	for _, n := range []string{"0", "-1"} {
		_, stderr, code := executeAt(t, s.server(), "logs", "--tail-bytes", n, "web")
		assert.Equal(t, command.ExitUsage, code, "--tail-bytes %s: %s", n, stderr)
		assert.Contains(t, stderr, "--tail-bytes")
	}
	assert.Empty(t, s.received(), "a refused value sends nothing")

	_, stderr, code := executeAt(t, s.server(), "logs", "--tail-bytes", "1", "web")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Len(t, s.received(), 1)
}

func TestLogs_ExitsNonZeroWhenTheStreamIsAbortedAndZeroWhenItEndsCleanly(t *testing.T) {
	aborted := newSubresourceServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial output\n"))
		abort(w)
	})
	clean := newSubresourceServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial output\n"))
	})

	stdout, stderr, code := executeAt(t, aborted.server(), "logs", "web")
	assert.Equal(t, command.ExitFailure, code, "an aborted stream is never a clean end")
	assert.Equal(t, "partial output\n", stdout, "what arrived before the cut is printed")
	assert.Contains(t, stderr, "aborted")

	stdout, stderr, code = executeAt(t, clean.server(), "logs", "web")
	assert.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "partial output\n", stdout)
	assert.Empty(t, stderr)
}

func TestLogs_ReportsTheReasonOfAnAnswerWithNoMachine(t *testing.T) {
	tests := []struct {
		reason api.Reason
		status int
	}{
		{api.ReasonNotRunning, 409},
		{api.ReasonUnavailable, 503},
		{api.ReasonNotFound, 404},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			s := newSubresourceServer(t, func(w http.ResponseWriter, _ *http.Request) { failWith(w, tt.reason, "no machine here") })

			stdout, stderr, code := executeAt(t, s.server(), "logs", "web")

			assert.Equal(t, command.ExitFailure, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, string(tt.reason))
			assert.Contains(t, stderr, "no machine here")
		})
	}
}

func TestLogs_FollowEndsQuietlyWhenTheUserCancelsIt(t *testing.T) {
	sent := make(chan struct{})
	s := newSubresourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first\n"))
		w.(http.Flusher).Flush()
		close(sent)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		<-sent
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	var out, errOut bytes.Buffer
	code := command.Run(ctx, s.server(), nil, []string{"logs", "-f", "web"}, nil, &out, &errOut)

	assert.Equal(t, command.ExitOK, code, errOut.String())
	assert.Equal(t, "first\n", out.String())
}

func TestLogs_WantsOneName(t *testing.T) {
	s := newSubresourceServer(t, logOf("x"))

	for _, args := range [][]string{{"logs"}, {"logs", "a", "b"}} {
		_, stderr, code := executeAt(t, s.server(), args...)
		assert.Equal(t, command.ExitUsage, code, stderr)
	}
	assert.Empty(t, s.received())
}
