package apiserver_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// apiserversID is the certificate the API server calls the agents with
// (deployment.md §Certificates).
var apiserversID = who{"gaon-apiserver", []string{"gaon:apiservers"}}

// agentCall is what the fake agent saw of one request.
type agentCall struct {
	method, path, body string
	query              url.Values
	peerOrgs           []string
}

// fakeAgent is an in-process stand-in for a Node Agent: a TLS 1.3 server on an
// ephemeral port that requires a client certificate signed by the client CA
// and of the Organization gaon:apiservers, and records what it is asked.
type fakeAgent struct {
	port int
	mu   sync.Mutex
	got  []agentCall
}

func (a *fakeAgent) calls() []agentCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agentCall(nil), a.got...)
}

// startAgent serves handle with the serving certificate certPEM and keyPEM.
func startAgent(t *testing.T, c *cluster, certPEM, keyPEM []byte, handle http.HandlerFunc, tune ...func(*tls.Config)) *fakeAgent {
	t.Helper()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(c.clientCA.cert)
	a := &fakeAgent{}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	for _, f := range tune {
		f(tlsCfg)
	}
	srv := &http.Server{
		TLSConfig: tlsCfg,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			call := agentCall{method: r.Method, path: r.URL.Path, query: r.URL.Query(), body: string(body)}
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				call.peerOrgs = r.TLS.PeerCertificates[0].Subject.Organization
			}
			a.mu.Lock()
			a.got = append(a.got, call)
			a.mu.Unlock()
			if len(call.peerOrgs) != 1 || call.peerOrgs[0] != "gaon:apiservers" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			handle(w, r)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a.port = ln.Addr().(*net.TCPAddr).Port
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return a
}

// answer is a handler that streams text and ends the stream cleanly.
func answer(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, text)
	}
}

// refuse is a handler that answers with the API error of reason.
func refuse(reason api.Reason, message string) http.HandlerFunc {
	e := api.NewAPIError(reason, "%s", message)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(e.Code)
		_ = json.NewEncoder(w).Encode(e)
	}
}

// agentFixture is an RBAC server whose agent client certificate is the one of
// apiserversID, with an admin to set it up.
type agentFixture struct {
	c           *cluster
	addr        string
	admin, rbac *api.Client
}

func newAgentFixture(t *testing.T, withAgentCert bool) *agentFixture {
	t.Helper()
	c := newCluster(t)
	if withAgentCert {
		certPEM, keyPEM := c.clientCA.issueFor(t, apiserversID)
		c.cfg.AgentCert = writeFile(t, "agent-client.pem", certPEM)
		c.cfg.AgentKey = writeFile(t, "agent-client-key.pem", keyPEM)
	}
	addr, _ := c.start(t, apiserver.ModeRBAC)
	f := &agentFixture{c: c, addr: addr}
	f.admin, f.rbac = c.clients(t, addr, adminID)
	return f
}

// agentCert is the serving certificate of an agent for the Node cn, signed by
// the client CA.
func (f *agentFixture) agentCert(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	return f.c.clientCA.issue(t, cn)
}

// node creates the Node name with the addresses and agent port given.
func (f *agentFixture) node(t *testing.T, name string, addrs []string, port int) {
	t.Helper()
	admitted := node(name)
	admitted.Spec.Admitted = true
	_, err := f.admin.PutNode(ctx(t), admitted)
	require.NoError(t, err)
	n, err := f.admin.GetNode(ctx(t), name)
	require.NoError(t, err)
	n.Status.State = api.NodeStateAdmitted
	n.Status.Addresses = addrs
	n.Status.AgentPort = port
	_, err = f.admin.PutNodeStatus(ctx(t), n)
	require.NoError(t, err)
}

// vm creates the MicroVM name bound to nodeName at epoch and returns it.
func (f *agentFixture) vm(t *testing.T, name, nodeName string, epoch api.Epoch) *api.MicroVM {
	t.Helper()
	m, err := f.admin.PutMicroVM(ctx(t), microvm(name))
	require.NoError(t, err)
	if nodeName == "" {
		return m
	}
	m, err = f.admin.BindMicroVM(ctx(t), name, api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: nodeName, BindingEpoch: epoch})
	require.NoError(t, err)
	return m
}

// served creates a Node served by a fake agent with handle, and the MicroVM
// name bound to it at epoch.
func (f *agentFixture) served(t *testing.T, name string, epoch api.Epoch, handle http.HandlerFunc) (*fakeAgent, *api.MicroVM) {
	t.Helper()
	certPEM, keyPEM := f.agentCert(t, "node-"+name)
	a := startAgent(t, f.c, certPEM, keyPEM, handle)
	f.node(t, "node-"+name, []string{"127.0.0.1"}, a.port)
	return a, f.vm(t, name, "node-"+name, epoch)
}

// as makes a raw request as w to path under the API root and returns the
// status, the content type and the whole body.
func (f *agentFixture) as(t *testing.T, w who, method, path, body string) (int, string, string) {
	t.Helper()
	resp := f.open(t, w, method, path, body)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(out)
}

