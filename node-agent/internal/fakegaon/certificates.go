package fakegaon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/clock"
)

var tokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// Seen is what the server's authentication read from one request.
type Seen struct {
	Method, Path string
	// ClientCN and ClientSerial are those of the client certificate, empty
	// when the request carried none.
	ClientCN, ClientSerial string
	// Token is the bearer token of the request, empty when it carried none.
	Token string
}

// CSRCreate is one CertificateSigningRequest the server was asked to create.
type CSRCreate struct {
	Name     string
	Username string
	Request  *x509.CertificateRequest
	At       time.Time
}

type tlsState struct {
	clk      clock.Clock
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caFile   string
	srv      *httptest.Server
	tokens   map[string]bool
	seen     []Seen
	creates  []CSRCreate
	gets     int
	validity time.Duration
	deny     string
	hold     bool
	pending  []string
}

// NewTLS starts a fake that serves TLS 1.3 and acts as the API server and the
// signer of ADR 0007: a request with a verified client certificate is
// authenticated as its CN, a request with an accepted bearer token may only
// create and read a CertificateSigningRequest, and any other is refused. It
// verifies client certificates against clk, signs requests with a CA of its own
// from clk's time, and stops when the test ends. Until SetSigning, the
// certificates it signs are valid for an hour.
func NewTLS(t testing.TB, clk clock.Clock) *Server {
	t.Helper()
	s, mux := newServer()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake-ca"},
		NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().Add(24 * 365 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	st := &tlsState{clk: clk, caCert: caCert, caKey: caKey, tokens: map[string]bool{}, validity: time.Hour}
	st.caFile = filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(st.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	s.tls = st

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fake-server"},
		NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().Add(24 * 365 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}, caCert, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	clients := x509.NewCertPool()
	clients.AddCert(caCert)
	ts := httptest.NewUnstartedServer(s.authenticate(mux))
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    clients,
		Time:         clk.Now,
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	st.srv = ts
	s.URL = ts.URL
	return s
}

// CAFile is the path of the CA certificate a client must trust.
func (s *Server) CAFile() string { return s.tls.caFile }

// AcceptToken makes token a valid bootstrap token.
func (s *Server) AcceptToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tls.tokens[token] = true
}

// SetSigning makes the certificates signed from now on valid for validity from
// the time they are signed, and approves requests.
func (s *Server) SetSigning(validity time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tls.validity, s.tls.deny = validity, ""
}

// DenyRequests makes the signer deny each request decided from now on, with
// reason.
func (s *Server) DenyRequests(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tls.deny = reason
}

// HoldRequests makes the signer leave requests undecided while on is true.
// Turning it off decides every request left waiting.
func (s *Server) HoldRequests(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tls.hold = on
	if on {
		return
	}
	for _, name := range s.tls.pending {
		s.decide(name)
	}
	s.tls.pending = nil
}

// DropConnections closes every open client connection, so the next request
// makes a new TLS handshake.
func (s *Server) DropConnections() { s.tls.srv.CloseClientConnections() }

// Seen returns the authentication of every request so far, in order.
func (s *Server) Seen() []Seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Seen(nil), s.tls.seen...)
}

// CSRCreates returns every request the server was asked to create, in order.
func (s *Server) CSRCreates() []CSRCreate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CSRCreate(nil), s.tls.creates...)
}

// CSRReads returns how many times a request was read.
func (s *Server) CSRReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tls.gets
}

// Issue returns a certificate for cn valid from notBefore to notAfter, signed
// by the server's CA, and its key, both PEM.
func (s *Server) Issue(t testing.TB, cn string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	return s.IssueFor(t, pkix.Name{CommonName: cn}, notBefore, notAfter, x509.ExtKeyUsageClientAuth)
}

// IssueFor is Issue for a subject and the extended key usages given.
func (s *Server) IssueFor(t testing.TB, subject pkix.Name, notBefore, notAfter time.Time, usages ...x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := s.sign(subject, &key.PublicKey, notBefore, notAfter, usages...)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// requestedUsages returns the extended key usage csr asks for: client auth
// alone when it asks for none, as the signer of ADR 0043 does.
func requestedUsages(csr *x509.CertificateRequest) []x509.ExtKeyUsage {
	for _, e := range csr.Extensions {
		if !e.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 37}) {
			continue
		}
		var oids []asn1.ObjectIdentifier
		if _, err := asn1.Unmarshal(e.Value, &oids); err != nil {
			panic(err)
		}
		var usages []x509.ExtKeyUsage
		for _, oid := range oids {
			if oid.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 1}) {
				usages = append(usages, x509.ExtKeyUsageServerAuth)
			} else {
				usages = append(usages, x509.ExtKeyUsageClientAuth)
			}
		}
		return usages
	}
	return []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
}

