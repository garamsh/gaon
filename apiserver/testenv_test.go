package apiserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// apiRoot is the group and version root the server is documented to serve.
const apiRoot = "/apis/compute.gaon.garam.sh/v1alpha1"

// env is a gaon-apiserver run by apiserver.Run over a real, embedded etcd
// member, and the clients that reach them.
type env struct {
	api  *api.Client
	base string
	etcd *clientv3.Client
	stop func() error
}

// newEnv starts one etcd member in this process and the API server over it.
// The server stops when the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	endpoint := startEtcd(t)
	etcd, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = etcd.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cfg := apiserver.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second}
	go func() { done <- apiserver.Run(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		return <-done
	}
	t.Cleanup(func() { _ = stop() })

	base := "http://" + ln.Addr().String()
	return &env{api: api.NewClient(base+apiRoot, nil), base: base, etcd: etcd, stop: stop}
}

// startEtcd starts an embedded etcd on ports it picks. A port is free when
// freeURL looks and may be taken by another process before etcd binds it, so
// a start that fails on a taken address is made again on new ports.
func startEtcd(t *testing.T) string {
	t.Helper()
	const attempts = 10
	for attempt := 1; ; attempt++ {
		cfg := embed.NewConfig()
		cfg.Dir = t.TempDir()
		cfg.LogLevel = "panic"
		cfg.TickMs, cfg.ElectionMs = 10, 100
		client, peer := freeURL(t), freeURL(t)
		cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{client}, []url.URL{client}
		cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peer}, []url.URL{peer}
		cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
		e, err := embed.StartEtcd(cfg)
		if err != nil && attempt < attempts && errors.Is(err, syscall.EADDRINUSE) {
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

// send makes a raw request to path under the API root, for what the client
// does not send.
func (e *env) send(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	return e.sendAbsolute(t, method, apiRoot+path, body)
}

// sendAbsolute makes a raw request to path on the server's address. It reads
// the whole response under a deadline, so a stream the server wrongly opens
// fails the test here instead of hanging it.
func (e *env) sendAbsolute(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.base+path, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func microvm(name string) *api.MicroVM {
	return &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.MicroVMSpec{Image: "alpine", RunStrategy: api.RunAlways},
	}
}

func node(name string) *api.Node {
	return &api.Node{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindNode},
		Metadata: api.ObjectMeta{Name: name},
	}
}

func lease(name string) *api.Lease {
	return &api.Lease{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindLease},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.LeaseSpec{Holder: name, RenewTime: time.Now().UTC(), DurationSeconds: 15},
	}
}

// createMicroVM creates a MicroVM and fails the test if that does not work.
func (e *env) createMicroVM(t *testing.T, m *api.MicroVM) *api.MicroVM {
	t.Helper()
	created, err := e.api.PutMicroVM(ctx(t), m)
	require.NoError(t, err)
	return created
}

// bind binds the MicroVM at its current version and returns the result.
func (e *env) bind(t *testing.T, m *api.MicroVM, nodeName string, epoch api.Epoch) *api.MicroVM {
	t.Helper()
	bound, err := e.api.BindMicroVM(ctx(t), m.Metadata.Name, api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: nodeName, BindingEpoch: epoch})
	require.NoError(t, err)
	return bound
}

func (e *env) createNode(t *testing.T, name string) *api.Node {
	t.Helper()
	created, err := e.api.PutNode(ctx(t), node(name))
	require.NoError(t, err)
	return created
}
