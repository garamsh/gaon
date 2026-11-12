package apiserver_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/garamsh/gaon/apiserver/internal/token"
	"github.com/garamsh/gaon/lib/api"
)

const testIssuer = "https://gaon.test"

// signingKey is a token signing key generated for one test, its file and the
// public half a verifier holds.
type signingKey struct {
	issuer string
	file   string
	pub    *ecdsa.PublicKey
	key    *ecdsa.PrivateKey
}

func newSigningKey(t *testing.T) *signingKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return &signingKey{issuer: testIssuer, file: writeFile(t, "token-key.pem", pemBytes), pub: &key.PublicKey, key: key}
}

func tokenRequest(m *api.MicroVM, seconds int64, audiences ...string) *api.TokenRequest {
	return &api.TokenRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindTokenRequest},
		Spec: api.TokenRequestSpec{
			Audiences:         audiences,
			ExpirationSeconds: seconds,
			BoundMicroVM:      api.BoundObject{Name: m.Metadata.Name, UID: m.Metadata.UID},
		},
	}
}

// tokenEnv is a server with a signing key, the ServiceAccounts web and other,
// and the MicroVM m1 of web.
type tokenEnv struct {
	*env
	key *signingKey
	m1  *api.MicroVM
}

func newTokenEnv(t *testing.T) *tokenEnv {
	t.Helper()
	key := newSigningKey(t)
	e := newEnvWith(t, envOptions{signing: key})
	for _, name := range []string{"web", "other"} {
		_, err := e.api.PutServiceAccount(ctx(t), serviceAccount(name))
		require.NoError(t, err)
	}
	m := microvm("m1")
	m.Spec.ServiceAccountName = "web"
	m, err := e.api.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	return &tokenEnv{env: e, key: key, m1: m}
}

func TestCreateServiceAccountToken_ReturnsATokenSignedByTheConfiguredKey(t *testing.T) {
	e := newTokenEnv(t)
	other := newSigningKey(t)

	got, err := e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, 600, "gaon", "vault"))
	require.NoError(t, err)

	claims, err := token.Verify(got.Status.Token, e.key.pub)
	require.NoError(t, err, "the configured key's public half verifies the token")
	assert.Equal(t, testIssuer, claims.Issuer)
	assert.Equal(t, api.ServiceAccountSubject("web"), claims.Subject)
	assert.Equal(t, []string{"gaon", "vault"}, claims.Audience)
	assert.Equal(t, "web", claims.Extension.ServiceAccount.Name)
	assert.NotEmpty(t, claims.Extension.ServiceAccount.UID)
	assert.Equal(t, api.BoundObject{Name: "m1", UID: e.m1.Metadata.UID}, claims.Extension.MicroVM)
	assert.Equal(t, int64(600), claims.Expiry-claims.IssuedAt)
	require.NotNil(t, got.Status.ExpirationTimestamp)
	assert.Equal(t, claims.Expiry, got.Status.ExpirationTimestamp.Unix())
	assert.WithinDuration(t, time.Now(), time.Unix(claims.IssuedAt, 0), time.Minute)

	_, err = token.Verify(got.Status.Token, other.pub)
	assert.Error(t, err, "control: another key does not verify it")
	parts := strings.Split(got.Status.Token, ".")
	require.Len(t, parts, 3)
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var h map[string]string
	require.NoError(t, json.Unmarshal(header, &h))
	assert.Equal(t, "ES256", h["alg"])
}

func TestCreateServiceAccountToken_LifetimeIsDefaultedAndBounded(t *testing.T) {
	e := newTokenEnv(t)
	lifetime := func(seconds int64) (int64, error) {
		got, err := e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, seconds, "gaon"))
		if err != nil {
			return 0, err
		}
		c, err := token.Verify(got.Status.Token, e.key.pub)
		require.NoError(t, err)
		return c.Expiry - c.IssuedAt, nil
	}

	d, err := lifetime(0)
	require.NoError(t, err)
	assert.Equal(t, api.DefaultTokenSeconds, d, "none asked")
	_, err = lifetime(api.MinTokenSeconds - 1)
	assert.ErrorIs(t, err, api.ErrInvalid, "below the minimum")
	_, err = lifetime(api.MaxTokenSeconds + 1)
	assert.ErrorIs(t, err, api.ErrInvalid, "above the maximum")
	d, err = lifetime(api.MinTokenSeconds)
	require.NoError(t, err, "control: the minimum")
	assert.Equal(t, api.MinTokenSeconds, d)
	d, err = lifetime(api.MaxTokenSeconds)
	require.NoError(t, err, "control: the maximum")
	assert.Equal(t, api.MaxTokenSeconds, d)
}

