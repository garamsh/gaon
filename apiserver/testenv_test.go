package apiserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// env is a gaon-apiserver run by apiserver.Run over a real, embedded etcd
// member, and the clients that reach them.
type env struct {
	api  *api.Client
	root string
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

	root := "http://" + ln.Addr().String()
	return &env{api: api.NewClient(root, nil), root: root, etcd: etcd, stop: stop}
}

func startEtcd(t *testing.T) string {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "panic"
	cfg.TickMs, cfg.ElectionMs = 10, 100
	client, peer := freeURL(t), freeURL(t)
	cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{client}, []url.URL{client}
	cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peer}, []url.URL{peer}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
	e, err := embed.StartEtcd(cfg)
	require.NoError(t, err)
	t.Cleanup(e.Close)
	select {
	case <-e.Server.ReadyNotify():
	case <-time.After(30 * time.Second):
		t.Fatal("etcd did not become ready")
	}
	return client.String()
}

func freeURL(t *testing.T) url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return url.URL{Scheme: "http", Host: ln.Addr().String()}
}

// send makes a raw request, for what the client does not send. It reads the
// whole response under a deadline, so a stream the server wrongly opens fails
// the test here instead of hanging it.
func (e *env) send(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.root+path, strings.NewReader(body))
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

func machine(name string) *api.Machine {
	return &api.Machine{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMachine},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.MachineSpec{Image: "alpine", RunStrategy: api.RunAlways},
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

// createMachine creates a Machine and fails the test if that does not work.
func (e *env) createMachine(t *testing.T, m *api.Machine) *api.Machine {
	t.Helper()
	created, err := e.api.PutMachine(ctx(t), m)
	require.NoError(t, err)
	return created
}

// bind binds the Machine at its current version and returns the result.
func (e *env) bind(t *testing.T, m *api.Machine, nodeName string, epoch api.Epoch) *api.Machine {
	t.Helper()
	bound, err := e.api.BindMachine(ctx(t), m.Metadata.Name, api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: nodeName, BindingEpoch: epoch})
	require.NoError(t, err)
	return bound
}

func (e *env) createNode(t *testing.T, name string) *api.Node {
	t.Helper()
	created, err := e.api.PutNode(ctx(t), node(name))
	require.NoError(t, err)
	return created
}
