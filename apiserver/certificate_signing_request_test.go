package apiserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

const (
	tokenA = "abcdef.0123456789abcdef"
	tokenB = "uvwxyz.fedcba9876543210"
)

// tokenFile has blank and indented lines, which the server ignores.
const tokenFile = "\n" + tokenA + "\n\n  " + tokenB + "  \n"

func pemRequest(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "node-1"}}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func csr(t *testing.T, name string) *api.CertificateSigningRequest {
	t.Helper()
	return &api.CertificateSigningRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindCertificateSigningRequest},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.CertificateSigningRequestSpec{Request: pemRequest(t), Username: "node-other"},
	}
}

// tokenClient is the client of a caller holding only token.
func (s *tlsServer) tokenClient(t *testing.T, token string) *api.Client {
	t.Helper()
	hc, err := api.NewServerTLSClient(s.caFile)
	require.NoError(t, err)
	return api.NewClient("https://"+s.addr+apiRoot, hc, api.WithBearerToken(token))
}

// certClient is the client of a caller holding a certificate with the CN cn.
func (s *tlsServer) certClient(t *testing.T, cn string) *api.Client {
	t.Helper()
	certPEM, keyPEM := s.clientCA.issue(t, cn)
	hc, err := api.NewTLSClient(s.caFile, writeFile(t, "cert.pem", certPEM), writeFile(t, "key.pem", keyPEM))
	require.NoError(t, err)
	return api.NewClient("https://"+s.addr+apiRoot, hc)
}

// do sends a raw request that presents no certificate, with the Authorization
// header authorization where it is not empty.
func (s *tlsServer) do(t *testing.T, authorization, method, path, body string) (int, string) {
	t.Helper()
	hc, err := api.NewServerTLSClient(s.caFile)
	require.NoError(t, err)
	hc.Timeout = 5 * time.Second
	req, err := http.NewRequest(method, "https://"+s.addr+apiRoot+path, strings.NewReader(body))
	require.NoError(t, err)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

func TestCertificateSigningRequest_ATokenHolderCreatesOneAndReadsItBack(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	holder := s.tokenClient(t, tokenA)
	sent := csr(t, "node-1")
	sent.Status = api.CertificateSigningRequestStatus{Certificate: "forged", Conditions: []api.Condition{{Type: api.CSRApproved, Status: "True"}}}

	created, err := holder.PutCertificateSigningRequest(ctx(t), sent)
	require.NoError(t, err)
	read, readErr := holder.GetCertificateSigningRequest(ctx(t), "node-1")

	assert.Equal(t, "bootstrap:abcdef", created.Spec.Username, "the username sent is replaced")
	assert.Empty(t, created.Status.Certificate, "the status sent is dropped")
	assert.Empty(t, created.Status.Conditions)
	require.NoError(t, readErr)
	assert.Equal(t, created, read)
	assert.Equal(t, sent.Spec.Request, read.Spec.Request)
}

func TestCertificateSigningRequest_TheSecondTokenInTheFileIsHonoured(t *testing.T) {
	s := newTokenServer(t, tokenFile)

	created, err := s.tokenClient(t, tokenB).PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))

	require.NoError(t, err)
	assert.Equal(t, "bootstrap:uvwxyz", created.Spec.Username)
}

func TestCertificateSigningRequest_ATokenHolderReadsOnlyARequestItCreated(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	a, b := s.tokenClient(t, tokenA), s.tokenClient(t, tokenB)
	_, err := a.PutCertificateSigningRequest(ctx(t), csr(t, "from-a"))
	require.NoError(t, err)
	_, err = s.certClient(t, "node-1").PutCertificateSigningRequest(ctx(t), csr(t, "from-cert"))
	require.NoError(t, err)

	_, otherToken := b.GetCertificateSigningRequest(ctx(t), "from-a")
	_, otherIdentity := a.GetCertificateSigningRequest(ctx(t), "from-cert")
	_, own := a.GetCertificateSigningRequest(ctx(t), "from-a")
	_, absent := a.GetCertificateSigningRequest(ctx(t), "nothing")

	assert.ErrorIs(t, otherToken, api.ErrForbidden)
	assert.ErrorIs(t, otherIdentity, api.ErrForbidden)
	require.NoError(t, own, "control: the creator reads its own")
	assert.ErrorIs(t, absent, api.ErrNotFound)
}

