package csrsigner_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/csrsigner"
	"github.com/garamsh/gaon/lib/api"
)

const signingDuration = 48 * time.Hour

// now is the signing time of every test, so a certificate's validity is exact.
var now = time.Now().UTC().Truncate(time.Second)

// start runs the signer against f until the test ends.
func start(t *testing.T, f *fakeAPI, ca *csrsigner.CA, duration time.Duration) {
	t.Helper()
	cfg := csrsigner.Config{CA: ca, Duration: duration, Now: func() time.Time { return now }}
	svc := csrsigner.NewService(f.client, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
}

// newRequest returns a request as the API server stores it: named, with the
// username it set, and undecided.
func newRequest(name, username, pemRequest string) *api.CertificateSigningRequest {
	return &api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CertificateSigningRequestSpec{Request: pemRequest, Username: username},
	}
}

// waitDecided waits until the API holds an Approved or Denied condition for
// name, and returns the request.
func waitDecided(t *testing.T, f *fakeAPI, name string) api.CertificateSigningRequest {
	t.Helper()
	require.Eventually(t, func() bool {
		r := f.stored(name)
		return len(r.Status.Conditions) > 0
	}, 5*time.Second, 5*time.Millisecond, "request %s is decided", name)
	return f.stored(name)
}

// condition returns the one condition r carries.
func condition(t *testing.T, r api.CertificateSigningRequest) api.Condition {
	t.Helper()
	require.Len(t, r.Status.Conditions, 1)
	return r.Status.Conditions[0]
}

// issued parses the certificate recorded in r.
func issued(t *testing.T, r api.CertificateSigningRequest) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode([]byte(r.Status.Certificate))
	require.NotNil(t, block, "status.certificate is PEM")
	require.Empty(t, rest)
	require.Equal(t, "CERTIFICATE", block.Type)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func TestRun_ApprovesABootstrapRequestAndSignsAClientCertificate(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	pemRequest, pub := newCSR(t, "node-1")
	f.add(newRequest("csr-1", "bootstrap:abcdef", pemRequest))
	start(t, f, ca.load(t), signingDuration)

	r := waitDecided(t, f, "csr-1")

	c := condition(t, r)
	assert.Equal(t, api.CSRApproved, c.Type)
	assert.Equal(t, api.ConditionTrue, c.Status)
	cert := issued(t, r)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	_, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	assert.NoError(t, err, "the certificate verifies against the CA as a client certificate")
	assert.Equal(t, "node-1", cert.Subject.CommonName)
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, cert.ExtKeyUsage)
	assert.Equal(t, x509.KeyUsageDigitalSignature, cert.KeyUsage)
	assert.True(t, pub.Equal(cert.PublicKey), "the certificate carries the requested key")
	assert.Equal(t, now.Add(-5*time.Minute), cert.NotBefore)
	assert.Equal(t, now.Add(signingDuration), cert.NotAfter)
	assert.Empty(t, cert.DNSNames)
}

func TestRun_SignsEachCertificateWithItsOwnRandomSerial(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	for _, name := range []string{"csr-1", "csr-2"} {
		pemRequest, _ := newCSR(t, "node-1")
		f.add(newRequest(name, "bootstrap:abcdef", pemRequest))
	}
	start(t, f, ca.load(t), signingDuration)

	first, second := issued(t, waitDecided(t, f, "csr-1")), issued(t, waitDecided(t, f, "csr-2"))

	assert.NotEqual(t, first.SerialNumber, second.SerialNumber)
	assert.Greater(t, first.SerialNumber.BitLen(), 64, "the serial is 128 random bits")
	assert.Greater(t, second.SerialNumber.BitLen(), 64)
}

func TestRun_ApprovesARenewalByTheNodesOwnCertificate(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	pemRequest, _ := newCSR(t, "node-1")
	f.add(newRequest("csr-1", "node-1", pemRequest))
	start(t, f, ca.load(t), signingDuration)

	r := waitDecided(t, f, "csr-1")

	assert.Equal(t, api.CSRApproved, condition(t, r).Type)
	assert.Equal(t, "node-1", issued(t, r).Subject.CommonName)
	assert.Equal(t, now.Add(signingDuration), issued(t, r).NotAfter)
}

