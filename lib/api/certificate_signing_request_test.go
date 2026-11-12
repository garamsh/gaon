package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// requestPEM returns a PEM PKCS#10 request for cn.
func requestPEM(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func newCSR(t *testing.T, name string) *api.CertificateSigningRequest {
	t.Helper()
	return &api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CertificateSigningRequestSpec{Request: requestPEM(t, "node-a")},
	}
}

func TestCertificateSigningRequestValidateRefusesWhatIsNotAPEMRequest(t *testing.T) {
	good := newCSR(t, "csr-1")
	require.NoError(t, good.Validate(), "control: a PEM CERTIFICATE REQUEST is accepted")

	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("der")}))
	for name, request := range map[string]string{
		"empty":                      "",
		"not PEM":                    "MIIBIjANBgkq",
		"a PEM of another type":      certificate,
		"a request type, wrong case": "-----BEGIN certificate request-----\nZGVy\n-----END certificate request-----\n",
	} {
		csr := newCSR(t, "csr-1")
		csr.Spec.Request = request
		assert.ErrorIs(t, csr.Validate(), api.ErrInvalid, name)
	}
}

func TestCertificateSigningRequestValidateChecksIdentity(t *testing.T) {
	csr := newCSR(t, "csr-1")
	csr.Kind = api.KindNode
	assert.ErrorIs(t, csr.Validate(), api.ErrInvalid)
	csr = newCSR(t, "")
	assert.ErrorIs(t, csr.Validate(), api.ErrInvalid)
}

func TestCertificateSigningRequestValidateStatusRefusesAnUnknownConditionType(t *testing.T) {
	for _, ty := range []string{api.CSRApproved, api.CSRDenied} {
		csr := newCSR(t, "csr-1")
		csr.Status.Conditions = []api.Condition{{Type: ty, Status: api.ConditionTrue}}
		assert.NoError(t, csr.ValidateStatus(), ty)
	}
	for _, ty := range []string{"", "approved", "Failed"} {
		csr := newCSR(t, "csr-1")
		csr.Status.Conditions = []api.Condition{{Type: ty, Status: api.ConditionTrue}}
		assert.ErrorIs(t, csr.ValidateStatus(), api.ErrInvalid, "%q", ty)
	}
}

func TestCertificateSigningRequestValidateStatusRefusesAnUnknownConditionStatus(t *testing.T) {
	for _, status := range []api.ConditionStatus{api.ConditionTrue, api.ConditionFalse, api.ConditionUnknown} {
		csr := newCSR(t, "csr-1")
		csr.Status.Conditions = []api.Condition{{Type: api.CSRApproved, Status: status}}
		assert.NoError(t, csr.ValidateStatus(), string(status))
	}
	for _, status := range []api.ConditionStatus{"", "true", "Maybe"} {
		csr := newCSR(t, "csr-1")
		csr.Status.Conditions = []api.Condition{{Type: api.CSRApproved, Status: status}}
		assert.ErrorIs(t, csr.ValidateStatus(), api.ErrInvalid, "%q", status)
	}
}

