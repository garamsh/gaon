package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"
)

const (
	testBucket = "gaon-backups"
	testPrefix = "etcd/"
	// fixedNowKey is fixedNow as an object key writes it.
	fixedNowKey = "20260102T030405Z"
)

// fixedNow is the time the run's clock reads.
var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return fixedNow }

// env is an embedded etcd member serving TLS to the clients of its CA, and the
// local stand-in for AWS, with the files and flags a run is given.
type env struct {
	etcdEndpoint string
	etcdCA       *authority
	// the files the flags name
	etcdCAFile, etcdCertFile, etcdKeyFile string
	raCertFile, raKeyFile                 string
	aws                                   *fakeAWS
	client                                *clientv3.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	etcdCA := newAuthority(t, "etcd ca")
	serverCert, serverKey := etcdCA.issue(t, "etcd")
	clientCert, clientKey := etcdCA.issue(t, "gaon-etcd-snapshot")
	gaonCA := newAuthority(t, "gaon ca")
	raCert, raKey := gaonCA.issue(t, "gaon-etcd-snapshot")

	e := &env{
		etcdCA:       etcdCA,
		etcdCAFile:   writeFile(t, "etcd-ca.pem", etcdCA.pem),
		etcdCertFile: writeFile(t, "etcd-client.pem", clientCert),
		etcdKeyFile:  writeFile(t, "etcd-client-key.pem", clientKey),
		raCertFile:   writeFile(t, "aws.pem", raCert),
		raKeyFile:    writeFile(t, "aws-key.pem", raKey),
		aws:          newFakeAWS(t, gaonCA),
	}
	e.etcdEndpoint = startEtcd(t, &transport.TLSInfo{
		CertFile:       writeFile(t, "etcd-server.pem", serverCert),
		KeyFile:        writeFile(t, "etcd-server-key.pem", serverKey),
		TrustedCAFile:  e.etcdCAFile,
		ClientCertAuth: true,
	}, "", "")

	tlsCfg, err := (&transport.TLSInfo{CertFile: e.etcdCertFile, KeyFile: e.etcdKeyFile, TrustedCAFile: e.etcdCAFile}).ClientConfig()
	require.NoError(t, err)
	e.client, err = clientv3.New(clientv3.Config{Endpoints: []string{e.etcdEndpoint}, DialTimeout: 5 * time.Second, TLS: tlsCfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.client.Close() })
	return e
}

// args are the flags of a run against e.
func (e *env) args() []string {
	return []string{
		"-etcd-endpoints", e.etcdEndpoint,
		"-etcd-ca-file", e.etcdCAFile,
		"-etcd-cert-file", e.etcdCertFile,
		"-etcd-key-file", e.etcdKeyFile,
		"-etcd-dial-timeout", "5s",
		"-s3-bucket", testBucket,
		"-s3-prefix", testPrefix,
		"-s3-endpoint", e.aws.URL,
		"-region", "eu-west-1",
		"-rolesanywhere-cert-file", e.raCertFile,
		"-rolesanywhere-key-file", e.raKeyFile,
		"-rolesanywhere-trust-anchor-arn", "arn:aws:rolesanywhere:eu-west-1:111122223333:trust-anchor/00000000-0000-0000-0000-000000000001",
		"-rolesanywhere-profile-arn", "arn:aws:rolesanywhere:eu-west-1:111122223333:profile/00000000-0000-0000-0000-000000000002",
		"-rolesanywhere-role-arn", "arn:aws:iam::111122223333:role/gaon-etcd-snapshot",
		"-rolesanywhere-endpoint", e.aws.URL,
		"-timeout", "1m",
	}
}

// run runs the command as main does, with its log and usage in log, and
// returns its exit code.
func (e *env) run(t *testing.T, args []string) (code int, log string) {
	t.Helper()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	code = execute(ctx, args, &out, fixedClock{})
	return code, out.String()
}

// put writes the keys and returns the revision of the last write.
func (e *env) put(t *testing.T, kv map[string]string) int64 {
	t.Helper()
	var rev int64
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		resp, err := e.client.Put(context.Background(), k, kv[k])
		require.NoError(t, err)
		rev = resp.Header.Revision
	}
	return rev
}

var written = map[string]string{"/registry/a": "alpha", "/registry/b": "beta", "/registry/c": "gamma"}

func TestRun_StoresTheSnapshotUnderItsKeyWithItsChecksum(t *testing.T) {
	e := newEnv(t)
	rev := e.put(t, written)

	code, log := e.run(t, e.args())

	require.Equal(t, 0, code, log)
	wantKey := testBucket + "/" + testPrefix + fixedNowKey + "-rev" + strconv.FormatInt(rev, 10) + ".db"
	body, ok := e.aws.object(wantKey)
	require.True(t, ok, "stored keys: %v", e.aws.keys())
	assert.Len(t, e.aws.keys(), 1)
	assert.Equal(t, wantKey, e.aws.keys()[0])
	// the checksum S3 verified is the SHA-256 of the stored bytes
	sum := sha256.Sum256(body)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sum[:]), e.aws.checksums[wantKey])
	assert.Equal(t, []string{"gaon-etcd-snapshot"}, e.aws.sessionCN)
}

