package storagelocal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/plugin"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var required = map[string]string{EnvAPIServer: "http://api", EnvNodeName: "n1", EnvMvmdSocket: "/run/mvmd.sock"}

func TestParseConfig_DefaultsTheSocketDirectoryAndTheResyncPeriodAndRequiresTheRest(t *testing.T) {
	c, err := ParseConfig(nil, env(required), io.Discard)

	require.NoError(t, err)
	assert.Equal(t, plugin.DefaultDir, c.PluginDir)
	assert.Equal(t, defaultResyncPeriod, c.ResyncPeriod)
	assert.Equal(t, "n1", c.NodeName)

	for _, drop := range []string{EnvAPIServer, EnvNodeName, EnvMvmdSocket} {
		partial := map[string]string{}
		for k, v := range required {
			if k != drop {
				partial[k] = v
			}
		}
		_, err := ParseConfig(nil, env(partial), io.Discard)
		require.Error(t, err, "without %s", drop)
		assert.Contains(t, err.Error(), drop)
	}
}

func TestParseConfig_AFlagGivenWinsOverItsVariable(t *testing.T) {
	vars := map[string]string{EnvAPIServer: "http://env", EnvNodeName: "env-node", EnvMvmdSocket: "/env.sock", EnvPluginDir: "/env/dir", EnvResyncPeriod: "3s"}

	c, err := ParseConfig([]string{"--node-name=flag-node", "--plugin-dir=/flag/dir", "--resync-period=7s"}, env(vars), io.Discard)

	require.NoError(t, err)
	assert.Equal(t, "flag-node", c.NodeName)
	assert.Equal(t, "/flag/dir", c.PluginDir)
	assert.Equal(t, 7*time.Second, c.ResyncPeriod)
	assert.Equal(t, "http://env", c.APIServer, "control: a variable whose flag is not given applies")
}

func TestParseConfig_RefusesABadPeriodAStrayArgumentAndACertDirThatCannotWork(t *testing.T) {
	with := func(args ...string) error {
		_, err := ParseConfig(args, env(required), io.Discard)
		return err
	}
	assert.Error(t, with("--resync-period=never"))
	assert.Error(t, with("--resync-period=0s"))
	assert.Error(t, with("stray"))
	assert.Error(t, with("--cert-dir=/d"), "a cert directory without a CA")
	assert.Error(t, with("--cert-dir=/d", "--tls-ca=/ca", "--tls-cert=/c", "--tls-key=/k"), "a cert directory beside a certificate of its own")
	assert.NoError(t, with("--cert-dir=/d", "--tls-ca=/ca"), "control: a cert directory with a CA")
}

// pairPEM is a self-signed certificate for cn followed by its key.
func pairPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}

func commonName(t *testing.T, get func() (string, error)) string {
	t.Helper()
	cn, err := get()
	require.NoError(t, err)
	return cn
}

func TestNodePair_PresentsTheCertificateTheAgentKeepsAndTheOneItRenewsItTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PairFile)
	require.NoError(t, os.WriteFile(path, pairPEM(t, "n1"), 0o600))
	get := nodePair(path)
	cn := func() (string, error) {
		c, err := get(nil)
		if err != nil {
			return "", err
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			return "", err
		}
		return leaf.Subject.CommonName, nil
	}

	assert.Equal(t, "n1", commonName(t, cn), "the Node's own certificate, whose CN is the node name")

	renewed := filepath.Join(dir, "renewed")
	require.NoError(t, os.WriteFile(renewed, pairPEM(t, "n1-renewed"), 0o600))
	require.NoError(t, os.Rename(renewed, path))
	assert.Equal(t, "n1-renewed", commonName(t, cn), "the next handshake presents the renewed pair, with no restart")
}

func TestNodePair_FailsTheHandshakeWhenTheAgentHasNotWrittenAUsablePair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PairFile)
	get := nodePair(path)

	_, err := get(nil)
	require.Error(t, err, "no file yet")

	require.NoError(t, os.WriteFile(path, []byte("not a pair"), 0o600))
	_, err = get(nil)
	require.Error(t, err, "a file that is not a pair")

	require.NoError(t, os.WriteFile(path, pairPEM(t, "n1"), 0o600))
	_, err = get(nil)
	assert.NoError(t, err, "control: the same function serves once the pair is there")
}
