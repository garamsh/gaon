package guest_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/gaon/node-agent/internal/fakepki"
	"github.com/garamsh/gaon/node-agent/internal/guest"
	"github.com/garamsh/mvmd/lib/runtimeapi"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	vm      = "web"
	epoch   = 3
	uid     = "uid-1"
	machine = "web-e3"
)

// holder holds the MicroVMs it is given, at epoch.
type holder map[string]api.MicroVM

func (h holder) Holds(name string, e api.Epoch, u api.UID) (api.MicroVM, bool) {
	m, ok := h[name]
	if !ok || m.Metadata.UID != u || e != epoch {
		return api.MicroVM{}, false
	}
	return m, true
}

type env struct {
	mvmd   *fakemvmd.Server
	ca     *fakepki.CA
	holds  holder
	addr   string
	client *http.Client
	conn   io.Closer
}

// clientFor returns an HTTP client that trusts the CA and presents certs.
func clientFor(ca *fakepki.CA, certs ...tls.Certificate) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: ca.Pool(), Certificates: certs,
	}}}
}

func (e *env) apiServer(t *testing.T) tls.Certificate {
	c, _, _ := e.ca.Issue(t, fakepki.Subject{CN: "apiserver", Org: []string{guest.APIServerOrganization}}, x509.ExtKeyUsageClientAuth)
	return c
}

func newEnv(t *testing.T, params guest.Params) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mvmd := fakemvmd.New(t, clock.NewFake(time.Now()))
	conn, err := runtimeapi.Dial(ctx, mvmd.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = mvmd.CreateMachine(ctx, &v1alpha2.CreateMachineRequest{MachineId: machine})
	require.NoError(t, err)
	mvmd.SetState(machine, v1alpha2.MachineState_MACHINE_STATE_RUNNING, "")

	ca := fakepki.New(t)
	serving, _, _ := ca.Issue(t, fakepki.Subject{CN: "node-1"}, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
	var m api.MicroVM
	m.Metadata.Name, m.Metadata.UID = vm, uid
	holds := holder{vm: m}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ln, err := guest.Listen(guest.ListenConfig{
		Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Port: 0, CAFile: ca.File(t),
		Certificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serving, nil },
		Handler:     guest.Handler(guest.NewService(holds, v1alpha2.NewRuntimeServiceClient(conn), params), log),
		Log:         log,
	})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { ln.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e := &env{mvmd: mvmd, ca: ca, holds: holds, conn: conn, addr: ln.Addrs()[0].String()}
	e.client = clientFor(ca, e.apiServer(t))
	return e
}

func (e *env) url(path string, q url.Values) string {
	return "https://" + e.addr + path + "?" + q.Encode()
}

func query(extra ...string) url.Values {
	q := url.Values{"bindingEpoch": {fmt.Sprint(epoch)}, "uid": {uid}}
	for i := 0; i < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

func (e *env) get(t *testing.T, q url.Values) *http.Response {
	t.Helper()
	resp, err := e.client.Get(e.url("/microvms/"+vm+"/log", q))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *env) post(t *testing.T, q url.Values, body string) *http.Response {
	t.Helper()
	resp, err := e.client.Post(e.url("/microvms/"+vm+"/exec", q), "application/json", strings.NewReader(body))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func stdout(e *env, data string) {
	e.mvmd.AppendOutput(machine, "entrypoint", v1alpha2.OutputStream_OUTPUT_STREAM_STDOUT, data)
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func reason(t *testing.T, resp *http.Response) api.Reason {
	t.Helper()
	var e api.APIError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	return e.Reason
}

// twice returns the query with key given twice.
func twice(key, value string) url.Values {
	q := query()
	q.Add(key, value)
	q.Add(key, value)
	return q
}

var fast = guest.Params{LogIdle: 50 * time.Millisecond}

func TestListener_RefusesAClientCertificateThatIsNotAnAPIServers(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "hello")
	e.mvmd.FinishExec(machine, "entrypoint", &v1alpha2.ExecRecord{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
	elsewhere := fakepki.New(t)
	other, _, _ := elsewhere.Issue(t, fakepki.Subject{CN: "apiserver", Org: []string{guest.APIServerOrganization}}, x509.ExtKeyUsageClientAuth)
	node, _, _ := e.ca.Issue(t, fakepki.Subject{CN: "node-2", Org: []string{api.NodeGroup}}, x509.ExtKeyUsageClientAuth)

	tests := []struct {
		name    string
		client  *http.Client
		allowed bool
	}{
		{"an API server's certificate from the cluster CA", e.client, true},
		{"a Node's certificate from the cluster CA", clientFor(e.ca, node), false},
		{"an API server's name from another CA", clientFor(e.ca, other), false},
		{"no certificate", clientFor(e.ca), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.client.Get(e.url("/microvms/"+vm+"/log", query()))
			if !tt.allowed {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "hello", body(t, resp))
		})
	}
}

func TestLog_AnswersNotRunningForAMachineItDoesNotHoldAtThatEpochAndUID(t *testing.T) {
	tests := []struct {
		name    string
		q       url.Values
		running bool
	}{
		{"the held epoch and uid of a Running machine", query(), true},
		{"another epoch", query("bindingEpoch", "2"), false},
		{"another uid", query("uid", "uid-2"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fast)
			stdout(e, "hello")
			resp := e.get(t, tt.q)
			if tt.running {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				return
			}
			assert.Equal(t, http.StatusConflict, resp.StatusCode)
			assert.Equal(t, api.ReasonNotRunning, reason(t, resp))
		})
	}
}

func TestLog_AnswersNotRunningForAMachineThatIsNotRunning(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.SetState(machine, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "")
	resp := e.get(t, query())
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, api.ReasonNotRunning, reason(t, resp))
}

func TestLog_AnswersNotRunningForAMachineTheRuntimeHoldsNoMore(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.SetState(machine, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "")
	_, err := e.mvmd.DeleteMachine(context.Background(), &v1alpha2.DeleteMachineRequest{MachineId: machine})
	require.NoError(t, err)
	resp := e.get(t, query())
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, api.ReasonNotRunning, reason(t, resp))
}

func TestLog_AnswersUnavailableWhenTheRuntimeCannotBeReached(t *testing.T) {
	e := newEnv(t, fast)
	require.NoError(t, e.conn.Close())
	resp := e.get(t, query())
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, api.ReasonUnavailable, reason(t, resp))
}

