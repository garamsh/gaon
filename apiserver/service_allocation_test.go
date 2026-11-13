package apiserver_test

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

// holders is the allocation keys under space, as address to holder.
func holders(t *testing.T, etcd *clientv3.Client, space string) map[string]string {
	t.Helper()
	prefix := "/allocations/" + space + "/"
	resp, err := etcd.Get(context.Background(), prefix, clientv3.WithPrefix())
	require.NoError(t, err)
	out := map[string]string{}
	for _, kv := range resp.Kvs {
		out[string(kv.Key)[len(prefix):]] = string(kv.Value)
	}
	return out
}

func lbSvc(name string) *api.Service {
	s := newSvc(name)
	s.Spec.Type = api.ServiceLoadBalancer
	return s
}

func TestAllocation_ACreateHoldsTheAddressesItIsGivenInTheStore(t *testing.T) {
	e := ranged(t)
	service := netip.MustParsePrefix("10.96.0.0/24")
	pool := netip.MustParsePrefix("192.168.50.0/28")

	plain, err := e.api.PutService(ctx(t), newSvc("plain"))
	require.NoError(t, err)
	clusterIP := netip.MustParseAddr(plain.Spec.ClusterIP)
	assert.True(t, service.Contains(clusterIP))
	assert.NotEqual(t, "10.96.0.0", clusterIP.String(), "not the network address")
	assert.NotEqual(t, "10.96.0.255", clusterIP.String(), "not the broadcast address")
	assert.Nil(t, plain.Status.LoadBalancer, "control: a ClusterIP Service has no LAN address")
	assert.Equal(t, map[string]string{clusterIP.String(): "plain"}, holders(t, e.etcd, "service-ip"))
	assert.Empty(t, holders(t, e.etcd, "lan"))

	lb, err := e.api.PutService(ctx(t), lbSvc("lb"))
	require.NoError(t, err)
	require.NotNil(t, lb.Status.LoadBalancer)
	lan := netip.MustParseAddr(lb.Status.LoadBalancer.Ingress[0].IP)
	assert.True(t, pool.Contains(lan))
	assert.True(t, service.Contains(netip.MustParseAddr(lb.Spec.ClusterIP)), "a LoadBalancer Service takes a clusterIP as well")
	assert.Equal(t, map[string]string{lan.String(): "lb"}, holders(t, e.etcd, "lan"))

	headless := newSvc("headless")
	headless.Spec.ClusterIP = api.ClusterIPNone
	got, err := e.api.PutService(ctx(t), headless)
	require.NoError(t, err)
	assert.Equal(t, api.ClusterIPNone, got.Spec.ClusterIP)
	assert.Len(t, holders(t, e.etcd, "service-ip"), 2, "a headless Service holds no address")
}

func TestAllocation_AnAddressMayBeAskedFor(t *testing.T) {
	e := ranged(t)
	s := lbSvc("asks")
	s.Spec.ClusterIP, s.Spec.LoadBalancerIP = "10.96.0.77", "192.168.50.5"

	got, err := e.api.PutService(ctx(t), s)
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.77", got.Spec.ClusterIP)
	assert.Equal(t, "192.168.50.5", got.Status.LoadBalancer.Ingress[0].IP)
	assert.Equal(t, map[string]string{"10.96.0.77": "asks"}, holders(t, e.etcd, "service-ip"))
	assert.Equal(t, map[string]string{"192.168.50.5": "asks"}, holders(t, e.etcd, "lan"))
}

