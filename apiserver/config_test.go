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

func TestParseConfig_EtcdTLSFlags(t *testing.T) {
	cfg, err := apiserver.ParseConfig([]string{"-etcd-ca-file", "ca.pem", "-etcd-cert-file", "cert.pem", "-etcd-key-file", "key.pem"})
	require.NoError(t, err)
	assert.Equal(t, "ca.pem", cfg.EtcdCA)
	assert.Equal(t, "cert.pem", cfg.EtcdCert)
	assert.Equal(t, "key.pem", cfg.EtcdKey)
}

func TestParseConfig_EtcdTLSNamesTheMissingFlag(t *testing.T) {
	all := map[string]string{"etcd-ca-file": "ca.pem", "etcd-cert-file": "cert.pem", "etcd-key-file": "key.pem"}
	for missing := range all {
		t.Run(missing, func(t *testing.T) {
			var args []string
			for flag, file := range all {
				if flag != missing {
					args = append(args, "-"+flag, file)
				}
			}
			_, err := apiserver.ParseConfig(args)
			assert.ErrorContains(t, err, "missing "+missing)
		})
	}
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
		"issuer, no key":   {"-service-account-issuer", "https://gaon"},
		"key, no issuer":   {"-service-account-signing-key-file", "key.pem"},
		"verify keys only": {"-service-account-verify-key-files", "old.pem"},
		"audiences only":   {"-api-audiences", "openbao"},
		"http issuer":      {"-service-account-issuer", "http://gaon", "-service-account-signing-key-file", "key.pem"},
		"issuer with path": {"-service-account-issuer", "https://gaon/path", "-service-account-signing-key-file", "key.pem"},
		"issuer, query":    {"-service-account-issuer", "https://gaon?a=b", "-service-account-signing-key-file", "key.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := apiserver.ParseConfig(args)
			assert.Error(t, err)
		})
	}
}

func TestParseConfig_TokenSigningFlags(t *testing.T) {
	cfg, err := apiserver.ParseConfig([]string{"-service-account-issuer", "https://gaon", "-service-account-signing-key-file", "key.pem"})
	require.NoError(t, err)
	assert.Equal(t, "https://gaon", cfg.ServiceAccountIssuer)
	assert.Equal(t, "key.pem", cfg.ServiceAccountSigningKeyFile)
	assert.Empty(t, cfg.ServiceAccountVerifyKeyFiles)

	cfg, err = apiserver.ParseConfig([]string{"-service-account-issuer", "https://gaon:6443", "-service-account-signing-key-file", "key.pem", "-service-account-verify-key-files", "a.pem, b.pem"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.pem", "b.pem"}, cfg.ServiceAccountVerifyKeyFiles)

	cfg, err = apiserver.ParseConfig([]string{"-service-account-issuer", "https://gaon", "-service-account-signing-key-file", "key.pem", "-api-audiences", "openbao, gaon"})
	require.NoError(t, err)
	assert.Equal(t, []string{"openbao", "gaon"}, cfg.APIAudiences)
}

func TestRun_RefusesAVerificationKeyThatIsNotAnECPublicKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	signing := newSigningKey(t)
	bad := writeFile(t, "bad.pem", []byte("not a key"))
	cfg := apiserver.Config{Endpoints: []string{"http://127.0.0.1:1"}, DialTimeout: time.Second, ServiceAccountIssuer: "https://gaon", ServiceAccountSigningKeyFile: signing.file, ServiceAccountVerifyKeyFiles: []string{bad}}

	err = apiserver.Run(context.Background(), cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.ErrorContains(t, err, "token verification key")
}

func TestRun_RefusesATokenSigningKeyThatIsNotAnECKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	bad := writeFile(t, "bad.pem", []byte("not a key"))
	cfg := apiserver.Config{Endpoints: []string{"http://127.0.0.1:1"}, DialTimeout: time.Second, ServiceAccountIssuer: "https://gaon", ServiceAccountSigningKeyFile: bad}

	err = apiserver.Run(context.Background(), cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.ErrorContains(t, err, "token signing key")
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
	_, err := e.api.ListMicroVMs(ctx(t))
	require.NoError(t, err)

	require.NoError(t, e.stop())

	_, err = e.api.ListMicroVMs(ctx(t))
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

func TestParseConfig_AddressRangeFlags(t *testing.T) {
	cfg, err := apiserver.ParseConfig([]string{"-service-cluster-ip-range", "10.96.0.0/12", "-load-balancer-pool", "192.168.50.0/28"})
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.0/12", cfg.ServiceClusterIPRange)
	assert.Equal(t, "192.168.50.0/28", cfg.LoadBalancerPool)

	cfg, err = apiserver.ParseConfig(nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.ServiceClusterIPRange, "control: a range not given stays empty")
	assert.Empty(t, cfg.LoadBalancerPool)
}

func TestParseConfig_RefusesARangeThatIsNotOneAndTwoThatOverlap(t *testing.T) {
	for name, args := range map[string][]string{
		"not a CIDR":           {"-service-cluster-ip-range", "10.96.0.0"},
		"host bits":            {"-load-balancer-pool", "192.168.50.5/28"},
		"loopback":             {"-service-cluster-ip-range", "127.0.0.0/8"},
		"link-local":           {"-load-balancer-pool", "169.254.0.0/16"},
		"identical":            {"-service-cluster-ip-range", "10.96.0.0/12", "-load-balancer-pool", "10.96.0.0/12"},
		"pool inside prefix":   {"-service-cluster-ip-range", "10.96.0.0/12", "-load-balancer-pool", "10.97.0.0/24"},
		"prefix inside pool":   {"-service-cluster-ip-range", "192.168.50.0/28", "-load-balancer-pool", "192.168.0.0/16"},
		"overlap, IPv6 ranges": {"-service-cluster-ip-range", "fd00:1::/64", "-load-balancer-pool", "fd00:1::/96"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := apiserver.ParseConfig(args)
			assert.Error(t, err)
		})
	}
	_, err := apiserver.ParseConfig([]string{"-service-cluster-ip-range", "10.96.0.0/12", "-load-balancer-pool", "192.168.50.0/28"})
	assert.NoError(t, err, "control: two ranges that do not overlap")
	_, err = apiserver.ParseConfig([]string{"-service-cluster-ip-range", "10.96.0.0/12", "-load-balancer-pool", "fd00:1::/64"})
	assert.NoError(t, err, "control: ranges of two families do not overlap")
}

func TestRun_RefusesOverlappingRangesAtStart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	cfg := apiserver.Config{Endpoints: []string{"http://127.0.0.1:1"}, DialTimeout: time.Second, ServiceClusterIPRange: "10.96.0.0/12", LoadBalancerPool: "10.96.1.0/24"}

	err = apiserver.Run(context.Background(), cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.ErrorContains(t, err, "overlap")
}