func TestLog_RefusesWhatItCannotRead(t *testing.T) {
	e := newEnv(t, fast)
	tests := []struct {
		name string
		q    url.Values
		ok   bool
	}{
		{"a query that is right", query("follow", "false", "tailBytes", "3"), true},
		{"an unknown parameter", query("since", "1"), false},
		{"a parameter given twice", twice("follow", "true"), false},
		{"follow that is not true or false", query("follow", "yes"), false},
		{"a tail of zero bytes", query("tailBytes", "0"), false},
		{"a tail that is not a number", query("tailBytes", "x"), false},
		{"an epoch that is not a number", query("bindingEpoch", "x"), false},
		{"no uid", url.Values{"bindingEpoch": {"3"}}, false},
	}
	stdout(e, "hello")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := e.get(t, tt.q)
			if tt.ok {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				return
			}
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
			assert.Equal(t, api.ReasonInvalid, reason(t, resp))
		})
	}
}

func TestLog_WithoutFollowEndsOnceTheOutputHeldIsSent(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "one ")
	stdout(e, "two")
	assert.Equal(t, "one two", body(t, e.get(t, query())))
}

func TestLog_WithoutFollowKeepsReadingWhileOutputComesInsideTheIdleInterval(t *testing.T) {
	e := newEnv(t, guest.Params{LogIdle: 200 * time.Millisecond})
	stdout(e, "a")
	go func() {
		for _, s := range []string{"b", "c", "d", "e"} {
			time.Sleep(100 * time.Millisecond)
			stdout(e, s)
		}
	}()
	assert.Equal(t, "abcde", body(t, e.get(t, query())))
}

func TestLog_WithTailBytesSendsTheLastBytesHeld(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "one ")
	stdout(e, "two three")
	assert.Equal(t, "three", body(t, e.get(t, query("tailBytes", "5"))))
	assert.Equal(t, "one two three", body(t, e.get(t, query("tailBytes", "100"))))
}