func TestRun_CapsNotAfterAtTheEndOfTheCACertificate(t *testing.T) {
	ca := newTestCA(t, now.Add(time.Hour))
	f := newFakeAPI(t)
	pemRequest, _ := newCSR(t, "node-1")
	f.add(newRequest("csr-1", "bootstrap:abcdef", pemRequest))
	start(t, f, ca.load(t), signingDuration)

	cert := issued(t, waitDecided(t, f, "csr-1"))

	assert.Equal(t, ca.cert.NotAfter, cert.NotAfter)
	assert.True(t, cert.NotAfter.Before(now.Add(signingDuration)), "the CA ends before the duration does")
}

func TestRun_DeniesEachFailureOfTheApprovalRuleWithItsOwnReason(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)

	garbage := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("not a request")}))
	der, _ := newCSRDER(t, "node-1")
	der[len(der)-1] ^= 0xff
	badSignature := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	node1, _ := newCSR(t, "node-1")
	empty, _ := newCSR(t, "")
	tokenName, _ := newCSR(t, "bootstrap:abcdef")
	san, _ := newCSR(t, "node-1", withDNSName("node-1.example"))
	other, _ := newCSR(t, "node-1", withOtherExtension())
	serverAuth, _ := newCSR(t, "node-1", withExtKeyUsage(t, oidServerAuth))
	both, _ := newCSR(t, "node-1", withExtKeyUsage(t, oidClientAuth, oidServerAuth))
	clientAuth, _ := newCSR(t, "node-1", withExtKeyUsage(t, oidClientAuth))

	tests := []struct {
		name     string
		username string
		request  string
		approved bool
		reason   string
	}{
		{"control", "bootstrap:abcdef", node1, true, ""},
		{"control with client auth", "bootstrap:abcdef", clientAuth, true, ""},
		{"not a certificate request", "bootstrap:abcdef", garbage, false, "InvalidRequest"},
		{"signature does not verify", "bootstrap:abcdef", badSignature, false, "InvalidRequest"},
		{"empty CN", "bootstrap:abcdef", empty, false, "InvalidSubject"},
		{"CN of a token", "bootstrap:abcdef", tokenName, false, "InvalidSubject"},
		{"subject alternative name", "bootstrap:abcdef", san, false, "DisallowedExtension"},
		{"other extension", "bootstrap:abcdef", other, false, "DisallowedExtension"},
		{"server auth only", "bootstrap:abcdef", serverAuth, false, "DisallowedExtension"},
		{"client and server auth", "bootstrap:abcdef", both, false, "DisallowedExtension"},
		{"another Node's certificate", "node-2", node1, false, "UnauthorizedRequester"},
		{"empty username", "", node1, false, "UnauthorizedRequester"},
	}
	for i, tt := range tests {
		f.add(newRequest(requestName(i), tt.username, tt.request))
	}
	start(t, f, ca.load(t), signingDuration)

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := waitDecided(t, f, requestName(i))

			c := condition(t, r)
			if tt.approved {
				assert.Equal(t, api.CSRApproved, c.Type)
				assert.NotEmpty(t, r.Status.Certificate)
				return
			}
			assert.Equal(t, api.CSRDenied, c.Type)
			assert.Equal(t, api.ConditionTrue, c.Status)
			assert.Equal(t, tt.reason, c.Reason)
			assert.NotEmpty(t, c.Message)
			assert.Empty(t, r.Status.Certificate, "a denied request is not signed")
		})
	}
}

func requestName(i int) string { return "csr-" + string(rune('a'+i)) }

func TestRun_RecordsTheFirstFailedRule(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	garbage := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("not a request")}))
	emptySAN, _ := newCSR(t, "", withDNSName("node-1.example"))
	sanOtherUser, _ := newCSR(t, "node-1", withDNSName("node-1.example"))
	f.add(newRequest("csr-a", "", garbage))
	f.add(newRequest("csr-b", "node-2", emptySAN))
	f.add(newRequest("csr-c", "node-2", sanOtherUser))
	start(t, f, ca.load(t), signingDuration)

	assert.Equal(t, "InvalidRequest", condition(t, waitDecided(t, f, "csr-a")).Reason)
	assert.Equal(t, "InvalidSubject", condition(t, waitDecided(t, f, "csr-b")).Reason)
	assert.Equal(t, "DisallowedExtension", condition(t, waitDecided(t, f, "csr-c")).Reason)
}