func TestCreateServiceAccountToken_IsNotStored(t *testing.T) {
	e := newTokenEnv(t)

	got, err := e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, 0, "gaon"))
	require.NoError(t, err)

	all, err := e.etcd.Get(ctx(t), "/", clientv3.WithPrefix())
	require.NoError(t, err)
	require.NotEmpty(t, all.Kvs, "setup: the Store holds the objects of the test")
	for _, kv := range all.Kvs {
		assert.NotContains(t, string(kv.Key), "token", "a key")
		assert.NotContains(t, string(kv.Value), got.Status.Token, "a value")
	}
}

func TestCreateServiceAccountToken_IsNotSentToAWebhook(t *testing.T) {
	e := newTokenEnv(t)
	w := newWebhookServer(t)
	e.mutating(t, "m", w.everything())
	e.validating(t, "v", w.everything())

	_, err := e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, 0, "gaon"))
	require.NoError(t, err)
	assert.Empty(t, w.received(), "no webhook was called")

	_, err = e.api.PutServiceAccount(ctx(t), serviceAccount("fresh"))
	require.NoError(t, err)
	assert.NotEmpty(t, w.received(), "control: the write of a ServiceAccount is")
}

func TestCreateServiceAccountToken_RefusesAsNewTokenClaimsDecides(t *testing.T) {
	e := newTokenEnv(t)
	other := microvm("m2")
	other.Spec.ServiceAccountName = "other"
	other, err := e.api.PutMicroVM(ctx(t), other)
	require.NoError(t, err)

	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, 0, "gaon"))
	require.NoError(t, err, "control: the request that names the bound MicroVM and its account")

	wrongUID := tokenRequest(e.m1, 0, "gaon")
	wrongUID.Spec.BoundMicroVM.UID = "not-its-uid"
	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", wrongUID)
	assert.ErrorIs(t, err, api.ErrConflict, "the uid of another MicroVM of that name")

	otherByName := tokenRequest(other, 0, "gaon")
	otherByName.Spec.BoundMicroVM.UID = e.m1.Metadata.UID
	_, err = e.api.CreateServiceAccountToken(ctx(t), "other", otherByName)
	assert.ErrorIs(t, err, api.ErrConflict, "another MicroVM's name with m1's uid")

	_, err = e.api.CreateServiceAccountToken(ctx(t), "other", tokenRequest(e.m1, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrInvalid, "another service account than the MicroVM names")

	_, err = e.api.CreateServiceAccountToken(ctx(t), "missing", tokenRequest(e.m1, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrNotFound, "a service account that does not exist")
	gone := tokenRequest(e.m1, 0, "gaon")
	gone.Spec.BoundMicroVM.Name = "gone"
	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", gone)
	assert.ErrorIs(t, err, api.ErrNotFound, "a MicroVM that does not exist")
	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(e.m1, 0))
	assert.ErrorIs(t, err, api.ErrInvalid, "no audience")
}

func TestCreateServiceAccountToken_RefusesAMicroVMBeingDeleted(t *testing.T) {
	e := newTokenEnv(t)
	held := microvm("held")
	held.Spec.ServiceAccountName = "web"
	held.Metadata.Finalizers = []string{nodeAgentFinalizer}
	held, err := e.api.PutMicroVM(ctx(t), held)
	require.NoError(t, err)
	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(held, 0, "gaon"))
	require.NoError(t, err, "control: the MicroVM before its deletion")

	_, err = e.api.DeleteMicroVM(ctx(t), "held", held.Metadata.ResourceVersion)
	require.NoError(t, err)
	deleting, err := e.api.GetMicroVM(ctx(t), "held")
	require.NoError(t, err)
	require.NotNil(t, deleting.Metadata.DeletionTimestamp, "setup: its finalizer keeps it")

	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(deleting, 0, "gaon"))
	assert.ErrorIs(t, err, api.ErrConflict)
}

func TestCreateServiceAccountToken_IsRefusedWhereNoKeyIsConfigured(t *testing.T) {
	e := newEnv(t)
	_, err := e.api.PutServiceAccount(ctx(t), serviceAccount("web"))
	require.NoError(t, err, "control: the other kinds are served")
	m := microvm("m1")
	m.Spec.ServiceAccountName = "web"
	m, err = e.api.PutMicroVM(ctx(t), m)
	require.NoError(t, err)

	_, err = e.api.CreateServiceAccountToken(ctx(t), "web", tokenRequest(m, 0, "gaon"))
	var apiErr *api.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.Code)
}
