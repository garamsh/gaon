package certificate_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/node-agent/internal/certificate"
	"github.com/garamsh/gaon/node-agent/internal/fakepki"
)

func TestServingPair_AcceptsACertificateThatServesAndRefusesThoseThatCannot(t *testing.T) {
	ca := fakepki.New(t)
	other := fakepki.New(t)
	subject := fakepki.Subject{CN: "n1", Org: []string{api.NodeGroup}}
	both := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	cases := []struct {
		name    string
		ca      *fakepki.CA
		subject fakepki.Subject
		usages  []x509.ExtKeyUsage
		wantErr string
	}{
		{"control: chains, named for the node, both usages", ca, subject, both, ""},
		{"server auth missing", ca, subject, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, "server auth"},
		{"client auth missing", ca, subject, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "client auth"},
		{"another CN", ca, fakepki.Subject{CN: "n2"}, both, `want "n1"`},
		{"another CA", other, subject, both, "chain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, certPEM, keyPEM := c.ca.Issue(t, c.subject, c.usages...)
			certFile, keyFile := fakepki.Files(t, certPEM, keyPEM)
			pair, err := certificate.ServingPair(certFile, keyFile, ca.File(t), "n1", time.Now())
			if c.wantErr == "" {
				require.NoError(t, err)
				assert.NotNil(t, pair)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestHolderServerCertificate_ServesOnlyAPairWithServerAuth(t *testing.T) {
	r := newRig(t)
	r.gaon.SetSigning(9 * time.Hour)
	_, err := r.holder.ServerCertificate(nil)
	require.Error(t, err, "setup: no pair yet")
	assert.Contains(t, err.Error(), "no certificate")

	require.NoError(t, r.service(t, true).Start(context.Background()))
	pair, err := r.holder.ServerCertificate(nil)
	require.NoError(t, err, "control: the pair the agent asked for serves")
	assert.Equal(t, r.presented(t).SerialNumber, mustLeaf(t, pair).SerialNumber)

	old := newRig(t)
	certPEM, keyPEM := old.gaon.Issue(t, "n1", old.now.Add(-time.Hour), old.now.Add(time.Hour))
	old.seed(t, certPEM, keyPEM)
	require.NoError(t, old.service(t, false).Start(context.Background()))
	_, err = old.holder.ServerCertificate(nil)
	require.Error(t, err, "a pair issued with client auth only does not serve")
	assert.Contains(t, err.Error(), "server auth")
}

func mustLeaf(t *testing.T, pair *tls.Certificate) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	return leaf
}
