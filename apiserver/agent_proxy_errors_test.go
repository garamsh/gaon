package apiserver_test

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// closedPort returns a port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func TestAgentProxy_TriesTheNextAddressOfTheNodeWhenOneRefuses(t *testing.T) {
	f := newAgentFixture(t, true)
	certPEM, keyPEM := f.agentCert(t, "node-m1")
	a := startAgent(t, f.c, certPEM, keyPEM, answer("out\n"))
	// The agent listens on 127.0.0.1 only, so 127.0.0.2 refuses the connection.
	f.node(t, "node-m1", []string{"127.0.0.2"}, a.port)
	f.vm(t, "m1", "node-m1", 1)
	status, _, _ := f.as(t, adminID, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusServiceUnavailable, status, "control: the one address refuses")

	n, err := f.admin.GetNode(ctx(t), "node-m1")
	require.NoError(t, err)
	n.Status.Addresses = []string{"127.0.0.2", "127.0.0.1"}
	_, err = f.admin.PutNodeStatus(ctx(t), n)
	require.NoError(t, err)

	status, _, body := f.as(t, adminID, http.MethodGet, logPath, "")

	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "out\n", body)
}

func TestAgentProxy_VerifiesTheCertificateOfTheAgent(t *testing.T) {
	f := newAgentFixture(t, true)
	otherCA := newAuthority(t, "other ca")
	for i, tc := range []struct {
		name  string
		issue func(node string) (certPEM, keyPEM []byte)
		want  int
	}{
		{"chains to the cluster CA and has the CN of the node", func(node string) ([]byte, []byte) { return f.c.clientCA.issue(t, node) }, http.StatusOK},
		{"has the CN of another node", func(string) ([]byte, []byte) { return f.c.clientCA.issue(t, "node-other") }, http.StatusServiceUnavailable},
		{"is signed by another CA", func(node string) ([]byte, []byte) { return otherCA.issue(t, node) }, http.StatusServiceUnavailable},
		{"is valid for a client only", func(node string) ([]byte, []byte) { return f.c.clientCA.issueFor(t, who{cn: node}) }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "c" + strconv.Itoa(i)
			certPEM, keyPEM := tc.issue("node-" + name)
			a := startAgent(t, f.c, certPEM, keyPEM, answer("out\n"))
			f.node(t, "node-"+name, []string{"127.0.0.1"}, a.port)
			f.vm(t, name, "node-"+name, 1)

			status, _, body := f.as(t, adminID, http.MethodGet, "/microvms/"+name+"/log", "")

			assert.Equal(t, tc.want, status, body)
			if tc.want == http.StatusOK {
				assert.Equal(t, "out\n", body)
				return
			}
			assert.Equal(t, api.ReasonUnavailable, reasonOf(t, body))
			assert.Empty(t, a.calls(), "no request reached an agent that failed the check")
		})
	}
}

func TestAgentProxy_AnswersUnavailableWhenTheAgentCannotBeReached(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "ok", 1, answer("out\n"))
	f.node(t, "node-noaddr", nil, 1234)
	f.vm(t, "noaddr", "node-noaddr", 1)
	f.node(t, "node-noport", []string{"127.0.0.1"}, 0)
	f.vm(t, "noport", "node-noport", 1)
	f.node(t, "node-down", []string{"127.0.0.1"}, closedPort(t))
	f.vm(t, "down", "node-down", 1)

	status, _, body := f.as(t, adminID, http.MethodGet, "/microvms/ok/log", "")
	assert.Equal(t, http.StatusOK, status, "control: an agent that answers")
	assert.Equal(t, "out\n", body)
	for _, name := range []string{"noaddr", "noport", "down"} {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/microvms/" + name + "/log", ""},
			{http.MethodPost, "/microvms/" + name + "/exec", execBody},
		} {
			status, _, body := f.as(t, adminID, req.method, req.path, req.body)
			assert.Equal(t, http.StatusServiceUnavailable, status, name+" "+req.path)
			assert.Equal(t, api.ReasonUnavailable, reasonOf(t, body), name+" "+req.path)
			if name != "down" {
				assert.Contains(t, body, "no address or no agent port", "the server does not dial a Node it has no address of: "+name)
			}
		}
	}
}

func TestAgentProxy_AnswersUnavailableWithoutAClientCertificate(t *testing.T) {
	f := newAgentFixture(t, false)
	a, _ := f.served(t, "m1", 1, answer("out\n"))

	status, _, body := f.as(t, adminID, http.MethodGet, logPath, "")

	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, api.ReasonUnavailable, reasonOf(t, body))
	assert.Empty(t, a.calls())

	status, _, _ = f.as(t, adminID, http.MethodGet, "/microvms/m1", "")
	assert.Equal(t, http.StatusOK, status, "control: the rest of the API is served")
}

func TestAgentProxy_AnswersNotRunningAsTheAgentReports(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "m1", 1, refuse(api.ReasonNotRunning, "the microvm has no machine"))
	f.served(t, "m2", 1, answer("out\n"))
	f.served(t, "m3", 1, refuse(api.ReasonNotFound, "agent says something else"))

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, logPath, ""},
		{http.MethodPost, execPath, execBody},
	} {
		status, _, body := f.as(t, adminID, req.method, req.path, req.body)
		assert.Equal(t, http.StatusConflict, status, req.path)
		assert.Equal(t, api.ReasonNotRunning, reasonOf(t, body), req.path)
		assert.Contains(t, body, "the microvm has no machine", "the agent's message reaches the caller")
	}
	status, _, _ := f.as(t, adminID, http.MethodGet, "/microvms/m2/log", "")
	assert.Equal(t, http.StatusOK, status, "control: an agent that answers")
	status, _, body := f.as(t, adminID, http.MethodGet, "/microvms/m3/log", "")
	assert.Equal(t, http.StatusServiceUnavailable, status, "a reason the agent is not to give is Unavailable")
	assert.Equal(t, api.ReasonUnavailable, reasonOf(t, body))
}

