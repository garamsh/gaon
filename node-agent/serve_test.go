package nodeagent_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// syncBuffer is a log destination the agent and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func startAgentLogging(t *testing.T, cfg nodeagent.Config, clk clock.Clock) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- nodeagent.Run(ctx, cfg, clk, slog.New(slog.NewTextHandler(logs, nil))) }()
	t.Cleanup(func() {
		cancel()
		err := <-done
		if t.Failed() {
			t.Logf("the agent's Run returned: %v\nthe agent's log:\n%s", err, logs.String())
		}
	})
	return logs
}

// apiServerClient is a client the way the API server is: a certificate of
// gaon:apiservers from the cluster CA. It checks the agent's certificate
// against the CA for server auth, as a client does, but not against the
// address: the certificates the fake issues carry no address.
func apiServerClient(t *testing.T, gaon *fakegaon.Server) *http.Client {
	t.Helper()
	now := time.Now()
	certPEM, keyPEM := gaon.IssueFor(t, pkix.Name{CommonName: "apiserver", Organization: []string{"gaon:apiservers"}},
		now.Add(-time.Minute), now.Add(time.Hour), x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	caPEM, err := os.ReadFile(gaon.CAFile())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, InsecureSkipVerify: true, //nolint:gosec // verified below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}}}
}

// awaitStatusWritten waits for the agent's first write of the Node's status,
// the write that carries the addresses and the port when it writes them: the
// Ready condition is set by that write and by no other.
func awaitStatusWritten(t *testing.T, gaon *fakegaon.Server) {
	t.Helper()
	require.Eventually(t, func() bool {
		n, ok := gaon.Node("n1")
		if !ok {
			return false
		}
		for _, c := range n.Status.Conditions {
			if c.Type == api.NodeReady && c.Status == api.ConditionTrue {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the agent wrote the Node's status")
}

func boundMicroVM() api.MicroVM {
	m := api.MicroVM{}
	m.Metadata.Name, m.Metadata.UID = "m1", "uid-1"
	m.Spec = api.MicroVMSpec{
		Image:     "registry.example/app@sha256:" + strings.Repeat("b", 64),
		Resources: api.ResourceRequirements{Limits: api.ResourceList{CPU: "1", Memory: "512Mi"}}, RunStrategy: api.RunAlways,
		NodeName: "n1", BindingEpoch: 1,
	}
	return m
}

func logURL(port int) string {
	return fmt.Sprintf("https://127.0.0.1:%d/microvms/m1/log?bindingEpoch=1&uid=uid-1", port)
}

func TestRun_ServesAMicroVMsLogOverTheCertificateItBootstrappedAndWritesWhereToReachIt(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	gaon.AcceptToken(bootstrapToken)
	port := freePort(t)
	args := []string{
		"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", filepath.Join(t.TempDir(), "certs"),
		"--bootstrap-token-file", writeFile(t, "token", bootstrapToken),
		"--agent-addresses", "127.0.0.1", "--agent-port", strconv.Itoa(port), "--resync-period", "20ms",
	}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	startAgentLogging(t, cfg, clk)
	gaon.SetMicroVM(boundMicroVM())

	require.Eventually(t, func() bool {
		n, ok := gaon.Node("n1")
		return ok && n.Status.AgentPort == port
	}, 5*time.Second, 10*time.Millisecond, "the Node's status tells where to reach the agent")
	n, _ := gaon.Node("n1")
	assert.Equal(t, []string{"127.0.0.1"}, n.Status.Addresses)
	require.Eventually(t, func() bool { return gaon.MicroVM("m1").Status.Phase == api.MicroVMRunning }, 5*time.Second, 10*time.Millisecond)
	mvmd.AppendOutput("m1-e1", "entrypoint", v1alpha2.OutputStream_OUTPUT_STREAM_STDOUT, "hello\n")

	resp, err := apiServerClient(t, gaon).Get(logURL(port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "hello\n", string(body))

	other, err := apiServerClient(t, gaon).Get(strings.Replace(logURL(port), "uid-1", "uid-2", 1))
	require.NoError(t, err)
	defer func() { _ = other.Body.Close() }()
	assert.Equal(t, http.StatusConflict, other.StatusCode, "another uid is NotRunning")
}

func operatorFiles(t *testing.T, gaon *fakegaon.Server, usages ...x509.ExtKeyUsage) (certFile, keyFile string) {
	t.Helper()
	now := time.Now()
	certPEM, keyPEM := gaon.IssueFor(t, pkix.Name{CommonName: "n1"}, now.Add(-time.Minute), now.Add(time.Hour), usages...)
	return writeFile(t, "tls.crt", string(certPEM)), writeFile(t, "tls.key", string(keyPEM))
}

func operatorConfig(t *testing.T, gaon *fakegaon.Server, mvmd *fakemvmd.Server, certFile, keyFile string, port int) nodeagent.Config {
	t.Helper()
	args := []string{
		"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--tls-cert", certFile, "--tls-key", keyFile,
		"--agent-addresses", "127.0.0.1", "--agent-port", strconv.Itoa(port), "--resync-period", "20ms",
	}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	return cfg
}

func TestRun_ServesWithAnOperatorCertificateOnlyWhenItCarriesServerAuth(t *testing.T) {
	clk := clock.System{}
	t.Run("with server auth the agent serves and writes the port", func(t *testing.T) {
		gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
		port := freePort(t)
		certFile, keyFile := operatorFiles(t, gaon, x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth)
		logs := startAgentLogging(t, operatorConfig(t, gaon, mvmd, certFile, keyFile, port), clk)
		gaon.SetMicroVM(boundMicroVM())

		require.Eventually(t, func() bool {
			n, ok := gaon.Node("n1")
			return ok && n.Status.AgentPort == port
		}, 5*time.Second, 10*time.Millisecond)
		require.Eventually(t, func() bool { return gaon.MicroVM("m1").Status.Phase == api.MicroVMRunning }, 5*time.Second, 10*time.Millisecond)
		mvmd.AppendOutput("m1-e1", "entrypoint", v1alpha2.OutputStream_OUTPUT_STREAM_STDOUT, "hello\n")
		resp, err := apiServerClient(t, gaon).Get(logURL(port))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotContains(t, logs.String(), "does not serve")
	})
	t.Run("without server auth the agent does not serve, says why and writes no port", func(t *testing.T) {
		gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
		port := freePort(t)
		certFile, keyFile := operatorFiles(t, gaon, x509.ExtKeyUsageClientAuth)
		logs := startAgentLogging(t, operatorConfig(t, gaon, mvmd, certFile, keyFile, port), clk)

		awaitStatusWritten(t, gaon)
		require.Eventually(t, func() bool { return strings.Contains(logs.String(), "does not serve") }, 5*time.Second, 10*time.Millisecond)
		assert.Contains(t, logs.String(), "server auth")
		n, _ := gaon.Node("n1")
		assert.Zero(t, n.Status.AgentPort, "a port nothing listens on is not written")
		assert.Equal(t, []string{"127.0.0.1"}, n.Status.Addresses, "the addresses are")
		_, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // only to see whether anything listens
		var op *net.OpError
		assert.True(t, errors.As(err, &op), "nothing listens on the port: %v", err)
	})
}

func TestRun_StopsTheStartWhenTheAgentsPortIsTaken(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	certFile, keyFile := operatorFiles(t, gaon, x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth)
	cfg := operatorConfig(t, gaon, mvmd, certFile, keyFile, taken.Addr().(*net.TCPAddr).Port)

	err = nodeagent.Run(context.Background(), cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen")
}

func TestRun_WithoutAnAddressTheAgentDoesNotServeAndSaysNothingOfIt(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	certFile, keyFile := operatorFiles(t, gaon, x509.ExtKeyUsageClientAuth)
	args := []string{"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--tls-cert", certFile, "--tls-key", keyFile}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	logs := startAgentLogging(t, cfg, clk)

	awaitStatusWritten(t, gaon)
	n, _ := gaon.Node("n1")
	assert.Zero(t, n.Status.AgentPort)
	assert.Empty(t, n.Status.Addresses)
	assert.NotContains(t, logs.String(), "does not serve")
}

// A Node bootstrapped before the agent asked for server auth holds a pair that
// cannot serve: the agent that serves renews it at start.
func TestRun_RenewsAtStartAHeldPairWithoutServerAuthAndThenServes(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	dir := filepath.Join(t.TempDir(), "certs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	now := time.Now()
	certPEM, keyPEM := gaon.Issue(t, "n1", now.Add(-time.Minute), now.Add(24*time.Hour))
	require.NoError(t, os.WriteFile(filepath.Join(dir, certificate.PairFile), append(certPEM, keyPEM...), 0o600))
	port := freePort(t)
	args := []string{
		"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", dir,
		"--agent-addresses", "127.0.0.1", "--agent-port", strconv.Itoa(port), "--resync-period", "20ms",
	}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	startAgentLogging(t, cfg, clk)
	gaon.SetMicroVM(boundMicroVM())

	require.Eventually(t, func() bool { return len(gaon.CSRCreates()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the pair is a day old at most, and it is renewed at once")
	require.Eventually(t, func() bool { return gaon.MicroVM("m1").Status.Phase == api.MicroVMRunning }, 5*time.Second, 10*time.Millisecond)
	mvmd.AppendOutput("m1-e1", "entrypoint", v1alpha2.OutputStream_OUTPUT_STREAM_STDOUT, "hello\n")
	require.Eventually(t, func() bool {
		resp, err := apiServerClient(t, gaon).Get(logURL(port))
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the renewed pair serves")
}
