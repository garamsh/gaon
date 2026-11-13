package apiserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/lib/api"
)

const (
	// agentDialTimeout bounds the connection and the handshake to one address.
	agentDialTimeout = 5 * time.Second
	// agentHeaderTimeout bounds the wait for the agent's response header.
	agentHeaderTimeout = 30 * time.Second
	// maxAgentError bounds how much of an agent's error body is read.
	maxAgentError = 1 << 16
)

// agentClient calls the Node Agents with the server's client certificate and
// trusts the cluster CA to have signed theirs.
type agentClient struct {
	cert  tls.Certificate
	roots *x509.CertPool
}

// agentTarget is where the agent of a MicroVM's Node listens: the Node's name,
// its addresses in order and its port.
type agentTarget struct {
	node  string
	addrs []string
	port  int
}

// dial connects to the first address of t that answers and whose certificate
// chains to the cluster CA and has the CN of the Node. The certificate has no
// host name or address to match, so the chain and the CN are checked instead.
func (c *agentClient) dial(ctx context.Context, t agentTarget) (net.Conn, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{c.cert},
		InsecureSkipVerify: true, // the VerifyConnection below verifies
		VerifyConnection:   func(cs tls.ConnectionState) error { return c.verify(cs, t.node) },
	}
	var errs []error
	for _, addr := range t.addrs {
		attempt, cancel := context.WithTimeout(ctx, agentDialTimeout)
		d := tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
		conn, err := d.DialContext(attempt, "tcp", net.JoinHostPort(addr, strconv.Itoa(t.port)))
		cancel()
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (c *agentClient) verify(cs tls.ConnectionState, node string) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("the agent presented no certificate")
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, cert := range cs.PeerCertificates[1:] {
		inter.AddCert(cert)
	}
	opts := x509.VerifyOptions{Roots: c.roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("verify the agent's certificate: %w", err)
	}
	// Verify accepts a leaf with no extended key usage for any use, and ADR 0043
	// has the agent's certificate carry serverAuth.
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return errors.New("the agent's certificate does not carry the extended key usage serverAuth")
	}
	if leaf.Subject.CommonName != node {
		return fmt.Errorf("the agent's certificate is for %q, want node %q", leaf.Subject.CommonName, node)
	}
	return nil
}

// do sends one request to the agent at t and never repeats it, because an exec
// that may have started must not run twice. The caller closes the body.
func (c *agentClient) do(ctx context.Context, t agentTarget, method, path string, query url.Values, body []byte) (*http.Response, error) {
	tr := &http.Transport{
		DialTLSContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return c.dial(ctx, t) },
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: agentHeaderTimeout,
	}
	u := url.URL{Scheme: "https", Host: t.node, Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return tr.RoundTrip(req)
}

// microVMLog serves GET /microvms/{name}/log.
func (h *handler) microVMLog(svc resourceapi.Service, agents *agentClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// URL.Query drops a pair it cannot parse, so the raw query is parsed here.
		parsed, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			h.fail(w, r, api.NewAPIError(api.ReasonInvalid, "the query is malformed: %v", err))
			return
		}
		q, err := logQuery(parsed)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.proxy(w, r, svc, agents, proxied{method: http.MethodGet, sub: "log", contentType: "application/octet-stream", query: q})
	}
}

// execRequest is the body of POST /microvms/{name}/exec.
type execRequest struct {
	Command    []string  `json:"command"`
	Env        []execEnv `json:"env,omitempty"`
	WorkingDir string    `json:"workingDir,omitempty"`
}

type execEnv struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// microVMExec serves POST /microvms/{name}/exec.
func (h *handler) microVMExec(svc resourceapi.Service, agents *agentClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in execRequest
		if !h.decode(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			h.fail(w, r, err)
			return
		}
		body, err := json.Marshal(in)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.proxy(w, r, svc, agents, proxied{method: http.MethodPost, sub: "exec", contentType: "application/x-ndjson", query: url.Values{}, body: body, exec: true})
	}
}

func (e execRequest) validate() error {
	if len(e.Command) == 0 || e.Command[0] == "" {
		return api.NewAPIError(api.ReasonInvalid, "command is empty, want the program to run and its arguments")
	}
	seen := map[string]bool{}
	for i, v := range e.Env {
		if !api.ValidEnvName(v.Name) {
			return api.NewAPIError(api.ReasonInvalid, "env[%d].name is %q, want letters, digits, '-', '_' or '.', not starting with a digit", i, v.Name)
		}
		if seen[v.Name] {
			return api.NewAPIError(api.ReasonInvalid, "env[%d].name %q is used twice", i, v.Name)
		}
		seen[v.Name] = true
	}
	return nil
}

