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

	"github.com/garamsh/gaon/apiserver"
)

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := apiserver.ParseConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8080", cfg.Listen)
	assert.Equal(t, []string{"http://127.0.0.1:2379"}, cfg.Endpoints)
	assert.Equal(t, 5*time.Second, cfg.DialTimeout)
}

func TestParseConfig_Flags(t *testing.T) {
	cfg, err := apiserver.ParseConfig([]string{"-etcd-endpoints", "http://a:1, http://b:2,", "-listen", "127.0.0.1:9", "-etcd-dial-timeout", "2s"})
	require.NoError(t, err)
	assert.Equal(t, []string{"http://a:1", "http://b:2"}, cfg.Endpoints)
	assert.Equal(t, "127.0.0.1:9", cfg.Listen)
	assert.Equal(t, 2*time.Second, cfg.DialTimeout)
}

func TestParseConfig_TLSFlags(t *testing.T) {
	cfg, err := apiserver.ParseConfig([]string{"-tls-client-ca", "ca.pem", "-tls-cert", "cert.pem", "-tls-key", "key.pem"})
	require.NoError(t, err)
	assert.Equal(t, "ca.pem", cfg.TLSClientCA)
	assert.Equal(t, "cert.pem", cfg.TLSCert)
	assert.Equal(t, "key.pem", cfg.TLSKey)
}

func TestParseConfig_Refuses(t *testing.T) {
	for name, args := range map[string][]string{
		"no endpoint":      {"-etcd-endpoints", " , "},
		"zero timeout":     {"-etcd-dial-timeout", "0s"},
		"negative timeout": {"-etcd-dial-timeout", "-1s"},
		"unknown flag":     {"-colour"},
		"only the CA":      {"-tls-client-ca", "ca.pem"},
		"no key":           {"-tls-client-ca", "ca.pem", "-tls-cert", "cert.pem"},
		"no CA":            {"-tls-cert", "cert.pem", "-tls-key", "key.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := apiserver.ParseConfig(args)
			assert.Error(t, err)
		})
	}
}

func TestRun_RefusesANonLoopbackListener(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer ln.Close()

	err = apiserver.Run(context.Background(), apiserver.Config{Endpoints: []string{"http://127.0.0.1:1"}, DialTimeout: time.Second}, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.ErrorContains(t, err, "loopback")
}

func TestRun_StopsWhenItsContextEnds(t *testing.T) {
	e := newEnv(t)
	_, err := e.api.ListMachines(ctx(t))
	require.NoError(t, err)

	require.NoError(t, e.stop())

	_, err = e.api.ListMachines(ctx(t))
	assert.Error(t, err, "nothing answers after the stop")
}

func TestRun_FailsWithoutEtcd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := free.Addr().String()
	free.Close()

	err = apiserver.Run(context.Background(), apiserver.Config{Endpoints: []string{"http://" + addr}, DialTimeout: time.Second}, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Error(t, err)
}