func TestRun_LeavesADecidedRequestAsItIs(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	pemRequest, _ := newCSR(t, "node-1")
	approved := newRequest("csr-approved", "bootstrap:abcdef", pemRequest)
	approved.Status = api.CertificateSigningRequestStatus{Conditions: []api.Condition{{Type: api.CSRApproved, Status: api.ConditionTrue, Reason: "Earlier"}}, Certificate: "earlier"}
	denied := newRequest("csr-denied", "bootstrap:abcdef", pemRequest)
	denied.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: api.ConditionFalse, Reason: "Earlier"}}
	pending := newRequest("csr-pending", "bootstrap:abcdef", pemRequest)
	f.add(approved)
	f.add(denied)
	f.add(pending)
	start(t, f, ca.load(t), signingDuration)

	waitDecided(t, f, "csr-pending")

	assert.Equal(t, []string{"csr-pending"}, f.written(), "the requests listed before it were passed over")
	got := f.stored("csr-approved")
	assert.Equal(t, "earlier", got.Status.Certificate)
	assert.Equal(t, "Earlier", condition(t, got).Reason)
	assert.Equal(t, "Earlier", condition(t, f.stored("csr-denied")).Reason)
}

func TestRun_DecidesTheRequestsAWatchDeliversAndSkipsDecidedOnes(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	start(t, f, ca.load(t), signingDuration)
	<-f.watching
	pemRequest, _ := newCSR(t, "node-1")

	decided := newRequest("csr-decided", "bootstrap:abcdef", pemRequest)
	decided.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: api.ConditionTrue, Reason: "Earlier"}}
	deleted := newRequest("csr-deleted", "bootstrap:abcdef", pemRequest)
	pending := newRequest("csr-pending", "bootstrap:abcdef", pemRequest)
	for _, r := range []*api.CertificateSigningRequest{decided, deleted, pending} {
		f.add(r)
	}
	f.emit(api.EventModified, decided)
	f.emit(api.EventDeleted, deleted)
	f.emit(api.EventAdded, pending)

	r := waitDecided(t, f, "csr-pending")

	assert.Equal(t, api.CSRApproved, condition(t, r).Type)
	assert.Equal(t, []string{"csr-pending"}, f.written(), "a decided request and a deleted one were not written")
}

func TestRun_ReadsARequestAgainAfterAConflictAndDecidesItWhileUndecided(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	pemRequest, _ := newCSR(t, "node-1")
	f.add(newRequest("csr-1", "bootstrap:abcdef", pemRequest))
	f.conflicts = 1
	start(t, f, ca.load(t), signingDuration)

	r := waitDecided(t, f, "csr-1")

	assert.Equal(t, api.CSRApproved, condition(t, r).Type)
	assert.Equal(t, []string{"csr-1"}, f.written())
}

func TestRun_LeavesARequestDecidedByAnotherWriterDuringAConflict(t *testing.T) {
	ca := newTestCA(t, now.Add(24*365*time.Hour))
	f := newFakeAPI(t)
	pemRequest, _ := newCSR(t, "node-1")
	f.add(newRequest("csr-conflicted", "bootstrap:abcdef", pemRequest))
	f.add(newRequest("csr-next", "bootstrap:abcdef", pemRequest))
	f.conflicts = 1
	f.beforeConflict = func(r *api.CertificateSigningRequest) {
		r.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: api.ConditionTrue, Reason: "Other"}}
	}
	start(t, f, ca.load(t), signingDuration)

	waitDecided(t, f, "csr-next")

	assert.Equal(t, []string{"csr-next"}, f.written(), "the request another writer decided was not written over")
	got := f.stored("csr-conflicted")
	assert.Equal(t, "Other", condition(t, got).Reason)
	assert.Empty(t, got.Status.Certificate)
}
