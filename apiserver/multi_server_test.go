package apiserver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// etcdCluster is an embedded etcd cluster of several members in this process.
type etcdCluster struct {
	endpoints []string
	members   []*embed.Etcd
	closed    []sync.Once
}

// stop closes member i; the test's end closes the others.
func (c *etcdCluster) stop(i int) { c.closed[i].Do(c.members[i].Close) }

// startEtcdCluster starts n members that form one cluster. Like startEtcd, it
// starts again on new ports where an address was taken meanwhile.
func startEtcdCluster(t *testing.T, n int) *etcdCluster {
	t.Helper()
	const attempts = 10
	for attempt := 1; ; attempt++ {
		clients, peers := make([]url.URL, n), make([]url.URL, n)
		initial := ""
		for i := range n {
			clients[i], peers[i] = freeURL(t), freeURL(t)
			if i > 0 {
				initial += ","
			}
			initial += fmt.Sprintf("m%d=%s", i, peers[i].String())
		}
		c := &etcdCluster{}
		var startErr error
		for i := range n {
			cfg := embed.NewConfig()
			cfg.Name = fmt.Sprintf("m%d", i)
			cfg.Dir = t.TempDir()
			cfg.LogLevel = "panic"
			cfg.TickMs, cfg.ElectionMs = 10, 100
			cfg.GRPCKeepAliveMinTime = time.Second
			cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{clients[i]}, []url.URL{clients[i]}
			cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peers[i]}, []url.URL{peers[i]}
			cfg.InitialCluster, cfg.InitialClusterToken = initial, fmt.Sprintf("g123-%d", attempt)
			e, err := embed.StartEtcd(cfg)
			if err != nil {
				startErr = err
				break
			}
			c.members = append(c.members, e)
			c.closed = append(c.closed, sync.Once{})
			c.endpoints = append(c.endpoints, clients[i].String())
		}
		if startErr != nil {
			for _, e := range c.members {
				e.Close()
			}
			if attempt < attempts && errors.Is(startErr, syscall.EADDRINUSE) {
				continue
			}
			require.NoError(t, startErr)
		}
		for i := range c.members {
			t.Cleanup(func() { c.stop(i) })
		}
		for _, e := range c.members {
			select {
			case <-e.Server.ReadyNotify():
			case <-time.After(30 * time.Second):
				t.Fatal("etcd did not become ready")
			}
		}
		return c
	}
}

// startServer runs an API server over the etcd endpoints, as apiserver.Run does
// in gaon-apiserver, and stops it when the test ends. It returns an error where
// the server does not start.
func startServer(t *testing.T, endpoints []string) (*env, error) {
	t.Helper()
	return startServerWith(t, apiserver.Config{Endpoints: endpoints, DialTimeout: 2 * time.Second})
}

// startServerWith is startServer for the configuration cfg.
func startServerWith(t *testing.T, cfg apiserver.Config) (*env, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- apiserver.Run(runCtx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
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
	e := &env{api: api.NewClient(base+apiRoot, nil), base: base, stop: stop}
	// Run returns at once where it cannot reach etcd; otherwise it serves.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			stopped = true
			return nil, err
		default:
		}
		if _, err := e.api.ListMicroVMs(context.Background()); err == nil {
			return e, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, errors.New("the server did not answer")
}

// startServers runs n API servers over one etcd, as three Nodes of the control
// plane do, and a client of that etcd.
func startServers(t *testing.T, endpoints []string, n int) ([]*env, *clientv3.Client) {
	t.Helper()
	etcd, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = etcd.Close() })
	servers := make([]*env, n)
	for i := range n {
		servers[i], err = startServer(t, endpoints)
		require.NoError(t, err)
		servers[i].etcd = etcd
	}
	return servers, etcd
}