func TestAllocation_ARequestedAddressOutsideItsRangeIsInvalid(t *testing.T) {
	e := ranged(t)

	outside := newSvc("outside")
	outside.Spec.ClusterIP = "10.97.0.1"
	_, err := e.api.PutService(ctx(t), outside)
	assert.ErrorIs(t, err, api.ErrInvalid, "a clusterIP outside the service prefix")
	lan := lbSvc("lan")
	lan.Spec.LoadBalancerIP = "192.168.50.99"
	_, err = e.api.PutService(ctx(t), lan)
	assert.ErrorIs(t, err, api.ErrInvalid, "a loadBalancerIP outside the pool")
	swapped := lbSvc("swapped")
	swapped.Spec.LoadBalancerIP = "10.96.0.9"
	_, err = e.api.PutService(ctx(t), swapped)
	assert.ErrorIs(t, err, api.ErrInvalid, "a service address asked for in the pool")
	network := newSvc("network")
	network.Spec.ClusterIP = "10.96.0.0"
	_, err = e.api.PutService(ctx(t), network)
	assert.ErrorIs(t, err, api.ErrInvalid, "the network address of the range")
	assert.Empty(t, holders(t, e.etcd, "service-ip"), "a refused create holds nothing")

	inside := lbSvc("inside")
	inside.Spec.ClusterIP, inside.Spec.LoadBalancerIP = "10.96.0.9", "192.168.50.9"
	_, err = e.api.PutService(ctx(t), inside)
	assert.NoError(t, err, "control: addresses inside their ranges")
}

func TestAllocation_AHeldAddressIsAConflictAndTheHolderKeepsIt(t *testing.T) {
	e := ranged(t)
	first := lbSvc("first")
	first.Spec.ClusterIP, first.Spec.LoadBalancerIP = "10.96.0.10", "192.168.50.10"
	_, err := e.api.PutService(ctx(t), first)
	require.NoError(t, err)

	sameIP := newSvc("same-ip")
	sameIP.Spec.ClusterIP = "10.96.0.10"
	_, err = e.api.PutService(ctx(t), sameIP)
	assert.ErrorIs(t, err, api.ErrConflict, "a clusterIP another Service holds")
	sameLAN := lbSvc("same-lan")
	sameLAN.Spec.LoadBalancerIP = "192.168.50.10"
	_, err = e.api.PutService(ctx(t), sameLAN)
	assert.ErrorIs(t, err, api.ErrConflict, "a loadBalancerIP another Service holds")

	_, err = e.api.GetService(ctx(t), "same-ip")
	assert.ErrorIs(t, err, api.ErrNotFound, "the refused create stored nothing")
	assert.Equal(t, map[string]string{"10.96.0.10": "first"}, holders(t, e.etcd, "service-ip"), "the holder keeps its key")
	assert.Equal(t, map[string]string{"192.168.50.10": "first"}, holders(t, e.etcd, "lan"))

	free := newSvc("free")
	free.Spec.ClusterIP = "10.96.0.11"
	_, err = e.api.PutService(ctx(t), free)
	assert.NoError(t, err, "control: an address nobody holds")
}

func TestAllocation_ACreateOfAStoredNameIsAlreadyExistsEvenWhereItsAddressIsHeld(t *testing.T) {
	e := ranged(t)
	s := newSvc("web")
	s.Spec.ClusterIP = "10.96.0.10"
	_, err := e.api.PutService(ctx(t), s)
	require.NoError(t, err)

	again := newSvc("web")
	again.Spec.ClusterIP = "10.96.0.10"
	_, err = e.api.PutService(ctx(t), again)
	assert.ErrorIs(t, err, api.ErrAlreadyExists)
}

func TestAllocation_AFullRangeIsAConflictAndADeleteFreesAnAddressForTheNextCreate(t *testing.T) {
	e := newEnvWith(t, envOptions{serviceRange: "10.96.0.0/30", lanPool: "192.168.50.0/30"})
	// A /30 holds the network and the broadcast address and two to give.
	first, err := e.api.PutService(ctx(t), newSvc("one"))
	require.NoError(t, err)
	_, err = e.api.PutService(ctx(t), newSvc("two"))
	require.NoError(t, err, "control: the range has room for two")
	assert.ElementsMatch(t, []string{"10.96.0.1", "10.96.0.2"}, keysOf(holders(t, e.etcd, "service-ip")))

	_, err = e.api.PutService(ctx(t), newSvc("three"))
	assert.ErrorIs(t, err, api.ErrConflict, "a full service prefix")
	_, err = e.api.GetService(ctx(t), "three")
	assert.ErrorIs(t, err, api.ErrNotFound)

	_, err = e.api.DeleteService(ctx(t), "one", first.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{netipOther(first.Spec.ClusterIP): "two"}, holders(t, e.etcd, "service-ip"), "the delete released the key of the Service and no other")
	three, err := e.api.PutService(ctx(t), newSvc("three"))
	require.NoError(t, err, "the freed address is there for the next create")
	assert.Equal(t, first.Spec.ClusterIP, three.Spec.ClusterIP)
}

