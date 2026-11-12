package apiserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/apiserver/internal/token"
	"github.com/garamsh/gaon/lib/api"
)

// startIssuing runs an API server on ln that signs as issuer with key and
// publishes and accepts the public keys in verifyFiles too. It returns a
// function that stops it.
func (c *cluster) startIssuing(t *testing.T, mode string, ln net.Listener, issuer string, key *signingKey, verifyFiles ...string) (stop func() error) {
	t.Helper()
	cfg := c.cfg
	cfg.AuthorizationMode = mode
	cfg.ServiceAccountIssuer, cfg.ServiceAccountSigningKeyFile = issuer, key.file
	cfg.ServiceAccountVerifyKeyFiles = verifyFiles
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// issuingServer starts one server that signs with a new key as the https URL
// of its own address, and returns that URL, the key and the server's address.
func (c *cluster) issuingServer(t *testing.T, mode string, verifyFiles ...string) (issuer string, key *signingKey, addr string) {
	t.Helper()
	ln := listenLoopback(t)
	addr = ln.Addr().String()
	issuer = "https://" + addr
	key = newSigningKey(t)
	c.startIssuing(t, mode, ln, issuer, key, verifyFiles...)
	return issuer, key, addr
}

// caClient is a client that trusts the server CA and presents no certificate.
// Every connection to the issuer's address goes to dial where it is set.
func (c *cluster) caClient(t *testing.T, dial string) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(c.serverCA.pem))
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	if dial != "" {
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, dial)
		}
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func publicKeyFile(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return writeFile(t, "verify-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// seedMicroVM creates the ServiceAccount web and its MicroVM m1 through
// compute, and returns m1.
func seedMicroVM(t *testing.T, compute *api.Client) *api.MicroVM {
	t.Helper()
	_, err := compute.PutServiceAccount(ctx(t), serviceAccount("web"))
	require.NoError(t, err)
	m := microvm("m1")
	m.Spec.ServiceAccountName = "web"
	m, err = compute.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	return m
}

func issueToken(t *testing.T, compute *api.Client, m *api.MicroVM, audience string) string {
	t.Helper()
	got, err := compute.CreateServiceAccountToken(ctx(t), "web", tokenRequest(m, 600, audience))
	require.NoError(t, err)
	return got.Status.Token
}

// forge signs claims with key, for a token the server would never issue.
func forge(t *testing.T, key *signingKey, issuer string, claims *api.TokenClaims) string {
	t.Helper()
	signer, err := token.NewSigner(issuer, key.key)
	require.NoError(t, err)
	out, err := signer.Sign(claims)
	require.NoError(t, err)
	return out
}

func get(t *testing.T, hc *http.Client, url string) (int, []byte) {
	t.Helper()
	resp, err := hc.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

func TestDiscovery_IsServedToACallerWithNoCredentials(t *testing.T) {
	c := newCluster(t)
	rotated := newSigningKey(t)
	issuer, _, addr := c.issuingServer(t, apiserver.ModeRBAC, publicKeyFile(t, rotated.pub))
	hc := c.caClient(t, "")

	status, body := get(t, hc, issuer+"/.well-known/openid-configuration")
	require.Equal(t, http.StatusOK, status)
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, issuer, doc.Issuer, "the issuer is the configured iss exactly")
	assert.Equal(t, issuer+"/openid/v1/jwks", doc.JWKSURI)

	status, body = get(t, hc, doc.JWKSURI)
	require.Equal(t, http.StatusOK, status)
	var set struct {
		Keys []struct{ Kid, Alg, Crv string } `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(body, &set))
	assert.Len(t, set.Keys, 2, "the signing key and the key kept for the rotation")
	for _, k := range set.Keys {
		assert.Equal(t, "ES256", k.Alg)
		assert.Equal(t, "P-256", k.Crv)
	}
	assert.NotContains(t, string(body), `"d"`, "no private part is published")

	status, _ = get(t, hc, "https://"+addr+apiRoot+"/microvms")
	assert.Equal(t, http.StatusUnauthorized, status, "control: an API route still needs a credential")
}

func TestDiscovery_IsNotServedWhereNoIssuerIsConfigured(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeAlwaysAllow)

	status, _ := get(t, c.caClient(t, ""), "https://"+addr+"/.well-known/openid-configuration")
	assert.Equal(t, http.StatusUnauthorized, status, "no document, and the path is not open to anonymous callers")
}

func TestDiscovery_AStandardLibraryVerifiesATokenFromTheDiscoveryURLAndTheCA(t *testing.T) {
	c := newCluster(t)
	issuer, key, addr := c.issuingServer(t, apiserver.ModeAlwaysAllow)
	compute, _ := c.clients(t, addr, adminID)
	m := seedMicroVM(t, compute)
	raw := issueToken(t, compute, m, "vault")

	oc := oidc.ClientContext(ctx(t), c.caClient(t, ""))
	provider, err := oidc.NewProvider(oc, issuer)
	require.NoError(t, err)
	verifier := provider.VerifierContext(oc, &oidc.Config{ClientID: "vault", SupportedSigningAlgs: []string{"ES256"}})

	idToken, err := verifier.Verify(oc, raw)
	require.NoError(t, err)
	assert.Equal(t, issuer, idToken.Issuer)
	assert.Equal(t, api.ServiceAccountSubject("web"), idToken.Subject)

	other := newSigningKey(t)
	claims, err := token.Verify(raw, key.pub)
	require.NoError(t, err)
	forged := forge(t, other, issuer, claims)
	_, err = verifier.Verify(oc, forged)
	assert.Error(t, err, "control: a token signed by another key is refused")
	_, err = verifier.Verify(oc, forge(t, key, issuer, claims))
	assert.NoError(t, err, "control: the same claims signed by the issuer's key are accepted")
}

func TestDiscovery_ATokenOfOneServerVerifiesAgainstTheKeysOfAnother(t *testing.T) {
	c := newCluster(t)
	lnA, lnB := listenLoopback(t), listenLoopback(t)
	issuer := "https://" + lnA.Addr().String()
	key := newSigningKey(t)
	stopA := c.startIssuing(t, apiserver.ModeAlwaysAllow, lnA, issuer, key)
	c.startIssuing(t, apiserver.ModeAlwaysAllow, lnB, issuer, key)

	compute, _ := c.clients(t, lnA.Addr().String(), adminID)
	m := seedMicroVM(t, compute)
	raw := issueToken(t, compute, m, "vault")
	require.NoError(t, stopA())

	oc := oidc.ClientContext(ctx(t), c.caClient(t, lnB.Addr().String()))
	provider, err := oidc.NewProvider(oc, issuer)
	require.NoError(t, err, "the discovery document comes from the other server")
	verifier := provider.VerifierContext(oc, &oidc.Config{ClientID: "vault", SupportedSigningAlgs: []string{"ES256"}})
	_, err = verifier.Verify(oc, raw)
	assert.NoError(t, err)

	otherKey := newSigningKey(t)
	claims, err := token.Verify(raw, key.pub)
	require.NoError(t, err)
	_, err = verifier.Verify(oc, forge(t, otherKey, issuer, claims))
	assert.Error(t, err, "control: a token signed by a key neither server holds")
}

// bearerFixture is an RBAC server whose ServiceAccount web is bound to a
// ClusterRole that holds get on microvms labeled app=web, and a bearer client
// of the token of its MicroVM m1.
type bearerFixture struct {
	c       *cluster
	issuer  string
	key     *signingKey
	addr    string
	compute *api.Client
	m1      *api.MicroVM
	raw     string
}

func newBearerFixture(t *testing.T, verifyFiles ...string) *bearerFixture {
	t.Helper()
	c := newCluster(t)
	f := &bearerFixture{c: c}
	f.issuer, f.key, f.addr = c.issuingServer(t, apiserver.ModeRBAC, verifyFiles...)
	var rbac *api.Client
	f.compute, rbac = c.clients(t, f.addr, adminID)
	f.m1 = seedMicroVM(t, f.compute)
	_, err := f.compute.PutMicroVM(ctx(t), labeled(microvm("web"), "app", "web"))
	require.NoError(t, err)
	_, err = f.compute.PutMicroVM(ctx(t), labeled(microvm("db"), "app", "db"))
	require.NoError(t, err)
	_, err = rbac.PutClusterRole(ctx(t), clusterRole("web-only",
		api.PolicyRule{APIGroups: []string{api.Group}, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbGet}, LabelSelector: "app=web"}))
	require.NoError(t, err)
	_, err = rbac.PutClusterRoleBinding(ctx(t), clusterRoleBinding("web-only", "web-only", userSubject(api.ServiceAccountSubject("web"))))
	require.NoError(t, err)
	f.raw = issueToken(t, f.compute, f.m1, "gaon")
	return f
}

// as is a client that presents raw as a bearer token and no certificate.
func (f *bearerFixture) as(t *testing.T, raw string) *api.Client {
	t.Helper()
	return api.NewClient("https://"+f.addr+apiRoot, f.c.caClient(t, ""), api.WithBearerToken(raw))
}

func TestBearer_AnOperatorIsHeldToItsLabelLimitedClusterRole(t *testing.T) {
	f := newBearerFixture(t)
	operator := f.as(t, f.raw)

	_, err := operator.GetMicroVM(ctx(t), "db")
	assert.ErrorIs(t, err, api.ErrForbidden, "authenticated, and held to the role's selector")
	_, err = operator.GetMicroVM(ctx(t), "web")
	assert.NoError(t, err, "control: an object inside the selector")
	_, err = operator.ListMicroVMs(ctx(t))
	assert.ErrorIs(t, err, api.ErrForbidden, "a verb the role does not grant")
}

func TestBearer_TheTokenOfADeletedMicroVMIsRefused(t *testing.T) {
	f := newBearerFixture(t)
	operator := f.as(t, f.raw)
	_, err := operator.GetMicroVM(ctx(t), "web")
	require.NoError(t, err, "control: the token holds while its MicroVM exists")

	_, err = f.compute.DeleteMicroVM(ctx(t), "m1", f.m1.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = operator.GetMicroVM(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrUnauthorized, "its MicroVM is gone")

	m := microvm("m1")
	m.Spec.ServiceAccountName = "web"
	_, err = f.compute.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	_, err = operator.GetMicroVM(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrUnauthorized, "a MicroVM of the same name has another uid")
}

func TestBearer_RefusesATokenThatDoesNotHold(t *testing.T) {
	f := newBearerFixture(t)
	valid, err := token.Verify(f.raw, f.key.pub)
	require.NoError(t, err)
	now := time.Now()
	expired := *valid
	expired.IssuedAt, expired.Expiry = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()

	elsewhere := *valid
	elsewhere.Issuer = "https://elsewhere.test"
	parts := strings.Split(f.raw, ".")
	require.Len(t, parts, 3)
	altered := *valid
	altered.Audience = []string{"another"}
	alteredJSON, err := json.Marshal(&altered)
	require.NoError(t, err)
	cases := map[string]string{
		"payload altered, its own key id and signature": parts[0] + "." + base64.RawURLEncoding.EncodeToString(alteredJSON) + "." + parts[2],
		"signed by another key":                         forge(t, newSigningKey(t), f.issuer, valid),
		"expired":                                       forge(t, f.key, f.issuer, &expired),
		"another issuer":                                forge(t, f.key, f.issuer, &elsewhere),
		"not a token":                                   "abc.def.ghi",
	}
	for name, raw := range cases {
		_, err := f.as(t, raw).GetMicroVM(ctx(t), "web")
		assert.ErrorIs(t, err, api.ErrUnauthorized, name)
	}
	_, err = f.as(t, forge(t, f.key, f.issuer, valid)).GetMicroVM(ctx(t), "web")
	assert.NoError(t, err, "control: the same claims signed by the configured key")
}

func TestBearer_ATokenIsNotAcceptedWhereNoIssuerIsConfigured(t *testing.T) {
	f := newBearerFixture(t)
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeAlwaysAllow)

	_, err := f.as(t, f.raw).GetMicroVM(ctx(t), "web")
	require.NoError(t, err, "control: the issuing server accepts it")
	other := api.NewClient("https://"+addr+apiRoot, c.caClient(t, ""), api.WithBearerToken(f.raw))
	_, err = other.GetMicroVM(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrUnauthorized)
}

func TestBearer_AKeyKeptForARotationVerifiesTheTokensItSigned(t *testing.T) {
	old := newSigningKey(t)
	f := newBearerFixture(t, publicKeyFile(t, old.pub))
	valid, err := token.Verify(f.raw, f.key.pub)
	require.NoError(t, err)
	oldToken := forge(t, old, f.issuer, valid)

	_, err = f.as(t, oldToken).GetMicroVM(ctx(t), "web")
	assert.NoError(t, err, "a token of the key kept for the rotation")
	_, err = f.as(t, f.raw).GetMicroVM(ctx(t), "web")
	assert.NoError(t, err, "control: a token of the signing key")

	plain := newBearerFixture(t)
	validPlain, err := token.Verify(plain.raw, plain.key.pub)
	require.NoError(t, err)
	_, err = plain.as(t, forge(t, old, plain.issuer, validPlain)).GetMicroVM(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrUnauthorized, "control: without the kept key the same token is refused")
}
