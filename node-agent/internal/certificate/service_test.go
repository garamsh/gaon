package certificate_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
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

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/clock"
	"github.com/garamsh/gaon/node-agent/internal/fakegaon"
)

const token = "abcdef.0123456789abcdef"

// gate is a Clock whose Sleep reports the duration asked and waits for the
// test to let it go, so a test reads what the service waits for and when.
type gate struct {
	*clock.Fake
	slept   chan time.Duration
	release chan struct{}
}

func (g *gate) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case g.slept <- d:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.Fake.Sleep(ctx, d)
}

// waitSleep returns the duration of the next Sleep, which stays blocked until
// proceed.
func (g *gate) waitSleep(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-g.slept:
		return d
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the service did not sleep")
		return 0
	}
}

func (g *gate) proceed() { g.release <- struct{}{} }

type rig struct {
	clock  *gate
	gaon   *fakegaon.Server
	dir    string
	holder *certificate.Holder
	now    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	g := &gate{Fake: clock.NewFake(now), slept: make(chan time.Duration), release: make(chan struct{})}
	gaon := fakegaon.NewTLS(t, g)
	gaon.AcceptToken(token)
	return &rig{clock: g, gaon: gaon, dir: filepath.Join(t.TempDir(), "certs"), holder: &certificate.Holder{}, now: now}
}

// service builds the Service as the agent's composition site does: the
// bootstrap requests carry the token and no certificate, the renewal requests
// present the pair.
func (r *rig) service(t *testing.T, withToken bool) certificate.Service {
	t.Helper()
	mtls, err := api.NewTLSClientFunc(r.gaon.CAFile(), r.holder.ClientCertificate)
	require.NoError(t, err)
	var bootstrap certificate.Requests
	if withToken {
		bare, err := api.NewServerTLSClient(r.gaon.CAFile())
		require.NoError(t, err)
		bootstrap = api.NewClient(r.gaon.URL, bare, api.WithBearerToken(token))
	}
	return certificate.NewService(certificate.Params{NodeName: "n1", Dir: r.dir}, r.holder, bootstrap,
		api.NewClient(r.gaon.URL, mtls), r.clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// run starts the renewal loop and stops it when the test ends.
func run(t *testing.T, svc certificate.Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) seed(t *testing.T, certPEM, keyPEM []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(r.dir, 0o700))
	if certPEM != nil || keyPEM != nil {
		data := append(append([]byte{}, certPEM...), keyPEM...)
		require.NoError(t, os.WriteFile(filepath.Join(r.dir, certificate.PairFile), data, 0o600))
	}
}

func readLeaf(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, certificate.PairFile))
	require.NoError(t, err)
	block, _ := pem.Decode(b)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return leaf
}

func (r *rig) presented(t *testing.T) *x509.Certificate {
	t.Helper()
	pair, err := r.holder.ClientCertificate(nil)
	require.NoError(t, err)
	if len(pair.Certificate) == 0 {
		return nil
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	return leaf
}

func TestStart_BootstrapsWithTheTokenAndKeepsTheIssuedPairOnDisk(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	require.NoDirExists(t, r.dir, "setup: the directory is missing at the start")

	require.NoError(t, r.service(t, true).Start(context.Background()))

	dir, err := os.Stat(r.dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm(), "the directory is created with mode 0700")
	pair, err := os.Stat(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), pair.Mode().Perm(), "the pair is written with mode 0600")
	entries, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the pair file is all the directory holds: no temporary file is left")

	creates := r.gaon.CSRCreates()
	require.Len(t, creates, 1)
	assert.Regexp(t, `^n1-[0-9a-f]{16}$`, creates[0].Name)
	assert.Equal(t, "bootstrap:abcdef", creates[0].Username)
	assert.Equal(t, "n1", creates[0].Request.Subject.CommonName)
	assert.Equal(t, []string{api.NodeGroup}, creates[0].Request.Subject.Organization, "the request names a Node by its Organization")
	assert.Len(t, creates[0].Request.Subject.Names, 2, "and by nothing else but the CN")
	req := creates[0].Request
	assert.Empty(t, req.DNSNames)
	assert.Empty(t, req.IPAddresses)
	assert.Empty(t, req.EmailAddresses)
	assert.Empty(t, req.URIs)
	pub, ok := req.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok, "the request carries an ECDSA key")
	assert.Equal(t, elliptic.P256(), pub.Curve)
	for _, s := range r.gaon.Seen() {
		assert.Equal(t, token, s.Token, "every bootstrap call carries the token")
		assert.Empty(t, s.ClientCN, "and no certificate")
	}

	leaf := readLeaf(t, r.dir)
	assert.Equal(t, "n1", leaf.Subject.CommonName)
	assert.Equal(t, []string{api.NodeGroup}, leaf.Subject.Organization)
	assert.Equal(t, leaf.SerialNumber, r.presented(t).SerialNumber, "the pair on disk is the pair presented")
}