func TestAllocation_AFullLANPoolIsAConflictAndADeleteFreesItsAddress(t *testing.T) {
	e := newEnvWith(t, envOptions{serviceRange: "10.96.0.0/24", lanPool: "192.168.50.0/30"})
	a, err := e.api.PutService(ctx(t), lbSvc("a"))
	require.NoError(t, err)
	_, err = e.api.PutService(ctx(t), lbSvc("b"))
	require.NoError(t, err, "control: the pool has room for two")

	_, err = e.api.PutService(ctx(t), lbSvc("c"))
	assert.ErrorIs(t, err, api.ErrConflict, "a full pool")
	assert.Len(t, holders(t, e.etcd, "service-ip"), 2, "the refused create held no service address either")
	_, err = e.api.PutService(ctx(t), newSvc("plain"))
	assert.NoError(t, err, "a full pool does not stop a ClusterIP Service")

	_, err = e.api.DeleteService(ctx(t), "a", a.Metadata.ResourceVersion)
	require.NoError(t, err)
	c, err := e.api.PutService(ctx(t), lbSvc("c"))
	require.NoError(t, err, "the freed LAN address is there for the next create")
	assert.Equal(t, a.Status.LoadBalancer.Ingress[0].IP, c.Status.LoadBalancer.Ingress[0].IP)
}

func TestAllocation_ADeleteReleasesBothKeysAndOnlyThoseOfItsService(t *testing.T) {
	e := ranged(t)
	lb, err := e.api.PutService(ctx(t), lbSvc("lb"))
	require.NoError(t, err)
	other, err := e.api.PutService(ctx(t), lbSvc("other"))
	require.NoError(t, err)
	require.Len(t, holders(t, e.etcd, "lan"), 2)

	_, err = e.api.DeleteService(ctx(t), "lb", lb.Metadata.ResourceVersion)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{other.Spec.ClusterIP: "other"}, holders(t, e.etcd, "service-ip"))
	assert.Equal(t, map[string]string{other.Status.LoadBalancer.Ingress[0].IP: "other"}, holders(t, e.etcd, "lan"))

	headless := newSvc("headless")
	headless.Spec.ClusterIP = api.ClusterIPNone
	h, err := e.api.PutService(ctx(t), headless)
	require.NoError(t, err)
	_, err = e.api.DeleteService(ctx(t), "headless", h.Metadata.ResourceVersion)
	assert.NoError(t, err, "a headless Service holds nothing to release")
	assert.Len(t, holders(t, e.etcd, "service-ip"), 1)
}

func TestAllocation_AnUpdateWithAnEmptyClusterIPKeepsItAndAnAddressIsNotHeldTwice(t *testing.T) {
	e := ranged(t)
	lb, err := e.api.PutService(ctx(t), lbSvc("lb"))
	require.NoError(t, err)

	update := *lb
	update.Spec.ClusterIP, update.Spec.LoadBalancerIP = "", ""
	update.Spec.Ports = []api.ServicePort{{Protocol: api.ProtocolTCP, Port: 8080}}
	kept, err := e.api.PutService(ctx(t), &update)
	require.NoError(t, err)
	assert.Equal(t, lb.Spec.ClusterIP, kept.Spec.ClusterIP, "the empty clusterIP kept the stored one")
	assert.Equal(t, lb.Status, kept.Status)
	assert.Equal(t, map[string]string{lb.Spec.ClusterIP: "lb"}, holders(t, e.etcd, "service-ip"), "an update allocates nothing")
	assert.Len(t, holders(t, e.etcd, "lan"), 1)

	other := *kept
	other.Spec.ClusterIP = "10.96.0.201"
	_, err = e.api.PutService(ctx(t), &other)
	assert.ErrorIs(t, err, api.ErrInvalid, "control: a different clusterIP is refused")
}

