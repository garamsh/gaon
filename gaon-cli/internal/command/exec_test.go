package command_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

// output is an Output line of the exec stream.
func output(stream, data string) string {
	return fmt.Sprintf(`{"type":"Output","stream":%q,"data":%q}`+"\n", stream, base64.StdEncoding.EncodeToString([]byte(data)))
}

// exited is an Exit line of a command that exited with code.
func exited(code int) string {
	return fmt.Sprintf(`{"type":"Exit","status":"Exited","exitCode":%d}`+"\n", code)
}

// streams answers an exec with lines, one JSON object each, and ends cleanly.
func streams(lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(strings.Join(lines, "")))
	}
}

func TestExec_SendsTheCommandTheEnvironmentAndTheWorkingDirectory(t *testing.T) {
	s := newSubresourceServer(t, streams(exited(0)))

	_, stderr, code := executeAt(t, s.server(), "exec", "-e", "A=1", "-e", "B=x=y", "-w", "/srv", "web", "--", "ls", "-l", "--all")

	require.Equal(t, command.ExitOK, code, stderr)
	call := s.only(t)
	assert.Equal(t, "POST", call.method)
	assert.Equal(t, "/microvms/web/exec", call.path)
	assert.JSONEq(t, `{"command":["ls","-l","--all"],"env":[{"name":"A","value":"1"},{"name":"B","value":"x=y"}],"workingDir":"/srv"}`, call.body)
}

func TestExec_SendsNoEnvironmentOrWorkingDirectoryUnasked(t *testing.T) {
	s := newSubresourceServer(t, streams(exited(0)))

	_, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "true")

	require.Equal(t, command.ExitOK, code, stderr)
	assert.JSONEq(t, `{"command":["true"]}`, s.only(t).body)
}

func TestExec_PrintsStandardOutputAndStandardErrorApart(t *testing.T) {
	s := newSubresourceServer(t, streams(output("Stdout", "out 1\n"), output("Stderr", "err\n"), output("Stdout", "out 2\n"), exited(0)))

	stdout, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "build")

	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "out 1\nout 2\n", stdout)
	assert.Equal(t, "err\n", stderr)
}

func TestExec_ExitsWithTheExitCodeOfTheCommand(t *testing.T) {
	tests := []struct {
		name string
		exit string
		code int
	}{
		{"zero is success", exited(0), command.ExitOK},
		{"3", exited(3), 3},
		{"1 is the command's, as any other", exited(1), 1},
		{"255", exited(255), 255},
		{"256 is not read as success", exited(256), command.ExitFailure},
		{"a negative code is a failure", exited(-1), command.ExitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSubresourceServer(t, streams(output("Stdout", "done\n"), tt.exit))

			stdout, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "job")

			assert.Equal(t, tt.code, code, stderr)
			assert.Equal(t, "done\n", stdout, "the output is printed whatever the code")
			if tt.code != command.ExitOK {
				assert.NotEmpty(t, stderr)
			}
		})
	}
}

func TestExec_ReportsACommandThatWasCutShortOrNeverStarted(t *testing.T) {
	tests := []struct {
		name, exit, want string
	}{
		{"cut short", `{"type":"Exit","status":"CutShort","termSignal":9,"reason":"killed by the guest"}` + "\n", "killed by the guest"},
		{"not started", `{"type":"Exit","status":"NotStarted","reason":"no such file"}` + "\n", "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSubresourceServer(t, streams(tt.exit))

			_, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "job")

			assert.Equal(t, command.ExitFailure, code)
			assert.Contains(t, stderr, tt.want)
		})
	}
}

func TestExec_ExitsNonZeroWhenTheStreamIsAbortedAndZeroWhenItEndsWithAnExit(t *testing.T) {
	aborted := newSubresourceServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(output("Stdout", "partial\n")))
		abort(w)
	})
	noExit := newSubresourceServer(t, streams(output("Stdout", "partial\n")))
	clean := newSubresourceServer(t, streams(output("Stdout", "partial\n"), exited(0)))

	stdout, stderr, code := executeAt(t, aborted.server(), "exec", "web", "--", "job")
	assert.Equal(t, command.ExitFailure, code, "an aborted stream is never a clean end")
	assert.Equal(t, "partial\n", stdout)
	assert.Contains(t, stderr, "aborted")

	_, stderr, code = executeAt(t, noExit.server(), "exec", "web", "--", "job")
	assert.Equal(t, command.ExitFailure, code, "a stream that stops before an Exit is not a clean end")
	assert.Contains(t, stderr, "without an exit status")

	stdout, stderr, code = executeAt(t, clean.server(), "exec", "web", "--", "job")
	assert.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "partial\n", stdout)
	assert.Empty(t, stderr)
}

func TestExec_ReportsTheErrorEventOfAStreamThatFailedAfterItBegan(t *testing.T) {
	e, err := json.Marshal(api.NewAPIError(api.ReasonUnavailable, "the agent went away"))
	require.NoError(t, err)
	s := newSubresourceServer(t, streams(output("Stdout", "partial\n"), `{"type":"Error","object":`+string(e)+"}\n"))

	stdout, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "job")

	assert.Equal(t, command.ExitFailure, code)
	assert.Equal(t, "partial\n", stdout)
	assert.Contains(t, stderr, "Unavailable")
	assert.Contains(t, stderr, "the agent went away")
}

func TestExec_ReportsTheReasonOfAnAnswerWithNoMachine(t *testing.T) {
	for _, reason := range []api.Reason{api.ReasonNotRunning, api.ReasonUnavailable, api.ReasonForbidden, api.ReasonInvalid} {
		t.Run(string(reason), func(t *testing.T) {
			s := newSubresourceServer(t, func(w http.ResponseWriter, _ *http.Request) { failWith(w, reason, "no machine here") })

			stdout, stderr, code := executeAt(t, s.server(), "exec", "web", "--", "job")

			assert.Equal(t, command.ExitFailure, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, string(reason))
			assert.Contains(t, stderr, "no machine here")
		})
	}
}

func TestExec_RefusesAMalformedCommandLineBeforeAnyRequestAndAcceptsAWellFormedOne(t *testing.T) {
	s := newSubresourceServer(t, streams(exited(0)))

	for name, args := range map[string][]string{
		"no separator":      {"exec", "web", "ls"},
		"no command":        {"exec", "web", "--"},
		"no name":           {"exec", "--", "ls"},
		"two names":         {"exec", "web", "db", "--", "ls"},
		"env without value": {"exec", "-e", "A", "web", "--", "ls"},
		"env without name":  {"exec", "-e", "=1", "web", "--", "ls"},
		"unknown flag":      {"exec", "-x", "web", "--", "ls"},
	} {
		_, stderr, code := executeAt(t, s.server(), args...)
		assert.Equal(t, command.ExitUsage, code, "%s: %s", name, stderr)
	}
	assert.Empty(t, s.received(), "a refused command line sends nothing")

	_, stderr, code := executeAt(t, s.server(), "exec", "-e", "A=", "web", "--", "ls")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.JSONEq(t, `{"command":["ls"],"env":[{"name":"A"}]}`, s.only(t).body, "an empty value is a value")
}