func TestStart_UsesAValidPairWithoutAToken(t *testing.T) {
	r := newRig(t)
	certPEM, keyPEM := r.gaon.Issue(t, "n1", r.now.Add(-time.Hour), r.now.Add(time.Hour))
	r.seed(t, certPEM, keyPEM)

	require.NoError(t, r.service(t, false).Start(context.Background()))

	assert.Empty(t, r.gaon.CSRCreates(), "no request is made")
	assert.Equal(t, readLeaf(t, r.dir).SerialNumber, r.presented(t).SerialNumber)
}

func TestStart_RefusesAnInvalidPairWithoutATokenAndBootstrapsWithOne(t *testing.T) {
	_, otherKey := newRig(t).gaon.Issue(t, "n1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	tests := []struct {
		name    string
		pair    func(r *rig) (certPEM, keyPEM []byte)
		wantErr string
	}{
		{"no files", func(*rig) ([]byte, []byte) { return nil, nil }, "read pair"},
		{"no key", func(r *rig) ([]byte, []byte) {
			c, _ := r.gaon.Issue(t, "n1", r.now.Add(-time.Hour), r.now.Add(time.Hour))
			return c, nil
		}, "certificate and key"},
		{"unparsable files", func(*rig) ([]byte, []byte) { return []byte("x"), []byte("y") }, "certificate and key"},
		{"a key that is not the certificate's", func(r *rig) ([]byte, []byte) {
			c, _ := r.gaon.Issue(t, "n1", r.now.Add(-time.Hour), r.now.Add(time.Hour))
			return c, otherKey
		}, "certificate and key"},
		{"a certificate for another node", func(r *rig) ([]byte, []byte) {
			return r.gaon.Issue(t, "n2", r.now.Add(-time.Hour), r.now.Add(time.Hour))
		}, `want "n1"`},
		{"an expired certificate", func(r *rig) ([]byte, []byte) {
			return r.gaon.Issue(t, "n1", r.now.Add(-2*time.Hour), r.now.Add(-time.Hour))
		}, "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			certPEM, keyPEM := tt.pair(r)
			r.seed(t, certPEM, keyPEM)

			err := r.service(t, false).Start(context.Background())
			require.ErrorContains(t, err, tt.wantErr, "with no token the start is refused for the pair's fault")
			assert.Empty(t, r.gaon.CSRCreates())

			require.NoError(t, r.service(t, true).Start(context.Background()), "with a token the agent bootstraps")
			assert.Len(t, r.gaon.CSRCreates(), 1)
			assert.Equal(t, readLeaf(t, r.dir).SerialNumber, r.presented(t).SerialNumber, "and keeps what it received")
		})
	}
}

func TestStart_ADeniedRequestStopsTheStartAndWritesNothing(t *testing.T) {
	r := newRig(t)
	r.gaon.DenyRequests("NotANode")

	err := r.service(t, true).Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	assert.Contains(t, err.Error(), "NotANode", "the reason is in the error")
	assert.Nil(t, r.presented(t))
	assert.NoFileExists(t, filepath.Join(r.dir, certificate.PairFile))
}

func TestStart_WaitsForTheSignersDecisionReadingTheRequestEverySecond(t *testing.T) {
	r := newRig(t)
	r.gaon.HoldRequests(true)
	svc := r.service(t, true)
	started := make(chan error, 1)
	go func() { started <- svc.Start(context.Background()) }()

	assert.Equal(t, time.Second, r.clock.waitSleep(t), "the request is read again after a second")
	assert.GreaterOrEqual(t, r.gaon.CSRReads(), 1)
	select {
	case err := <-started:
		require.FailNow(t, "the start returned before the decision", "%v", err)
	default:
	}

	r.gaon.HoldRequests(false)
	r.clock.proceed()

	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the start did not return after the decision")
	}
	assert.NotNil(t, r.presented(t))
}

func TestRun_RenewsAtTwoThirdsOfTheValidityOverItsOwnCertificateWithAFreshKey(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	svc := r.service(t, true)
	require.NoError(t, svc.Start(context.Background()))
	first := r.presented(t)
	run(t, svc)

	assert.Equal(t, 6*time.Hour, r.clock.waitSleep(t), "the renewal is due two thirds into nine hours")
	require.Len(t, r.gaon.CSRCreates(), 1, "nothing is requested before it is due")
	r.clock.proceed()

	assert.Equal(t, 6*time.Hour, r.clock.waitSleep(t), "the new certificate is renewed at its own two thirds")
	creates := r.gaon.CSRCreates()
	require.Len(t, creates, 2)
	assert.Equal(t, "n1", creates[1].Username, "the renewal is made as the node, not as a token")
	assert.Regexp(t, `^n1-[0-9a-f]{16}$`, creates[1].Name)
	assert.NotEqual(t, creates[0].Name, creates[1].Name)
	assert.False(t, creates[0].Request.PublicKey.(*ecdsa.PublicKey).Equal(creates[1].Request.PublicKey), "each request has a key of its own")
	var renewal fakegaon.Seen
	for _, s := range r.gaon.Seen() {
		if s.Method == "PUT" && s.Path == "/certificatesigningrequests/"+creates[1].Name {
			renewal = s
		}
	}
	assert.Equal(t, "n1", renewal.ClientCN, "the server saw the client certificate")
	assert.Equal(t, first.SerialNumber.String(), renewal.ClientSerial, "the one the agent held")
	assert.Empty(t, renewal.Token, "and no token")
	assert.NotEqual(t, first.SerialNumber, r.presented(t).SerialNumber, "the new pair is presented")
	assert.Equal(t, r.presented(t).SerialNumber, readLeaf(t, r.dir).SerialNumber, "and kept on disk")
}