func TestCertificateSigningRequest_ATokenHolderIsForbiddenEverythingElse(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	bearer := "Bearer " + tokenA
	cases := []struct {
		name, method, path, body string
	}{
		{"list microvms", http.MethodGet, "/microvms", ""},
		{"put microvm", http.MethodPut, "/microvms/m1", `{}`},
		{"put node", http.MethodPut, "/nodes/n1", `{}`},
		{"list requests", http.MethodGet, "/certificatesigningrequests", ""},
		{"watch requests", http.MethodGet, "/certificatesigningrequests?watch=true&resourceVersion=1", ""},
		{"write a request's status", http.MethodPut, "/certificatesigningrequests/node-1/status", `{}`},
		{"delete a request", http.MethodDelete, "/certificatesigningrequests/node-1", ""},
		{"post to a request", http.MethodPost, "/certificatesigningrequests/node-1", ""},
		{"a path of no route", http.MethodGet, "/nothing", ""},
		{"a path outside the root", http.MethodGet, "/../nothing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := s.do(t, bearer, tc.method, tc.path, tc.body)

			assert.Equal(t, http.StatusForbidden, status)
			assert.Contains(t, body, `"reason":"Forbidden"`)
		})
	}
	_, err := s.tokenClient(t, tokenA).PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err, "control: the same token creates a request")
}

func TestCertificateSigningRequest_ARequestWithoutACertificateNeedsAValidToken(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	body := `{}`
	cases := map[string]string{
		"no token":                     "",
		"a token not in the file":      "Bearer qqqqqq.0123456789abcdef",
		"the right id, a wrong secret": "Bearer abcdef.0123456789abcdee",
		"a malformed token":            "Bearer not-a-token",
		"another scheme":               "Basic " + tokenA,
		"no scheme":                    tokenA,
	}
	for name, authorization := range cases {
		t.Run(name, func(t *testing.T) {
			status, resp := s.do(t, authorization, http.MethodPut, "/certificatesigningrequests/node-1", body)

			assert.Equal(t, http.StatusUnauthorized, status)
			assert.Contains(t, resp, `"reason":"Unauthorized"`)
		})
	}
	status, _ := s.do(t, "Bearer "+tokenA, http.MethodGet, "/certificatesigningrequests/node-1", "")
	assert.Equal(t, http.StatusNotFound, status, "control: a valid token is past the authentication")
}

func TestCertificateSigningRequest_AServerWithoutATokenFileHonoursNoToken(t *testing.T) {
	s := newTLSServer(t)

	status, _ := s.do(t, "Bearer "+tokenA, http.MethodPut, "/certificatesigningrequests/node-1", `{}`)
	controlStatus, _ := s.do(t, "", http.MethodPut, "/certificatesigningrequests/node-1", `{}`)

	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, http.StatusUnauthorized, controlStatus)
}

func TestCertificateSigningRequest_ACertificateHolderIsServedAsBefore(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	holder := s.certClient(t, "node-1")

	m, microvmErr := holder.PutMicroVM(ctx(t), microvm("m1"))
	created, err := holder.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	list, listErr := holder.ListCertificateSigningRequests(ctx(t))

	require.NoError(t, microvmErr)
	assert.Equal(t, "m1", m.Metadata.Name)
	require.NoError(t, err)
	assert.Equal(t, "node-1", created.Spec.Username, "the CN is the identity")
	require.NoError(t, listErr)
	require.Len(t, list.Items, 1)
	assert.Equal(t, created, &list.Items[0])
}

func TestCertificateSigningRequest_ACertificateHolderWatchesRequests(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	holder := s.certClient(t, "controllers")
	list, err := holder.ListCertificateSigningRequests(ctx(t))
	require.NoError(t, err)
	w, err := holder.WatchCertificateSigningRequests(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()

	_, err = s.tokenClient(t, tokenA).PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err)

	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "bootstrap:abcdef", ev.Object.Spec.Username)
}

func TestCertificateSigningRequest_APutThatCarriesAVersionIsInvalid(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	holder := s.certClient(t, "node-1")
	created, err := holder.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err)

	changed := *created
	changed.Spec.Request = pemRequest(t)
	_, err = holder.PutCertificateSigningRequest(ctx(t), &changed)
	absent := csr(t, "absent")
	absent.Metadata.ResourceVersion = "1"
	_, absentErr := holder.PutCertificateSigningRequest(ctx(t), absent)
	_, duplicateErr := holder.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	read, readErr := holder.GetCertificateSigningRequest(ctx(t), "node-1")

	assert.ErrorIs(t, err, api.ErrInvalid)
	assert.ErrorIs(t, absentErr, api.ErrInvalid)
	assert.ErrorIs(t, duplicateErr, api.ErrAlreadyExists, "control: a put without a version is a create")
	require.NoError(t, readErr)
	assert.Equal(t, created, read, "the stored request is unchanged")
}