func TestAllocation_AServerWithNoRangeRefusesAServiceThatNeedsAnAddress(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutService(ctx(t), newSvc("plain"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, api.ErrInvalid, "the fault is the server's, not the request's")
	headless := newSvc("headless")
	headless.Spec.ClusterIP = api.ClusterIPNone
	_, err = e.api.PutService(ctx(t), headless)
	assert.NoError(t, err, "control: a headless Service needs no range")
}

func TestAllocation_TwoServersOnOneStoreNeverHandOutOneAddress(t *testing.T) {
	endpoint := startEtcd(t)
	cfg := apiserver.Config{Endpoints: []string{endpoint}, ServiceClusterIPRange: "10.96.0.0/26", LoadBalancerPool: "192.168.50.0/28", DialTimeout: 2 * time.Second}
	etcd, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = etcd.Close() })
	servers := make([]*env, 3)
	for i := range servers {
		var err error
		servers[i], err = startServerWith(t, cfg)
		require.NoError(t, err)
	}

	const contenders = 45
	type result struct {
		svc *api.Service
		err error
	}
	results := make([]result, contenders)
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := lbSvc(fmt.Sprintf("svc-%d", i))
			results[i].svc, results[i].err = servers[i%len(servers)].api.PutService(ctx(t), s)
		}()
	}
	wg.Wait()

	clusterIPs, lans := map[string]string{}, map[string]string{}
	stored := 0
	for i, r := range results {
		if r.err != nil {
			assert.ErrorIs(t, r.err, api.ErrConflict, "a create that lost every race is told to retry, never given a held address")
			continue
		}
		stored++
		name := fmt.Sprintf("svc-%d", i)
		if prior, dup := clusterIPs[r.svc.Spec.ClusterIP]; dup {
			t.Errorf("clusterIP %s is held by %s and %s", r.svc.Spec.ClusterIP, prior, name)
		}
		clusterIPs[r.svc.Spec.ClusterIP] = name
		lan := r.svc.Status.LoadBalancer.Ingress[0].IP
		if prior, dup := lans[lan]; dup {
			t.Errorf("LAN address %s is held by %s and %s", lan, prior, name)
		}
		lans[lan] = name
	}
	require.Positive(t, stored)
	assert.LessOrEqual(t, stored, 14, "the pool holds 14 addresses to give")
	assert.Equal(t, clusterIPs, holders(t, etcd, "service-ip"), "each stored Service holds its key, and the keys hold nothing else")
	assert.Equal(t, lans, holders(t, etcd, "lan"))

	list, err := servers[2].api.ListServices(ctx(t))
	require.NoError(t, err)
	assert.Len(t, list.Items, stored)
}

func TestAllocation_TwoServersAskedForOneAddressGiveItToOne(t *testing.T) {
	endpoint := startEtcd(t)
	cfg := apiserver.Config{Endpoints: []string{endpoint}, ServiceClusterIPRange: "10.96.0.0/24", DialTimeout: 2 * time.Second}
	a, err := startServerWith(t, cfg)
	require.NoError(t, err)
	b, err := startServerWith(t, cfg)
	require.NoError(t, err)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, e := range []*env{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newSvc(fmt.Sprintf("claim-%d", i))
			s.Spec.ClusterIP = "10.96.0.50"
			_, errs[i] = e.api.PutService(ctx(t), s)
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, api.ErrConflict)
	}
	assert.Equal(t, 1, won, "exactly one of two Services that ask for one address holds it")
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// netipOther is the address of the two a /30 holds that is not ip.
func netipOther(ip string) string {
	if ip == "10.96.0.1" {
		return "10.96.0.2"
	}
	return "10.96.0.1"
}
