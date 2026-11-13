package csrsigner_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/csrsigner"
	"github.com/garamsh/gaon/lib/api"
)

// testCA is a CA generated for a test: its certificate, and the files
// csrsigner.LoadCA reads.
type testCA struct {
	cert              *x509.Certificate
	certFile, keyFile string
}

func newTestCA(t *testing.T, notAfter time.Time) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gaon-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	ca := testCA{cert: cert, certFile: filepath.Join(dir, "ca.pem"), keyFile: filepath.Join(dir, "ca-key.pem")}
	require.NoError(t, os.WriteFile(ca.certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(ca.keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return ca
}

func (c testCA) load(t *testing.T) *csrsigner.CA {
	t.Helper()
	ca, err := csrsigner.LoadCA(c.certFile, c.keyFile)
	require.NoError(t, err)
	return ca
}

// csrOption changes the request template before it is signed.
type csrOption func(*x509.CertificateRequest)

var (
	oidEKU         = asn1.ObjectIdentifier{2, 5, 29, 37}
	oidClientAuth  = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 2}
	oidServerAuth  = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 1}
	oidCodeSigning = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 3}

	oidCommonName         = asn1.ObjectIdentifier{2, 5, 4, 3}
	oidOrganization       = asn1.ObjectIdentifier{2, 5, 4, 10}
	oidOrganizationalUnit = asn1.ObjectIdentifier{2, 5, 4, 11}
	oidCountry            = asn1.ObjectIdentifier{2, 5, 4, 6}
)

// withExtKeyUsage asks for an extended key usage of the given OIDs.
func withExtKeyUsage(t *testing.T, oids ...asn1.ObjectIdentifier) csrOption {
	value, err := asn1.Marshal(oids)
	require.NoError(t, err)
	return func(r *x509.CertificateRequest) {
		r.ExtraExtensions = append(r.ExtraExtensions, pkix.Extension{Id: oidEKU, Value: value})
	}
}

// withSubjectAttribute adds an attribute of the given type to the subject, beside the CN.
func withSubjectAttribute(typ asn1.ObjectIdentifier, value string) csrOption {
	return func(r *x509.CertificateRequest) {
		r.Subject.ExtraNames = append(r.Subject.ExtraNames, pkix.AttributeTypeAndValue{Type: typ, Value: value})
	}
}

// withTwoCNs gives the subject two CN attributes. ExtraNames overrides
// Subject.CommonName for the same type, so both go through ExtraNames.
func withTwoCNs(first, second string) csrOption {
	return func(r *x509.CertificateRequest) {
		r.Subject.CommonName = ""
		r.Subject.ExtraNames = append(r.Subject.ExtraNames,
			pkix.AttributeTypeAndValue{Type: oidCommonName, Value: first},
			pkix.AttributeTypeAndValue{Type: oidCommonName, Value: second})
	}
}

// withOrganizations replaces the subject's Organization values.
func withOrganizations(orgs ...string) csrOption {
	return func(r *x509.CertificateRequest) { r.Subject.Organization = orgs }
}

func withDNSName(name string) csrOption {
	return func(r *x509.CertificateRequest) { r.DNSNames = append(r.DNSNames, name) }
}

// withOtherExtension asks for a key usage extension.
func withOtherExtension() csrOption {
	return func(r *x509.CertificateRequest) {
		r.ExtraExtensions = append(r.ExtraExtensions, pkix.Extension{Id: asn1.ObjectIdentifier{2, 5, 29, 15}, Value: []byte{0x03, 0x02, 0x07, 0x80}})
	}
}

// newCSR returns a PEM PKCS#10 request for cn and the public key it asks for.
// Its subject is a Node's: the CN and the Organization gaon:nodes.
func newCSR(t *testing.T, cn string, opts ...csrOption) (pemRequest string, pub *ecdsa.PublicKey) {
	t.Helper()
	der, pub := newCSRDER(t, cn, opts...)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), pub
}

func newCSRDER(t *testing.T, cn string, opts ...csrOption) ([]byte, *ecdsa.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn, Organization: []string{api.NodeGroup}}}
	for _, o := range opts {
		o(tmpl)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	require.NoError(t, err)
	return der, &key.PublicKey
}