// logQuery returns the query the agent is sent for the log's own parameters,
// and refuses any other parameter and any repeated one.
func logQuery(in url.Values) (url.Values, error) {
	out := url.Values{}
	for name, values := range in {
		if name != "follow" && name != "tailBytes" {
			return nil, api.NewAPIError(api.ReasonInvalid, "the query parameter %q is not one of follow and tailBytes", name)
		}
		if len(values) != 1 {
			return nil, api.NewAPIError(api.ReasonInvalid, "the query parameter %q is given %d times, want once", name, len(values))
		}
	}
	switch v := in.Get("follow"); v {
	case "":
		if in.Has("follow") {
			return nil, api.NewAPIError(api.ReasonInvalid, "follow is empty, want true or false")
		}
	case "true", "false":
		out.Set("follow", v)
	default:
		return nil, api.NewAPIError(api.ReasonInvalid, "follow is %q, want true or false", v)
	}
	if in.Has("tailBytes") {
		n, err := strconv.ParseInt(in.Get("tailBytes"), 10, 64)
		if err != nil || n < 1 {
			return nil, api.NewAPIError(api.ReasonInvalid, "tailBytes is %q, want an integer of at least 1 that fits in 64 bits", in.Get("tailBytes"))
		}
		out.Set("tailBytes", strconv.FormatInt(n, 10))
	}
	return out, nil
}

// proxied is what differs between the two routes: the call made to the agent
// and the type of what is answered.
type proxied struct {
	method, sub, contentType string
	query                    url.Values
	body                     []byte
	exec                     bool
}

// proxy finds the agent of the Node the MicroVM is bound to, makes the call
// of p to it and streams what it answers.
func (h *handler) proxy(w http.ResponseWriter, r *http.Request, svc resourceapi.Service, agents *agentClient, p proxied) {
	name := r.PathValue("name")
	m, err := svc.GetMicroVM(r.Context(), name)
	if err == nil {
		var t agentTarget
		if t, err = agentOf(r.Context(), svc, m, p.exec); err == nil {
			err = h.callAgent(w, r, agents, t, m, name, p)
		}
	}
	if err != nil {
		h.fail(w, r, err)
	}
}

// agentOf finds the agent of m's Node. A MicroVM with no machine to reach is
// NotRunning, and an agent that cannot be addressed is Unavailable.
func agentOf(ctx context.Context, svc resourceapi.Service, m *api.MicroVM, exec bool) (agentTarget, error) {
	if exec && m.Metadata.DeletionTimestamp != nil {
		return agentTarget{}, api.NewAPIError(api.ReasonNotRunning, "the microvm %q is being deleted", m.Metadata.Name)
	}
	if m.Spec.NodeName == "" {
		return agentTarget{}, api.NewAPIError(api.ReasonNotRunning, "the microvm %q is bound to no node", m.Metadata.Name)
	}
	n, err := svc.GetNode(ctx, m.Spec.NodeName)
	if errors.Is(err, api.ErrNotFound) {
		return agentTarget{}, api.NewAPIError(api.ReasonUnavailable, "node %q does not exist", m.Spec.NodeName)
	}
	if err != nil {
		return agentTarget{}, err
	}
	if len(n.Status.Addresses) == 0 || n.Status.AgentPort == 0 {
		return agentTarget{}, api.NewAPIError(api.ReasonUnavailable, "node %q has no address or no agent port", n.Metadata.Name)
	}
	return agentTarget{node: n.Metadata.Name, addrs: n.Status.Addresses, port: n.Status.AgentPort}, nil
}

// callAgent makes the call and, once the agent answers 200, streams the body.
// It returns an error only before the response has begun.
func (h *handler) callAgent(w http.ResponseWriter, r *http.Request, agents *agentClient, t agentTarget, m *api.MicroVM, name string, p proxied) error {
	if agents == nil {
		return api.NewAPIError(api.ReasonUnavailable, "the server has no client certificate for the agents")
	}
	q := p.query
	q.Set("bindingEpoch", strconv.FormatInt(int64(m.Spec.BindingEpoch), 10))
	q.Set("uid", string(m.Metadata.UID))
	resp, err := agents.do(r.Context(), t, p.method, "/microvms/"+name+"/"+p.sub, q, p.body)
	if err != nil {
		// The cause names an address and a certificate, which the caller may not read.
		h.log.Warn("call the agent failed", "trace_id", traceID(r), "node", t.node, "error", err)
		return api.NewAPIError(api.ReasonUnavailable, "the agent of node %q cannot be reached", t.node)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentError(t.node, resp)
	}
	w.Header().Set("Content-Type", p.contentType)
	w.WriteHeader(http.StatusOK)
	h.stream(w, r, resp.Body)
	return nil
}

// agentError is the answer to an agent that did not answer 200: the reason it
// gave where that is NotRunning or Unavailable, and Unavailable for anything
// else, which the agent is not to answer to a request the server validated.
func agentError(node string, resp *http.Response) error {
	var e api.APIError
	err := json.NewDecoder(io.LimitReader(resp.Body, maxAgentError)).Decode(&e)
	if err == nil && (e.Reason == api.ReasonNotRunning || e.Reason == api.ReasonUnavailable) {
		return api.NewAPIError(e.Reason, "%s", e.Message)
	}
	return api.NewAPIError(api.ReasonUnavailable, "the agent of node %q answered %d", node, resp.StatusCode)
}

// stream copies body to the client as it arrives. A body that fails before its
// end aborts the response, so that the client sees a transport error and not a
// complete one: the stream has no framing that would tell a short read from the
// whole. A client that went away ends the copy quietly.
func (h *handler) stream(w http.ResponseWriter, r *http.Request, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	flush(flusher) // the status and headers go out before the first byte of output
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flush(flusher)
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			if r.Context().Err() == nil {
				h.log.Warn("agent stream failed", "trace_id", traceID(r), "path", r.URL.Path, "error", err)
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}