func TestCertificateSigningRequest_TheSignerWritesTheStatusAndNotTheSpec(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	signer := s.certClient(t, "controllers")
	created, err := s.tokenClient(t, tokenA).PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err)

	answer := *created
	answer.Spec.Username = "node-other"
	answer.Status = api.CertificateSigningRequestStatus{
		Conditions:  []api.Condition{{Type: api.CSRApproved, Status: "True", Reason: "NodeAgent"}},
		Certificate: "PEM",
	}
	written, err := signer.PutCertificateSigningRequestStatus(ctx(t), &answer)
	require.NoError(t, err)
	read, err := s.tokenClient(t, tokenA).GetCertificateSigningRequest(ctx(t), "node-1")
	require.NoError(t, err)

	assert.Equal(t, "PEM", read.Status.Certificate)
	require.Len(t, read.Status.Conditions, 1)
	assert.Equal(t, api.CSRApproved, read.Status.Conditions[0].Type)
	assert.Equal(t, "bootstrap:abcdef", read.Spec.Username, "a status write keeps the spec")
	assert.Equal(t, written.Metadata.ResourceVersion, read.Metadata.ResourceVersion)

	_, staleErr := signer.PutCertificateSigningRequestStatus(ctx(t), &answer)
	assert.ErrorIs(t, staleErr, api.ErrConflict, "a status write is a compare-and-swap")
}

func TestCertificateSigningRequest_AStatusWriteIsValidated(t *testing.T) {
	s := newTokenServer(t, tokenFile)
	signer := s.certClient(t, "controllers")
	created, err := signer.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))
	require.NoError(t, err)

	unknownType := *created
	unknownType.Status.Conditions = []api.Condition{{Type: "Ready", Status: "True"}}
	_, typeErr := signer.PutCertificateSigningRequestStatus(ctx(t), &unknownType)
	unknownStatus := *created
	unknownStatus.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: "Maybe"}}
	_, statusErr := signer.PutCertificateSigningRequestStatus(ctx(t), &unknownStatus)
	valid := *created
	valid.Status.Conditions = []api.Condition{{Type: api.CSRDenied, Status: "True"}}
	_, validErr := signer.PutCertificateSigningRequestStatus(ctx(t), &valid)

	assert.ErrorIs(t, typeErr, api.ErrInvalid)
	assert.ErrorIs(t, statusErr, api.ErrInvalid)
	require.NoError(t, validErr, "control: a known type and status is stored")
}

func TestCertificateSigningRequest_PlainHTTPAuthenticatesNobody(t *testing.T) {
	e := newEnv(t)
	// A token sent over plain HTTP is not honoured: no identity comes of it.
	req, err := http.NewRequest(http.MethodGet, e.base+apiRoot+"/microvms", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	created, err := e.api.PutCertificateSigningRequest(ctx(t), csr(t, "node-1"))

	assert.Equal(t, http.StatusOK, resp.StatusCode, "no authentication is asked of a loopback server")
	require.NoError(t, err)
	assert.Empty(t, created.Spec.Username, "no one is authenticated, whatever the request sent")
}

func TestRun_RefusesATokenFileThatDoesNotLoad(t *testing.T) {
	ca := newAuthority(t, "ca")
	certPEM, keyPEM := ca.issue(t, "gaon-apiserver")
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(dir, "tokens")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	good := apiserver.Config{
		Endpoints:          []string{"http://127.0.0.1:1"},
		DialTimeout:        time.Second,
		TLSClientCA:        writeFile(t, "ca.pem", ca.pem),
		TLSCert:            writeFile(t, "cert.pem", certPEM),
		TLSKey:             writeFile(t, "key.pem", keyPEM),
		BootstrapTokenFile: write(tokenFile),
	}
	for name, tc := range map[string]struct {
		mutate func(*apiserver.Config)
		want   string
	}{
		"missing file":            {func(c *apiserver.Config) { c.BootstrapTokenFile = filepath.Join(dir, "none") }, "read bootstrap token file"},
		"a line that is no token": {func(c *apiserver.Config) { c.BootstrapTokenFile = write(tokenA + "\n\nnot-a-token\n") }, "line 3"},
		"an uppercase token":      {func(c *apiserver.Config) { c.BootstrapTokenFile = write("ABCDEF.0123456789abcdef\n") }, "line 1"},
		"a short secret":          {func(c *apiserver.Config) { c.BootstrapTokenFile = write("abcdef.0123\n") }, "line 1"},
		"two tokens on a line":    {func(c *apiserver.Config) { c.BootstrapTokenFile = write(tokenA + " " + tokenB + "\n") }, "line 1"},
		"no TLS files":            {func(c *apiserver.Config) { c.TLSClientCA, c.TLSCert, c.TLSKey = "", "", "" }, "over TLS only"},
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()
			cfg := good
			tc.mutate(&cfg)

			err = apiserver.Run(context.Background(), cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

			assert.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "0123456789abcdef", "the error does not print a secret")
		})
	}
}

func TestParseConfig_RefusesATokenFileWithoutTheTLSFiles(t *testing.T) {
	_, err := apiserver.ParseConfig([]string{"-bootstrap-token-file", "tokens"})
	cfg, controlErr := apiserver.ParseConfig([]string{"-bootstrap-token-file", "tokens", "-tls-client-ca", "ca.pem", "-tls-cert", "cert.pem", "-tls-key", "key.pem"})

	assert.ErrorContains(t, err, "over TLS only")
	require.NoError(t, controlErr, "control: the same flag with the TLS files parses")
	assert.Equal(t, "tokens", cfg.BootstrapTokenFile)
}