func TestLog_WithFollowKeepsSendingUntilTheProcessEnds(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "one ")
	resp := e.get(t, query("follow", "true"))
	r := bufio.NewReader(resp.Body)
	buf := make([]byte, 4)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err)
	assert.Equal(t, "one ", string(buf))

	// Idle for several times the idle interval: a read that follows is still open.
	time.Sleep(4 * fast.LogIdle)
	stdout(e, "two")
	_, err = io.ReadFull(r, buf[:3])
	require.NoError(t, err)
	assert.Equal(t, "two", string(buf[:3]))

	e.mvmd.FinishExec(machine, "entrypoint", &v1alpha2.ExecRecord{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Empty(t, rest)
}

func TestLog_WithFollowAndTailBytesSendsTheTailThenWhatComesAfter(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "abcdef")
	resp := e.get(t, query("follow", "true", "tailBytes", "3"))
	buf := make([]byte, 3)
	_, err := io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	assert.Equal(t, "def", string(buf))

	time.Sleep(4 * fast.LogIdle)
	stdout(e, "ghi")
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	assert.Equal(t, "ghi", string(buf))
}

func TestLog_CutsTheResponseWhenTheReadFailsAfterItBegan(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "hello")
	e.mvmd.FailReadsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	resp := e.get(t, query())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, err := io.ReadAll(resp.Body)
	assert.Error(t, err, "a clean end would tell the client it has the whole log")
}

func TestLog_ReadsOnFromTheEarliestOutputMvmdHolds(t *testing.T) {
	e := newEnv(t, fast)
	stdout(e, "hello")
	stdout(e, "world")
	e.mvmd.DropOutputBefore(machine, "entrypoint", 5)
	assert.Equal(t, "world", body(t, e.get(t, query())))
	assert.Equal(t, []uint64{0, 5}, e.mvmd.ReadOpens(machine, "entrypoint"))
}

type outputLine struct {
	Type       string          `json:"type"`
	Stream     string          `json:"stream"`
	Data       string          `json:"data"`
	Status     string          `json:"status"`
	ExitCode   *int32          `json:"exitCode"`
	TermSignal *int32          `json:"termSignal"`
	Reason     string          `json:"reason"`
	Object     json.RawMessage `json:"object"`
}

func lines(t *testing.T, resp *http.Response) []outputLine {
	t.Helper()
	var out []outputLine
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var l outputLine
		require.NoError(t, json.Unmarshal(sc.Bytes(), &l), sc.Text())
		out = append(out, l)
	}
	require.NoError(t, sc.Err())
	return out
}

const command = `{"command":["ls","-l"],"env":[{"name":"A","value":"1"}],"workingDir":"/srv"}`

func TestExec_RunsTheCommandOnceAndSendsItsOutputAndExit(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED, ExitCode: 3, Stdout: "out", Stderr: "err"})
	resp := e.post(t, query(), command)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))

	got := lines(t, resp)
	require.Len(t, got, 3)
	assert.Equal(t, outputLine{Type: "Output", Stream: "Stdout", Data: base64.StdEncoding.EncodeToString([]byte("out"))}, got[0])
	assert.Equal(t, outputLine{Type: "Output", Stream: "Stderr", Data: base64.StdEncoding.EncodeToString([]byte("err"))}, got[1])
	require.NotNil(t, got[2].ExitCode)
	assert.Equal(t, "Exit", got[2].Type)
	assert.Equal(t, "Exited", got[2].Status)
	assert.Equal(t, int32(3), *got[2].ExitCode)

	calls := e.mvmd.ExecRequests()
	require.Len(t, calls, 1)
	assert.Equal(t, machine, calls[0].GetMachineId())
	assert.Equal(t, []string{"ls", "-l"}, calls[0].GetCommand())
	assert.Equal(t, map[string]string{"A": "1"}, calls[0].GetEnv())
	assert.Equal(t, "/srv", calls[0].GetWorkingDir())
	assert.NotEmpty(t, calls[0].GetExecId())
	assert.NotEmpty(t, calls[0].GetRequestId())
}

func TestExec_UsesAFreshExecAndRequestIDForEachRequest(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
	for range 2 {
		lines(t, e.post(t, query(), command))
	}
	calls := e.mvmd.ExecRequests()
	require.Len(t, calls, 2)
	assert.NotEqual(t, calls[0].GetExecId(), calls[1].GetExecId())
	assert.NotEqual(t, calls[0].GetRequestId(), calls[1].GetRequestId())
}