func TestMultiServer_AWriteAtOneServerIsReadAndWatchedAtTheOthers(t *testing.T) {
	servers, _ := startServers(t, []string{startEtcd(t)}, 3)

	list, err := servers[1].api.ListMicroVMs(ctx(t))
	require.NoError(t, err)
	w, err := servers[2].api.WatchMicroVMs(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	created := servers[0].createMicroVM(t, microvm("shared"))
	got, err := servers[1].api.GetMicroVM(ctx(t), "shared")
	require.NoError(t, err)
	assert.Equal(t, created.Metadata.ResourceVersion, got.Metadata.ResourceVersion, "the version is the store's, the same at every server")
	assert.Equal(t, created.Metadata.UID, got.Metadata.UID)

	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "shared", ev.Object.Metadata.Name)

	// The second write is made at a server other than the first, from the version
	// the first returned.
	got.Spec.IdleTimeoutSeconds = 30
	_, err = servers[2].api.PutMicroVM(ctx(t), got)
	require.NoError(t, err)
	ev, err = w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, int32(30), ev.Object.Spec.IdleTimeoutSeconds)
}

func TestMultiServer_OneOfSeveralWritersOfTheSameVersionWins(t *testing.T) {
	servers, _ := startServers(t, []string{startEtcd(t)}, 3)
	created := servers[0].createMicroVM(t, microvm("contended"))

	const writers = 9
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := *created
			m.Spec.IdleTimeoutSeconds = int32(i + 1)
			_, errs[i] = servers[i%len(servers)].api.PutMicroVM(ctx(t), &m)
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, api.ErrConflict, "a writer that lost is told its version is old")
	}
	assert.Equal(t, 1, won, "exactly one write of a version is stored, whichever server it reached")
}

func TestMultiServer_AWebhookConfigurationTakesEffectAtEveryServerAndItsRemovalToo(t *testing.T) {
	servers, _ := startServers(t, []string{startEtcd(t)}, 3)
	w := newWebhookServer(t)
	w.answer = func(c call) *api.AdmissionResponse { return deny(c, 403, "refused by the webhook") }

	c := &api.ValidatingWebhookConfiguration{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionRegistrationGroupVersion, Kind: api.KindValidatingWebhookConfiguration},
		Metadata: api.ObjectMeta{Name: "deny-microvms"},
		Webhooks: []api.Webhook{w.hook("deny.hooks.example", "/", "microvms")},
	}
	stored, err := servers[0].admission().PutValidatingWebhookConfiguration(ctx(t), c)
	require.NoError(t, err)

	for i, s := range servers[1:] {
		_, err = s.api.PutMicroVM(ctx(t), microvm(fmt.Sprintf("denied-%d", i)))
		require.Error(t, err, "server %d enforces a configuration written at server 0", i+1)
		assert.ErrorContains(t, err, "refused by the webhook")
	}

	_, err = servers[2].admission().DeleteValidatingWebhookConfiguration(ctx(t), "deny-microvms", stored.Metadata.ResourceVersion)
	require.NoError(t, err)
	for i, s := range servers {
		_, err = s.api.PutMicroVM(ctx(t), microvm(fmt.Sprintf("allowed-%d", i)))
		assert.NoError(t, err, "control: with the configuration removed at server 2, server %d allows the write", i)
	}
}

func TestMultiServer_ADefinitionCreatedAtOneServerServesAtTheOthersAndItsDeletionToo(t *testing.T) {
	servers, _ := startServers(t, []string{startEtcd(t)}, 3)
	servers[0].define(t, widgetDefinition(t, false))

	_, err := servers[1].custom(t, widgetType).Put(ctx(t), widget("w1", `{"size":3}`))
	require.NoError(t, err, "a path is served from the moment any server stores the definition")
	_, err = servers[2].custom(t, widgetType).Get(ctx(t), "w1")
	require.NoError(t, err)

	d, err := servers[2].extensions().GetCustomResourceDefinition(ctx(t), widgetType.Plural+"."+widgetType.Group)
	require.NoError(t, err)
	_, err = servers[1].extensions().DeleteCustomResourceDefinition(ctx(t), d.Metadata.Name, d.Metadata.ResourceVersion)
	require.NoError(t, err)

	for i, s := range servers {
		_, err = s.custom(t, widgetType).Get(ctx(t), "w1")
		assert.ErrorIs(t, err, api.ErrNotFound, "server %d no longer serves the path of a definition removed at server 1", i)
	}
}