func TestClientCertificateSigningRequestIsCreatedOnceAndSignedByStatus(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	asked := newCSR(t, "csr-1")
	asked.Spec.Username = "node-b"
	created, err := c.PutCertificateSigningRequest(ctx, asked)
	require.NoError(t, err)
	assert.Equal(t, asked.Spec.Request, created.Spec.Request)
	assert.Equal(t, memoryRequester, created.Spec.Username, "the server sets the username, not the requester")
	assert.Empty(t, created.Status.Conditions)

	_, err = c.PutCertificateSigningRequest(ctx, newCSR(t, "csr-1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)

	badRequest := newCSR(t, "csr-2")
	badRequest.Spec.Request = "not a request"
	_, err = c.PutCertificateSigningRequest(ctx, badRequest)
	assert.ErrorIs(t, err, api.ErrInvalid)

	sign := *created
	sign.Status = api.CertificateSigningRequestStatus{
		Conditions:  []api.Condition{{Type: api.CSRApproved, Status: api.ConditionTrue}},
		Certificate: "-----BEGIN CERTIFICATE-----\nZGVy\n-----END CERTIFICATE-----\n",
	}
	signed, err := c.PutCertificateSigningRequestStatus(ctx, &sign)
	require.NoError(t, err)
	assert.Equal(t, sign.Status, signed.Status)
	assert.Equal(t, created.Spec, signed.Spec, "a status write leaves the spec")

	_, err = c.PutCertificateSigningRequestStatus(ctx, &sign)
	assert.ErrorIs(t, err, api.ErrConflict, "a status write from an older version loses the compare-and-swap")

	badCondition := *signed
	badCondition.Status.Conditions = []api.Condition{{Type: "Failed", Status: api.ConditionTrue}}
	_, err = c.PutCertificateSigningRequestStatus(ctx, &badCondition)
	assert.ErrorIs(t, err, api.ErrInvalid)

	badStatus := *signed
	badStatus.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: "Maybe"}}
	_, err = c.PutCertificateSigningRequestStatus(ctx, &badStatus)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetCertificateSigningRequest(ctx, "csr-1")
	require.NoError(t, err)
	assert.Equal(t, signed, got, "the refused writes changed nothing")
}

func TestClientCertificateSigningRequestSpecIsImmutable(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)
	created, err := c.PutCertificateSigningRequest(ctx, newCSR(t, "csr-1"))
	require.NoError(t, err)

	change := *created
	change.Spec.Request = requestPEM(t, "node-b")
	_, err = c.PutCertificateSigningRequest(ctx, &change)
	assert.ErrorIs(t, err, api.ErrInvalid)

	got, err := c.GetCertificateSigningRequest(ctx, "csr-1")
	require.NoError(t, err)
	assert.Equal(t, created.Spec.Request, got.Spec.Request)
}

func TestClientCertificateSigningRequestGetAndList(t *testing.T) {
	ctx := testContext(t)
	_, c := newMemoryServer(t)

	_, err := c.GetCertificateSigningRequest(ctx, "absent")
	assert.ErrorIs(t, err, api.ErrNotFound)

	_, err = c.PutCertificateSigningRequest(ctx, newCSR(t, "csr-1"))
	require.NoError(t, err)
	second, err := c.PutCertificateSigningRequest(ctx, newCSR(t, "csr-2"))
	require.NoError(t, err)

	list, err := c.ListCertificateSigningRequests(ctx)
	require.NoError(t, err)
	assert.Equal(t, api.KindCertificateSigningRequestList, list.Kind)
	assert.Len(t, list.Items, 2)
	assert.Equal(t, second.Metadata.ResourceVersion, list.Metadata.ResourceVersion)
}

func TestClientWatchCertificateSigningRequestsDeliversTheirEventsOnly(t *testing.T) {
	ctx := testContext(t)
	s, c := newMemoryServer(t)
	_, err := c.PutMicroVM(ctx, newMicroVM("m1"))
	require.NoError(t, err)
	created, err := c.PutCertificateSigningRequest(ctx, newCSR(t, "csr-1"))
	require.NoError(t, err)

	w, err := c.WatchCertificateSigningRequests(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ev := nextEvent(t, w)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "csr-1", ev.Object.Metadata.Name, "the MicroVM's event is not delivered to a request watch")

	sign := *created
	sign.Status.Conditions = []api.Condition{{Type: api.CSRApproved, Status: api.ConditionTrue}}
	signed, err := c.PutCertificateSigningRequestStatus(ctx, &sign)
	require.NoError(t, err)
	ev = nextEvent(t, w)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, signed.Metadata.ResourceVersion, ev.Object.Metadata.ResourceVersion)
	assert.Equal(t, signed.Status, ev.Object.Status)

	s.compact()
	stale, err := c.WatchCertificateSigningRequests(ctx, "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stale.Close() })
	_, err = stale.Next()
	assert.ErrorIs(t, err, api.ErrExpired)
	_, err = stale.Next()
	assert.ErrorIs(t, err, io.EOF, "the Error event is the last of its stream")

	_, err = c.WatchCertificateSigningRequests(ctx, "")
	assert.Error(t, err, "a watch names the version it resumes from")
}