func TestExec_EndsWithNotStartedWhenTheCommandDidNotStart(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_NOT_STARTED, Reason: "no such file"})
	got := lines(t, e.post(t, query(), command))
	require.Len(t, got, 1)
	assert.Equal(t, outputLine{Type: "Exit", Status: "NotStarted", Reason: "no such file"}, got[0])
}

func TestExec_EndsWithCutShortAndTheSignal(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_CUT_SHORT, Reason: "machine stopped"})
	got := lines(t, e.post(t, query(), command))
	require.Len(t, got, 1)
	assert.Equal(t, "CutShort", got[0].Status)
	assert.Nil(t, got[0].ExitCode)
}

func TestExec_WaitsForACommandThatRuns(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_STARTED, Stdout: "a"})
	resp := e.post(t, query(), command)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sc := bufio.NewScanner(resp.Body)
	require.True(t, sc.Scan())
	assert.Contains(t, sc.Text(), `"Output"`)

	time.Sleep(4 * fast.LogIdle)
	id := e.mvmd.ExecRequests()[0].GetExecId()
	e.mvmd.FinishExec(machine, id, &v1alpha2.ExecRecord{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
	require.True(t, sc.Scan())
	assert.Contains(t, sc.Text(), `"Exit"`)
}

func TestExec_AnswersNotRunningWithoutCallingTheRuntime(t *testing.T) {
	deleting := func(e *env) {
		m := e.holds[vm]
		now := time.Now()
		m.Metadata.DeletionTimestamp = &now
		e.holds[vm] = m
	}
	tests := []struct {
		name    string
		q       url.Values
		prepare func(*env)
		runs    bool
	}{
		{"a Running machine that is held", query(), func(*env) {}, true},
		{"another epoch", query("bindingEpoch", "2"), func(*env) {}, false},
		{"another uid", query("uid", "uid-2"), func(*env) {}, false},
		{"a MicroVM being deleted", query(), deleting, false},
		{"a machine that is stopped", query(), func(e *env) {
			e.mvmd.SetState(machine, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fast)
			e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
			tt.prepare(e)
			resp := e.post(t, tt.q, command)
			if tt.runs {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Len(t, e.mvmd.ExecRequests(), 1)
				return
			}
			assert.Equal(t, http.StatusConflict, resp.StatusCode)
			assert.Equal(t, api.ReasonNotRunning, reason(t, resp))
			assert.Empty(t, e.mvmd.ExecRequests())
		})
	}
}

func TestExec_RefusesARequestItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{"a request that is right", command, true},
		{"no command", `{"command":[]}`, false},
		{"an empty first element", `{"command":[""]}`, false},
		{"an env name that is not valid", `{"command":["ls"],"env":[{"name":"1=","value":"x"}]}`, false},
		{"an env name given twice", `{"command":["ls"],"env":[{"name":"A","value":"1"},{"name":"A","value":"2"}]}`, false},
		{"a field it does not know", `{"command":["ls"],"tty":true}`, false},
		{"a body that is not JSON", `ls`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fast)
			e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_EXITED})
			resp := e.post(t, query(), tt.body)
			if tt.ok {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				return
			}
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
			assert.Equal(t, api.ReasonInvalid, reason(t, resp))
			assert.Empty(t, e.mvmd.ExecRequests())
		})
	}
}

func TestExec_AnswersUnavailableAndDoesNotRepeatACallThatFailed(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.FailExecsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	resp := e.post(t, query(), command)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, api.ReasonUnavailable, reason(t, resp))
	assert.Len(t, e.mvmd.ExecRequests(), 1)
}

func TestExec_EndsWithAnErrorLineWhenTheReadFailsAfterItBegan(t *testing.T) {
	e := newEnv(t, fast)
	e.mvmd.PlanExec(fakemvmd.ExecPlan{Status: v1alpha2.ExecStatus_EXEC_STATUS_STARTED})
	e.mvmd.FailReadsWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	resp := e.post(t, query(), command)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := lines(t, resp)
	require.Len(t, got, 1)
	assert.Equal(t, "Error", got[0].Type)
	var apiErr api.APIError
	require.NoError(t, json.Unmarshal(got[0].Object, &apiErr))
	assert.Equal(t, api.ReasonUnavailable, apiErr.Reason)
	assert.Len(t, e.mvmd.ExecRequests(), 1)
}
