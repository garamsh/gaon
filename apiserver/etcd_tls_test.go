package apiserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/pkg/v3/transport"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// tlsEtcd is an embedded etcd member that serves clients over TLS and accepts
// only a client certificate of its CA.
type tlsEtcd struct {
	endpoint string
	ca       *authority
	// caFile and the two files after it are the client side's files, as
	// apiserver.Config names them.
	caFile, certFile, keyFile string
}

func startTLSEtcd(t *testing.T) *tlsEtcd {
	t.Helper()
	ca := newAuthority(t, "etcd ca")
	serverCert, serverKey := ca.issue(t, "etcd")
	clientCert, clientKey := ca.issue(t, "gaon-apiserver")
	e := &tlsEtcd{
		ca:       ca,
		caFile:   writeFile(t, "etcd-ca.pem", ca.pem),
		certFile: writeFile(t, "etcd-client.pem", clientCert),
		keyFile:  writeFile(t, "etcd-client-key.pem", clientKey),
	}
	e.endpoint = startEtcdWith(t, &transport.TLSInfo{
		CertFile:       writeFile(t, "etcd-server.pem", serverCert),
		KeyFile:        writeFile(t, "etcd-server-key.pem", serverKey),
		TrustedCAFile:  e.caFile,
		ClientCertAuth: true,
	})
	return e
}

// config is the API server's configuration over e, with the etcd files it
// would carry.
func (e *tlsEtcd) config() apiserver.Config {
	return apiserver.Config{
		Endpoints:   []string{e.endpoint},
		DialTimeout: 2 * time.Second,
		EtcdCA:      e.caFile,
		EtcdCert:    e.certFile,
		EtcdKey:     e.keyFile,
	}
}

func runLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRun_ServesOverEtcdTLS(t *testing.T) {
	etcd := startTLSEtcd(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(runCtx, etcd.config(), ln, runLogger()) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	client := api.NewClient("http://"+ln.Addr().String()+apiRoot, nil)

	created, err := client.PutMicroVM(ctx(t), microvm("over-tls"))
	require.NoError(t, err)

	got, err := client.GetMicroVM(ctx(t), "over-tls")
	require.NoError(t, err)
	assert.Equal(t, created.Metadata.ResourceVersion, got.Metadata.ResourceVersion)
}

func TestRun_RefusesEtcdItCannotTrust(t *testing.T) {
	etcd := startTLSEtcd(t)
	stranger := newAuthority(t, "stranger ca")
	strangerCert, strangerKey := stranger.issue(t, "gaon-apiserver")
	wrongCA := etcd.config()
	wrongCA.EtcdCA = writeFile(t, "stranger-ca.pem", stranger.pem)
	wrongClient := etcd.config()
	wrongClient.EtcdCert = writeFile(t, "stranger.pem", strangerCert)
	wrongClient.EtcdKey = writeFile(t, "stranger-key.pem", strangerKey)

	for name, tc := range map[string]struct {
		cfg     apiserver.Config
		refused bool
	}{
		"the etcd CA and a client certificate it signed": {etcd.config(), false},
		"a CA that did not sign etcd's certificate":      {wrongCA, true},
		"a client certificate etcd's CA did not sign":    {wrongClient, true},
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- apiserver.Run(runCtx, tc.cfg, ln, runLogger()) }()

			if tc.refused {
				select {
				case err := <-done:
					assert.ErrorContains(t, err, "reach etcd")
				case <-time.After(20 * time.Second):
					t.Fatal("Run did not refuse")
				}
				return
			}
			_, err = api.NewClient("http://"+ln.Addr().String()+apiRoot, nil).ListMicroVMs(ctx(t))
			require.NoError(t, err)
			cancel()
			require.NoError(t, <-done)
		})
	}
}
