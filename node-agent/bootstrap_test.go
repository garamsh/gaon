package nodeagent_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nodeagent "github.com/garamsh/gaon/node-agent"
	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
	"github.com/garamsh/gaon/node-agent/internal/fakemvmd"
)

const bootstrapToken = "abcdef.0123456789abcdef"

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestParseConfig_BootstrapFlagRules(t *testing.T) {
	a := newMTLSAPI(t)
	tokenFile := writeFile(t, "token", bootstrapToken+"\n")
	dir := filepath.Join(t.TempDir(), "certs")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"token file without a cert dir", []string{"--bootstrap-token-file", tokenFile, "--tls-ca", a.ca}, "--bootstrap-token-file needs --cert-dir"},
		{"token file without a CA", []string{"--bootstrap-token-file", tokenFile, "--cert-dir", dir}, "needs --tls-ca"},
		{"cert dir without a CA", []string{"--cert-dir", dir}, "--cert-dir needs --tls-ca"},
		{"token file with the operator's pair", append([]string{"--bootstrap-token-file", tokenFile, "--cert-dir", dir}, a.flags()...), "exclude each other"},
		{"cert dir with the operator's pair", append([]string{"--cert-dir", dir}, a.flags()...), "exclude each other"},
		{"a token of the wrong form", []string{"--bootstrap-token-file", writeFile(t, "bad", "ABCDEF.0123456789abcdef"), "--cert-dir", dir, "--tls-ca", a.ca}, "want one token matching"},
		{"an id of five characters", []string{"--bootstrap-token-file", writeFile(t, "short", "abcde.0123456789abcdef"), "--cert-dir", dir, "--tls-ca", a.ca}, "want one token matching"},
		{"a secret of seventeen characters", []string{"--bootstrap-token-file", writeFile(t, "long", "abcdef.0123456789abcdefg"), "--cert-dir", dir, "--tls-ca", a.ca}, "want one token matching"},
		{"two tokens", []string{"--bootstrap-token-file", writeFile(t, "two", bootstrapToken+"\n"+bootstrapToken), "--cert-dir", dir, "--tls-ca", a.ca}, "want one token matching"},
		{"an empty token file", []string{"--bootstrap-token-file", writeFile(t, "empty", "\n"), "--cert-dir", dir, "--tls-ca", a.ca}, "want one token matching"},
		{"a missing token file", []string{"--bootstrap-token-file", filepath.Join(t.TempDir(), "none"), "--cert-dir", dir, "--tls-ca", a.ca}, "bootstrap token file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := nodeagent.ParseConfig(tt.args, env(required), io.Discard)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "0123456789abcdef", "the token is never printed")
		})
	}
	for _, args := range [][]string{
		{"--bootstrap-token-file", tokenFile, "--cert-dir", dir, "--tls-ca", a.ca},
		{"--cert-dir", dir, "--tls-ca", a.ca},
	} {
		cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
		require.NoError(t, err, "control: %v", args)
		assert.Equal(t, dir, cfg.CertDir)
	}
}

func TestParseConfig_TakesTheBootstrapFlagsFromTheEnvironment(t *testing.T) {
	a := newMTLSAPI(t)
	dir := filepath.Join(t.TempDir(), "certs")
	environment := map[string]string{
		nodeagent.EnvBootstrapTokenFile: writeFile(t, "token", " "+bootstrapToken+" \n"),
		nodeagent.EnvCertDir:            dir,
		nodeagent.EnvTLSCA:              a.ca,
	}
	for k, v := range required {
		environment[k] = v
	}

	cfg, err := nodeagent.ParseConfig(nil, env(environment), io.Discard)

	require.NoError(t, err)
	assert.Equal(t, dir, cfg.CertDir)
	assert.Equal(t, bootstrapToken, cfg.BootstrapToken, "whitespace around the token is trimmed")
}

func leafOf(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, certificate.PairFile))
	require.NoError(t, err)
	block, _ := pem.Decode(b)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return leaf
}

