//go:build extensions

package extensions

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// authority is the one CA of the run. The API server trusts it for client
// certificates, gaon-controllers signs with it, and every client trusts it
// for the servers.
type authority struct {
	dir     string
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	CertPEM []byte
	// CertFile and KeyFile are the PEM files gaon-controllers signs with.
	CertFile, KeyFile string
}

// identity is a certificate and its key as files.
type identity struct {
	CertFile, KeyFile string
	CertPEM           []byte
}

func newAuthority(t *testing.T, dir string) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: runTag + "-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	a := &authority{dir: dir, cert: cert, key: key, CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	a.CertFile = writeFile(t, filepath.Join(dir, "ca.crt"), a.CertPEM)
	a.KeyFile = writeFile(t, filepath.Join(dir, "ca.key"), keyPEM(t, key))
	return a
}

// client issues a client certificate whose subject is cn and orgs; the API
// server reads them as the user name and the groups.
func (a *authority) client(t *testing.T, cn string, orgs ...string) identity {
	t.Helper()
	return a.issue(t, pkix.Name{CommonName: cn, Organization: orgs}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

// server issues a server certificate for 127.0.0.1.
func (a *authority) server(t *testing.T, cn string) identity {
	t.Helper()
	return a.issue(t, pkix.Name{CommonName: cn}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
}

func (a *authority) issue(t *testing.T, subject pkix.Name, usage []x509.ExtKeyUsage) identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      subject,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	base := filepath.Join(a.dir, subject.CommonName)
	return identity{
		CertFile: writeFile(t, base+".crt", certPEM),
		KeyFile:  writeFile(t, base+".key", keyPEM(t, key)),
		CertPEM:  certPEM,
	}
}

func keyPEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	require.NoError(t, err)
	return n
}

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}