func (s *Server) sign(subject pkix.Name, pub any, notBefore, notAfter time.Time, usages ...x509.ExtKeyUsage) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	return x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial, Subject: subject, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
	}, s.tls.caCert, pub, s.tls.caKey)
}

// authenticate records each request's credentials and refuses what ADR 0007
// refuses: a request with no client certificate may only create or read a
// CertificateSigningRequest, with a valid token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := Seen{Method: r.Method, Path: r.URL.Path}
		identity := ""
		if len(r.TLS.PeerCertificates) > 0 {
			c := r.TLS.PeerCertificates[0]
			seen.ClientCN, seen.ClientSerial = c.Subject.CommonName, c.SerialNumber.String()
			identity = c.Subject.CommonName
		}
		token, hasToken := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if hasToken {
			seen.Token = token
		}
		s.mu.Lock()
		s.tls.seen = append(s.tls.seen, seen)
		valid := hasToken && tokenPattern.MatchString(token) && s.tls.tokens[token]
		s.mu.Unlock()
		if identity == "" {
			switch {
			case !valid:
				writeError(w, api.NewAPIError(api.ReasonUnauthorized, "no client certificate and no valid token"))
				return
			case !isCSRPath(r):
				writeError(w, api.NewAPIError(api.ReasonForbidden, "a token holder only creates and reads a request"))
				return
			}
			identity = "bootstrap:" + strings.SplitN(token, ".", 2)[0]
		}
		r.Header.Set("X-Fake-Identity", identity)
		next.ServeHTTP(w, r)
	})
}

func isCSRPath(r *http.Request) bool {
	name, ok := strings.CutPrefix(r.URL.Path, "/certificatesigningrequests/")
	return ok && name != "" && !strings.Contains(name, "/") && (r.Method == http.MethodPut || r.Method == http.MethodGet)
}

func (s *Server) putCSR(w http.ResponseWriter, r *http.Request) {
	var next api.CertificateSigningRequest
	if !decode(w, r, &next) {
		return
	}
	if next.Metadata.ResourceVersion != "" {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "a request is created without a version"))
		return
	}
	if err := next.Validate(); err != nil {
		writeError(w, err.(*api.APIError))
		return
	}
	block, _ := pem.Decode([]byte(next.Spec.Request))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err == nil {
		err = csr.CheckSignature()
	}
	if err != nil {
		writeError(w, api.NewAPIError(api.ReasonInvalid, "spec.request: %v", err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := next.Metadata.Name
	if _, found := s.csrs[name]; found {
		writeError(w, api.NewAPIError(api.ReasonAlreadyExists, "%q exists", name))
		return
	}
	next.TypeMeta = api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest}
	next.Spec.Username = r.Header.Get("X-Fake-Identity")
	s.rev++
	next.Metadata.ResourceVersion = version(s.rev)
	s.csrs[name] = next
	s.tls.creates = append(s.tls.creates, CSRCreate{Name: name, Username: next.Spec.Username, Request: csr, At: s.tls.clk.Now()})
	if s.tls.hold {
		s.tls.pending = append(s.tls.pending, name)
	} else {
		s.decide(name)
	}
	writeJSON(w, http.StatusOK, s.csrs[name])
}

// decide records the signer's answer for the request name: Denied with the
// configured reason, or Approved with a certificate. The caller holds s.mu.
func (s *Server) decide(name string) {
	r := s.csrs[name]
	now := s.tls.clk.Now()
	if s.tls.deny != "" {
		r.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: api.ConditionTrue, Reason: s.tls.deny, Message: s.tls.deny}}
	} else {
		block, _ := pem.Decode([]byte(r.Spec.Request))
		csr, _ := x509.ParseCertificateRequest(block.Bytes)
		der, err := s.sign(csr.Subject, csr.PublicKey, now, now.Add(s.tls.validity), requestedUsages(csr)...)
		if err != nil {
			panic(err)
		}
		r.Status.Conditions = []api.Condition{{Type: api.CSRApproved, Status: api.ConditionTrue, Reason: "Signed"}}
		r.Status.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	s.rev++
	r.Metadata.ResourceVersion = version(s.rev)
	s.csrs[name] = r
}

func (s *Server) getCSR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tls.gets++
	got, ok := s.csrs[r.PathValue("name")]
	if !ok {
		notFound(w, r.PathValue("name"))
		return
	}
	if who := r.Header.Get("X-Fake-Identity"); strings.HasPrefix(who, "bootstrap:") && who != got.Spec.Username {
		writeError(w, api.NewAPIError(api.ReasonForbidden, "a token holder reads only a request it created"))
		return
	}
	writeJSON(w, http.StatusOK, got)
}