func startAgent(t *testing.T, cfg nodeagent.Config, clk clock.Clock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- nodeagent.Run(ctx, cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestRun_BootstrapsRegistersOverTheCertificateRenewsAndUsesTheNewPairWithoutRestart(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	gaon.AcceptToken(bootstrapToken)
	gaon.SetSigning(3 * time.Second) // renewal is due two seconds in
	dir := filepath.Join(t.TempDir(), "certs")
	args := []string{
		"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", dir,
		"--bootstrap-token-file", writeFile(t, "token", bootstrapToken),
	}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	startAgent(t, cfg, clk)

	require.Eventually(t, func() bool { _, ok := gaon.Node("n1"); return ok }, 5*time.Second, 10*time.Millisecond,
		"the agent got a certificate with its token and registered the Node")
	first := leafOf(t, dir)
	assert.Equal(t, "n1", first.Subject.CommonName)
	for _, s := range gaon.Seen() {
		if s.Path == "/nodes/n1" || s.Path == "/nodes" {
			assert.Equal(t, "n1", s.ClientCN, "the Node was registered over the certificate")
			assert.Empty(t, s.Token, "without the token")
		}
	}
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	info, err = os.Stat(filepath.Join(dir, certificate.PairFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Eventually(t, func() bool { return len(gaon.CSRCreates()) >= 2 }, 10*time.Second, 20*time.Millisecond, "the agent renews")
	renewal := gaon.CSRCreates()[1]
	assert.Equal(t, "n1", renewal.Username, "over its own certificate")
	require.Eventually(t, func() bool { return leafOf(t, dir).SerialNumber.Cmp(first.SerialNumber) != 0 }, 5*time.Second, 20*time.Millisecond)
	second := leafOf(t, dir)

	gaon.DropConnections()
	require.Eventually(t, func() bool {
		for _, s := range gaon.Seen() {
			if s.ClientSerial == second.SerialNumber.String() {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the new pair is presented on a new connection, with no restart")
	for _, s := range gaon.Seen() {
		if s.Path != "/certificatesigningrequests/"+renewal.Name || s.Method != "PUT" {
			continue
		}
		assert.Equal(t, first.SerialNumber.String(), s.ClientSerial, "the renewal was presented with the first certificate")
		assert.Empty(t, s.Token, "and no token")
	}
}

func TestRun_UsesAValidPairInTheDirectoryWithoutAToken(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	dir := filepath.Join(t.TempDir(), "certs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	now := time.Now()
	certPEM, keyPEM := gaon.Issue(t, "n1", now.Add(-time.Minute), now.Add(time.Hour))
	require.NoError(t, os.WriteFile(filepath.Join(dir, certificate.PairFile), append(certPEM, keyPEM...), 0o600))
	args := []string{"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", dir}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)
	startAgent(t, cfg, clk)

	require.Eventually(t, func() bool { _, ok := gaon.Node("n1"); return ok }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, gaon.CSRCreates(), "nothing is requested while the pair is valid and not due")
}

func TestRun_ADirectoryWithoutAValidPairAndWithoutATokenStopsTheStart(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	args := []string{"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", filepath.Join(t.TempDir(), "certs")}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)

	err = nodeagent.Run(context.Background(), cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no bootstrap token")
	_, registered := gaon.Node("n1")
	assert.False(t, registered)
}

func TestRun_ADeniedBootstrapStopsTheStartWithTheReason(t *testing.T) {
	clk := clock.System{}
	gaon, mvmd := fakegaon.NewTLS(t, clk), fakemvmd.New(t, clk)
	gaon.AcceptToken(bootstrapToken)
	gaon.DenyRequests("NotANode")
	args := []string{
		"--api-server", gaon.URL, "--mvmd-socket", mvmd.Socket, "--tls-ca", gaon.CAFile(), "--cert-dir", filepath.Join(t.TempDir(), "certs"),
		"--bootstrap-token-file", writeFile(t, "token", bootstrapToken),
	}
	cfg, err := nodeagent.ParseConfig(args, env(required), io.Discard)
	require.NoError(t, err)

	err = nodeagent.Run(context.Background(), cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	assert.Contains(t, err.Error(), "NotANode")
	assert.NotContains(t, err.Error(), "0123456789abcdef", "the token is never printed")
	_, registered := gaon.Node("n1")
	assert.False(t, registered, "no Node is registered")
}