func TestRun_StoresASnapshotThatRestoresToTheWrittenKeys(t *testing.T) {
	e := newEnv(t)
	rev := e.put(t, written)
	code, log := e.run(t, e.args())
	require.Equal(t, 0, code, log)
	body, ok := e.aws.object(testBucket + "/" + testPrefix + fixedNowKey + "-rev" + strconv.FormatInt(rev, 10) + ".db")
	require.True(t, ok)

	// restore it as the procedure in deployment.md does, then serve the
	// restored data directory
	dir := t.TempDir()
	file := writeFile(t, "restore.db", body)
	peer := freeURL(t)
	restored := filepath.Join(dir, "restored")
	require.NoError(t, snapshot.NewV3(zap.NewNop()).Restore(snapshot.RestoreConfig{
		SnapshotPath:   file,
		Name:           "default",
		OutputDataDir:  restored,
		PeerURLs:       []string{peer.String()},
		InitialCluster: "default=" + peer.String(),
	}))
	endpoint := startEtcd(t, nil, restored, peer.String())
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	for k, want := range written {
		resp, err := c.Get(context.Background(), k)
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1, k)
		assert.Equal(t, want, string(resp.Kvs[0].Value), k)
	}
}

func TestRun_ExitsNonZeroAndStoresNothingWhenTheUploadFails(t *testing.T) {
	e := newEnv(t)
	e.put(t, written)
	e.aws.mu.Lock()
	e.aws.denyPut = true
	e.aws.mu.Unlock()

	code, log := e.run(t, e.args())

	assert.Equal(t, 1, code)
	assert.Contains(t, log, "put snapshot")
	assert.Empty(t, e.aws.keys())
}

func TestRun_ExitsNonZeroWhenEtcdDoesNotTrustTheClientCertificate(t *testing.T) {
	e := newEnv(t)
	e.put(t, written)
	stranger := newAuthority(t, "stranger ca")
	cert, key := stranger.issue(t, "gaon-etcd-snapshot")
	args := append(e.args(),
		"-etcd-cert-file", writeFile(t, "stranger.pem", cert),
		"-etcd-key-file", writeFile(t, "stranger-key.pem", key),
		"-etcd-dial-timeout", "2s",
		"-timeout", "5s")

	code, log := e.run(t, args)

	assert.Equal(t, 1, code, log)
	assert.Empty(t, e.aws.keys())
}

func TestRun_RefusesACertificateThatIsNotGaonEtcdSnapshot(t *testing.T) {
	e := newEnv(t)
	e.put(t, written)
	other, otherKey := e.aws.anchor.issue(t, "someone-else")
	args := append(e.args(),
		"-rolesanywhere-cert-file", writeFile(t, "other.pem", other),
		"-rolesanywhere-key-file", writeFile(t, "other-key.pem", otherKey))

	code, log := e.run(t, args)

	assert.Equal(t, 1, code)
	assert.Contains(t, log, "someone-else")
	assert.Empty(t, e.aws.keys())
	assert.Empty(t, e.aws.sessionCN)
}

func TestExecute_RefusesFlagsItCannotRunWith(t *testing.T) {
	e := newEnv(t)
	for name, drop := range map[string]string{
		"no bucket":                  "-s3-bucket",
		"no role":                    "-rolesanywhere-role-arn",
		"no region":                  "-region",
		"a partial set of etcd TLS":  "-etcd-key-file",
		"no Roles Anywhere key file": "-rolesanywhere-key-file",
	} {
		t.Run(name, func(t *testing.T) {
			var args []string
			a := e.args()
			for i := 0; i < len(a); i += 2 {
				if a[i] != drop {
					args = append(args, a[i], a[i+1])
				}
			}
			code, _ := e.run(t, args)
			assert.Equal(t, 2, code)
			assert.Empty(t, e.aws.keys())
		})
	}
}

// startEtcd starts an embedded etcd member on ports it picks. It serves TLS as
// tlsInfo says, or plain HTTP where it is nil. A non-empty dir and peer start
// the member from a restored data directory whose peer URL is peer.
func startEtcd(t *testing.T, tlsInfo *transport.TLSInfo, dir, peer string) string {
	t.Helper()
	const attempts = 10
	for attempt := 1; ; attempt++ {
		cfg := embed.NewConfig()
		cfg.Dir = dir
		if cfg.Dir == "" {
			cfg.Dir = t.TempDir()
		}
		cfg.LogLevel = "panic"
		cfg.TickMs, cfg.ElectionMs = 10, 100
		client := freeURL(t)
		peerURL := freeURL(t)
		if peer != "" {
			u, err := url.Parse(peer)
			require.NoError(t, err)
			peerURL = *u
		}
		if tlsInfo != nil {
			client.Scheme = "https"
			cfg.ClientTLSInfo = *tlsInfo
		}
		cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{client}, []url.URL{client}
		cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peerURL}, []url.URL{peerURL}
		cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
		e, err := embed.StartEtcd(cfg)
		if err != nil && peer == "" && attempt < attempts && errors.Is(err, syscall.EADDRINUSE) {
			continue
		}
		require.NoError(t, err)
		t.Cleanup(e.Close)
		select {
		case <-e.Server.ReadyNotify():
		case <-time.After(30 * time.Second):
			t.Fatal("etcd did not become ready")
		}
		return client.String()
	}
}

func freeURL(t *testing.T) url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return url.URL{Scheme: "http", Host: ln.Addr().String()}
}