func TestAgentProxy_AnswersNotRunningForAMicroVMWithNoMachineToReach(t *testing.T) {
	f := newAgentFixture(t, true)
	a, m := f.served(t, "m1", 1, answer("out\n"))
	f.vm(t, "unbound", "", 0)
	m.Metadata.Finalizers = []string{"hold"}
	m, err := f.admin.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	_, err = f.admin.DeleteMicroVM(ctx(t), "m1", m.Metadata.ResourceVersion)
	require.NoError(t, err)

	status, _, body := f.as(t, adminID, http.MethodGet, "/microvms/unbound/log", "")
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, api.ReasonNotRunning, reasonOf(t, body))
	status, _, body = f.as(t, adminID, http.MethodPost, "/microvms/unbound/exec", execBody)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, api.ReasonNotRunning, reasonOf(t, body))

	status, _, body = f.as(t, adminID, http.MethodPost, execPath, execBody)
	assert.Equal(t, http.StatusConflict, status, "an exec into a microvm being deleted")
	assert.Equal(t, api.ReasonNotRunning, reasonOf(t, body))
	assert.Empty(t, a.calls(), "the agent was not asked")
	status, _, body = f.as(t, adminID, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusOK, status, "control: the log of a microvm being deleted is still served")
	assert.Equal(t, "out\n", body)
}

func TestAgentProxy_AnswersNotFoundForAMicroVMThatDoesNotExist(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "m1", 1, answer("out\n"))

	status, _, body := f.as(t, adminID, http.MethodGet, "/microvms/nobody/log", "")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, api.ReasonNotFound, reasonOf(t, body))
	status, _, _ = f.as(t, adminID, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusOK, status, "control: the microvm that exists")
}

func TestAgentProxy_AbortsAStreamTheAgentCutsAfterTheResponseBegan(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "cut", 1, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	f.served(t, "whole", 1, answer("complete"))

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/microvms/cut/log", ""},
		{http.MethodPost, "/microvms/cut/exec", execBody},
	} {
		resp := f.open(t, adminID, req.method, req.path, req.body)
		_, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the response had begun")
		assert.Error(t, err, "the client sees a transport error, not a complete body: "+req.path)
	}

	resp := f.open(t, adminID, http.MethodGet, "/microvms/whole/log", "")
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.NoError(t, err, "control: a stream the agent ends cleanly ends cleanly")
	assert.Equal(t, "complete", string(out))
}

func TestAgentProxy_RefusesAnInvalidLogQueryWithoutCallingTheAgent(t *testing.T) {
	f := newAgentFixture(t, true)
	a, _ := f.served(t, "m1", 1, answer("out\n"))

	for _, q := range []string{
		"?tailBytes=0", "?tailBytes=-1", "?tailBytes=abc", "?tailBytes=1.5", "?tailBytes=", "?tailBytes=99999999999999999999",
		"?follow=maybe", "?follow=", "?follow=true&follow=false", "?colour=red",
	} {
		status, _, body := f.as(t, adminID, http.MethodGet, logPath+q, "")
		assert.Equal(t, http.StatusUnprocessableEntity, status, q)
		assert.Equal(t, api.ReasonInvalid, reasonOf(t, body), q)
	}
	assert.Empty(t, a.calls(), "no invalid request reached the agent")

	for _, q := range []string{"", "?tailBytes=1", "?follow=false&tailBytes=4096", "?follow=true"} {
		status, _, _ := f.as(t, adminID, http.MethodGet, logPath+q, "")
		assert.Equal(t, http.StatusOK, status, "control: "+q)
	}
	assert.Len(t, a.calls(), 4)
}

func TestAgentProxy_RefusesAnInvalidExecBodyWithoutCallingTheAgent(t *testing.T) {
	f := newAgentFixture(t, true)
	a, _ := f.served(t, "m1", 1, answer("out\n"))

	for name, body := range map[string]string{
		"no command":         `{}`,
		"empty command":      `{"command":[]}`,
		"empty program":      `{"command":[""]}`,
		"env name, a digit":  `{"command":["a"],"env":[{"name":"1A","value":"x"}]}`,
		"env name, empty":    `{"command":["a"],"env":[{"name":"","value":"x"}]}`,
		"env name, an equal": `{"command":["a"],"env":[{"name":"A=B","value":"x"}]}`,
		"env name twice":     `{"command":["a"],"env":[{"name":"A"},{"name":"A"}]}`,
		"unknown field":      `{"command":["a"],"tty":true}`,
		"command, a string":  `{"command":"ls"}`,
		"not JSON":           `ls`,
		"two values":         `{"command":["a"]} {"command":["b"]}`,
	} {
		status, _, out := f.as(t, adminID, http.MethodPost, execPath, body)
		assert.Equal(t, http.StatusUnprocessableEntity, status, name+": "+out)
		assert.Equal(t, api.ReasonInvalid, reasonOf(t, out), name)
	}
	assert.Empty(t, a.calls(), "no invalid request reached the agent")

	for name, body := range map[string]string{
		"a command":           `{"command":["ls","-l"]}`,
		"env and working dir": `{"command":["ls"],"env":[{"name":"_a-b.C1","value":""},{"name":"B"}],"workingDir":"/"}`,
	} {
		status, _, out := f.as(t, adminID, http.MethodPost, execPath, body)
		assert.Equal(t, http.StatusOK, status, "control: "+name+": "+out)
	}
	assert.Len(t, a.calls(), 2)
}