func TestMultiServer_CreatesOfDefinitionsThatShareANameLeaveExactlyOneAccepted(t *testing.T) {
	servers, _ := startServers(t, []string{startEtcd(t)}, 3)

	// The names differ and so do the definitions' keys, but the kind is shared,
	// so at most one definition of the group may hold it.
	const contenders = 12
	errs := make([]error, contenders)
	defs := make([]*api.CustomResourceDefinition, contenders)
	for i := range contenders {
		ct := api.CustomResourceType{Group: appsGroup, Version: "v1alpha1", Kind: "Widget", Plural: fmt.Sprintf("widgets%c", 'a'+i)}
		defs[i] = definition(t, ct, widgetSchema, false)
	}
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = servers[i%len(servers)].extensions().PutCustomResourceDefinition(ctx(t), defs[i])
		}()
	}
	wg.Wait()

	stored := 0
	for _, err := range errs {
		if err == nil {
			stored++
			continue
		}
		assert.ErrorIs(t, err, api.ErrConflict, "a create that kept losing the race is told to retry")
	}
	require.Positive(t, stored, "some create is stored")

	list, err := servers[0].extensions().ListCustomResourceDefinitions(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, stored)
	accepted := 0
	for _, d := range list.Items {
		if condition(t, &d, api.CRDNamesAccepted).Status == api.ConditionTrue {
			accepted++
		}
	}
	assert.Equal(t, 1, accepted, "of the definitions that share a kind exactly one holds the names, whichever servers stored them")
}

func TestMultiServer_ObjectsCreatedWhileTheirDefinitionIsDeletedAreNotLeftBehind(t *testing.T) {
	servers, etcd := startServers(t, []string{startEtcd(t)}, 3)
	name := widgetType.Plural + "." + widgetType.Group
	servers[0].define(t, widgetDefinition(t, false))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i, s := range servers[1:] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				// Whether the create is stored or refused, it must not leave an
				// object behind once the definition is gone.
				_, _ = s.custom(t, widgetType).Put(ctx(t), widget(fmt.Sprintf("w-%d-%d", i, n), `{"size":1}`))
			}
		}()
	}
	// Let creates get under way, then delete the definition at a third server.
	require.Eventually(t, func() bool {
		l, err := servers[0].custom(t, widgetType).List(ctx(t))
		return err == nil && len(l.Items) >= 5
	}, 15*time.Second, 5*time.Millisecond, "the creators stored objects before the delete")
	d, err := servers[0].extensions().GetCustomResourceDefinition(ctx(t), name)
	require.NoError(t, err)
	_, err = servers[0].extensions().DeleteCustomResourceDefinition(ctx(t), name, d.Metadata.ResourceVersion)
	require.NoError(t, err)
	close(stop)
	wg.Wait()

	_, err = servers[1].extensions().GetCustomResourceDefinition(ctx(t), name)
	require.ErrorIs(t, err, api.ErrNotFound, "the definition is removed")
	left, err := etcd.Get(ctx(t), "/registry/"+name+"/", clientv3.WithPrefix())
	require.NoError(t, err)
	assert.Empty(t, left.Kvs, "no object of the removed kind is left in the store")
}

func TestMultiServer_AServerStartsAndServesWhileTheFirstEndpointIsDown(t *testing.T) {
	cluster := startEtcdCluster(t, 3)

	servers, _ := startServers(t, cluster.endpoints, 2)
	created := servers[0].createMicroVM(t, microvm("before"))

	// The first member stops; the other two keep a quorum.
	cluster.stop(0)
	started, err := startServer(t, cluster.endpoints)
	require.NoError(t, err, "the first endpoint is down and a server starts over the others")
	got, err := started.api.GetMicroVM(ctx(t), "before")
	require.NoError(t, err)
	assert.Equal(t, created.Metadata.ResourceVersion, got.Metadata.ResourceVersion)
	_, err = started.api.PutMicroVM(ctx(t), microvm("after"))
	require.NoError(t, err, "the new server writes through the cluster")
	_, err = servers[1].api.GetMicroVM(ctx(t), "after")
	assert.NoError(t, err, "a server started before the member stopped reads what it wrote")

	_, err = startServer(t, cluster.endpoints[:1])
	assert.Error(t, err, "control: a server given only the stopped member's endpoint does not start")
}