// open makes the request and returns the response with its body unread.
func (f *agentFixture) open(t *testing.T, w who, method, path, body string) *http.Response {
	t.Helper()
	certPEM, keyPEM := f.c.clientCA.issueFor(t, w)
	hc, err := api.NewTLSClient(f.c.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	hc.Timeout = 10 * time.Second
	req, err := http.NewRequestWithContext(ctx(t), method, "https://"+f.addr+apiRoot+path, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := hc.Do(req)
	require.NoError(t, err)
	return resp
}

// reasonOf is the reason of an API error body.
func reasonOf(t *testing.T, body string) api.Reason {
	t.Helper()
	var e api.APIError
	require.NoError(t, json.Unmarshal([]byte(body), &e), body)
	return e.Reason
}

const (
	execBody = `{"command":["echo","hi"]}`
	logPath  = "/microvms/m1/log"
	execPath = "/microvms/m1/exec"
)

func TestAgentProxy_LogAndExecAreAuthorizedApartFromGetOnMicroVMs(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "m1", 1, answer("out\n"))
	reader, logger, runner := who{"reader", []string{"gaon:testers"}}, who{"logger", []string{"gaon:testers"}}, who{"runner", []string{"gaon:testers"}}
	for _, o := range []struct {
		name string
		who  who
		rule api.PolicyRule
	}{
		{"reader", reader, rule(api.Group, "microvms", api.VerbGet)},
		{"logger", logger, rule(api.Group, "microvms/log", api.VerbGet)},
		{"runner", runner, rule(api.Group, "microvms/exec", api.VerbCreate)},
	} {
		_, err := f.rbac.PutClusterRole(ctx(t), clusterRole(o.name, o.rule))
		require.NoError(t, err)
		_, err = f.rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding(o.name, o.name, userSubject(o.who.cn)))
		require.NoError(t, err)
	}

	status, _, _ := f.as(t, reader, http.MethodGet, "/microvms/m1", "")
	assert.Equal(t, http.StatusOK, status, "control: get on microvms is what the reader has")
	status, _, body := f.as(t, reader, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusForbidden, status, body)
	status, _, body = f.as(t, reader, http.MethodPost, execPath, execBody)
	assert.Equal(t, http.StatusForbidden, status, body)

	status, _, body = f.as(t, logger, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusOK, status, "control: get on microvms/log allows the log")
	assert.Equal(t, "out\n", body)
	status, _, _ = f.as(t, logger, http.MethodPost, execPath, execBody)
	assert.Equal(t, http.StatusForbidden, status, "get on microvms/log does not allow an exec")
	status, _, _ = f.as(t, logger, http.MethodGet, "/microvms/m1", "")
	assert.Equal(t, http.StatusForbidden, status, "nor does it allow get on microvms")

	status, _, body = f.as(t, runner, http.MethodPost, execPath, execBody)
	assert.Equal(t, http.StatusOK, status, "control: create on microvms/exec allows the exec")
	assert.Equal(t, "out\n", body)
	status, _, _ = f.as(t, runner, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusForbidden, status, "create on microvms/exec does not allow the log")
}

func TestAgentProxy_ANodeCertificateIsNotAllowedTheLogOrExec(t *testing.T) {
	f := newAgentFixture(t, true)
	f.served(t, "m1", 1, answer("out\n"))

	status, _, _ := f.as(t, nodeID("node-m1"), http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusForbidden, status)
	status, _, _ = f.as(t, nodeID("node-m1"), http.MethodPost, execPath, execBody)
	assert.Equal(t, http.StatusForbidden, status)
	status, _, _ = f.as(t, adminID, http.MethodGet, logPath, "")
	assert.Equal(t, http.StatusOK, status, "control: an admin is allowed the log")
}

func TestAgentProxy_CallsTheAgentWithTheBindingEpochAndUID(t *testing.T) {
	f := newAgentFixture(t, true)
	a, m := f.served(t, "m1", 1, answer("line 1\nline 2\n"))

	status, contentType, body := f.as(t, adminID, http.MethodGet, logPath+"?follow=true&tailBytes=10", "")

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "application/octet-stream", contentType)
	assert.Equal(t, "line 1\nline 2\n", body)
	require.Len(t, a.calls(), 1)
	got := a.calls()[0]
	assert.Equal(t, http.MethodGet, got.method)
	assert.Equal(t, "/microvms/m1/log", got.path)
	assert.Equal(t, url.Values{"follow": {"true"}, "tailBytes": {"10"}, "bindingEpoch": {"1"}, "uid": {string(m.Metadata.UID)}}, got.query)
	assert.Equal(t, []string{"gaon:apiservers"}, got.peerOrgs, "the agent was called with the apiservers certificate")

	status, contentType, body = f.as(t, adminID, http.MethodPost, execPath, `{"command":["sh","-c","ls"],"env":[{"name":"A","value":"1"}],"workingDir":"/tmp"}`)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "application/x-ndjson", contentType)
	assert.Equal(t, "line 1\nline 2\n", body)
	require.Len(t, a.calls(), 2)
	got = a.calls()[1]
	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/microvms/m1/exec", got.path)
	assert.Equal(t, url.Values{"bindingEpoch": {"1"}, "uid": {string(m.Metadata.UID)}}, got.query)
	assert.JSONEq(t, `{"command":["sh","-c","ls"],"env":[{"name":"A","value":"1"}],"workingDir":"/tmp"}`, got.body)
}