func TestRun_ADeniedRenewalKeepsThePairAndTriesAgainAfterAMinute(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	svc := r.service(t, true)
	require.NoError(t, svc.Start(context.Background()))
	first := r.presented(t)
	before, err := os.ReadFile(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	run(t, svc)

	r.clock.waitSleep(t)
	r.gaon.DenyRequests("NotANode")
	r.clock.proceed()

	assert.Equal(t, time.Minute, r.clock.waitSleep(t), "a failed renewal is retried after a minute")
	require.Len(t, r.gaon.CSRCreates(), 2, "the renewal was asked for")
	assert.Equal(t, first.SerialNumber, r.presented(t).SerialNumber, "the pair in use stays")
	after, err := os.ReadFile(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the pair on disk is the old one")

	r.gaon.SetSigning(9 * time.Hour)
	r.clock.proceed()
	assert.Equal(t, 6*time.Hour, r.clock.waitSleep(t), "the retry renews, and the next one is due two thirds on")
	assert.NotEqual(t, first.SerialNumber, r.presented(t).SerialNumber, "control: the same agent replaces the pair once a certificate arrives")
}

func TestRun_RenewsAtOnceWhenThePairIsPastTwoThirdsAtStart(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	certPEM, keyPEM := r.gaon.Issue(t, "n1", r.now.Add(-7*time.Hour), r.now.Add(2*time.Hour))
	r.seed(t, certPEM, keyPEM)
	svc := r.service(t, false)
	require.NoError(t, svc.Start(context.Background()))
	run(t, svc)

	// The loop sleeps before it asks only when the renewal is not yet due, and
	// nothing here receives a Sleep, so a request proves it did not sleep.
	require.Eventually(t, func() bool { return len(r.gaon.CSRCreates()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "n1", r.gaon.CSRCreates()[0].Username)
}

func TestRun_ACertificateIssuedBeforeTheOrganizationRenewsIntoItWithoutARestart(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	certPEM, keyPEM := r.gaon.Issue(t, "n1", r.now.Add(-7*time.Hour), r.now.Add(2*time.Hour))
	r.seed(t, certPEM, keyPEM)
	svc := r.service(t, false)
	require.NoError(t, svc.Start(context.Background()))
	before := r.presented(t)
	require.NotNil(t, before)
	require.Empty(t, before.Subject.Organization, "setup: the pair in use predates the Organization")
	run(t, svc)

	require.Eventually(t, func() bool {
		cert := r.presented(t)
		return cert.SerialNumber.Cmp(before.SerialNumber) != 0
	}, 5*time.Second, 5*time.Millisecond, "the renewed pair is presented by the same service")
	after := r.presented(t)
	assert.Equal(t, "n1", after.Subject.CommonName)
	assert.Equal(t, []string{api.NodeGroup}, after.Subject.Organization)
	assert.Equal(t, []string{api.NodeGroup}, readLeaf(t, r.dir).Subject.Organization, "and kept on disk")
	creates := r.gaon.CSRCreates()
	require.Len(t, creates, 1)
	assert.Equal(t, "n1", creates[0].Username, "the renewal is made as the old certificate's CN")
	assert.Equal(t, []string{api.NodeGroup}, creates[0].Request.Subject.Organization)
}

func TestRun_DoesNotRenewBeforeTwoThirds(t *testing.T) {
	r := newRig(t)
	certPEM, keyPEM := r.gaon.Issue(t, "n1", r.now.Add(-5*time.Hour), r.now.Add(4*time.Hour))
	r.seed(t, certPEM, keyPEM)
	svc := r.service(t, false)
	require.NoError(t, svc.Start(context.Background()))
	run(t, svc)

	assert.Equal(t, time.Hour, r.clock.waitSleep(t), "the pair is five of nine hours old, so one hour remains until the two thirds")
	assert.Empty(t, r.gaon.CSRCreates())
}

func TestRun_ReplacesThePairFileByRenameSoAFileInUseIsNeverRewritten(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	svc := r.service(t, true)
	require.NoError(t, svc.Start(context.Background()))
	oldPair, err := os.Open(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = oldPair.Close() })
	before, err := os.ReadFile(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	run(t, svc)

	r.clock.waitSleep(t)
	r.clock.proceed()
	r.clock.waitSleep(t)

	after, err := os.ReadFile(filepath.Join(r.dir, certificate.PairFile))
	require.NoError(t, err)
	require.NotEqual(t, before, after, "setup: the pair on disk was replaced")
	held, err := io.ReadAll(oldPair)
	require.NoError(t, err)
	assert.Equal(t, before, held, "a handle opened before the renewal still reads the old pair: the file was renamed over, not written in place")
	entries, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left")
}
