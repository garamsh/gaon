package networkdefault_test

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/garamsh/gaon/lib/plugin"
	networkdefault "github.com/garamsh/gaon/network-default"
)

const clusterIP = "10.96.0.10"

// expose makes d expose routes and announce. Every test runs in the same
// namespace, so the cleanup withdraws what it exposed for the next one.
func expose(t *testing.T, d *networkdefault.Default, routes []plugin.Route, announce ...string) {
	t.Helper()
	t.Cleanup(func() { _ = d.Expose(context.Background(), nil, nil) })
	require.NoError(t, d.Expose(context.Background(), routes, announce))
}

func tcpRoute(address string, backends ...string) plugin.Route {
	r := plugin.Route{Service: "default/web", Address: address, Protocol: plugin.TCP, Port: 80}
	for _, b := range backends {
		r.Backends = append(r.Backends, plugin.Backend{Address: b, Port: 8080})
	}
	return r
}

// listenCounting accepts TCP connections on addr in ns until the test ends
// and counts them.
func listenCounting(t *testing.T, ns netns.NsHandle, addr string) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	var ln net.Listener
	var err error
	in(ns, func() { ln, err = net.Listen("tcp", addr) })
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return &n
}

// dial opens and closes a TCP connection from ns to addr.
func dial(ns netns.NsHandle, addr string) error {
	var err error
	in(ns, func() {
		var c net.Conn
		if c, err = net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
		}
	})
	return err
}

func connect(t *testing.T, ns netns.NsHandle, addr string, times int) {
	t.Helper()
	for range times {
		require.NoError(t, dial(ns, addr))
	}
}

func TestExpose_AServiceWithTwoEndpointsSendsConnectionsToBoth(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24", "10.1.0.11/24"}, []string{"10.2.0.5/24"})
	a := listenCounting(t, top.vm, "10.1.0.10:8080")
	b := listenCounting(t, top.vm, "10.1.0.11:8080")
	d := &networkdefault.Default{}

	require.Error(t, dial(top.peer, clusterIP+":80"), "before the Service, nothing answers on the cluster IP")
	expose(t, d, []plugin.Route{tcpRoute(clusterIP, "10.1.0.10", "10.1.0.11")})
	connect(t, top.peer, clusterIP+":80", 60)

	assert.Eventually(t, func() bool { return a.Load()+b.Load() == 60 }, 5*time.Second, 10*time.Millisecond, "every connection reached an endpoint")
	assert.Positive(t, a.Load(), "the first endpoint")
	assert.Positive(t, b.Load(), "the second endpoint")
}

func TestExpose_AnEndpointThatGoesIsNotReachedAndTheOtherStillIs(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24", "10.1.0.11/24"}, []string{"10.2.0.5/24"})
	a := listenCounting(t, top.vm, "10.1.0.10:8080")
	b := listenCounting(t, top.vm, "10.1.0.11:8080")
	d := &networkdefault.Default{}
	expose(t, d, []plugin.Route{tcpRoute(clusterIP, "10.1.0.10", "10.1.0.11")})
	connect(t, top.peer, clusterIP+":80", 40)
	require.Eventually(t, func() bool { return a.Load()+b.Load() == 40 }, 5*time.Second, 10*time.Millisecond)
	require.Positive(t, b.Load(), "control: the second endpoint served while it was listed")
	before, aBefore := b.Load(), a.Load()

	require.NoError(t, d.Expose(context.Background(), []plugin.Route{tcpRoute(clusterIP, "10.1.0.10")}, nil))
	connect(t, top.peer, clusterIP+":80", 40)

	assert.Eventually(t, func() bool { return a.Load()-aBefore == 40 }, 5*time.Second, 10*time.Millisecond, "the remaining endpoint took every connection")
	assert.Equal(t, before, b.Load(), "the endpoint that went")
}

func TestExpose_AServiceWithoutEndpointsRefusesTCPConnections(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listenCounting(t, top.vm, "10.1.0.10:8080")
	d := &networkdefault.Default{}

	expose(t, d, []plugin.Route{tcpRoute(clusterIP)})
	err := dial(top.peer, clusterIP+":80")
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ECONNREFUSED, "a refusal, not a timeout")

	require.NoError(t, d.Expose(context.Background(), []plugin.Route{tcpRoute(clusterIP, "10.1.0.10")}, nil))
	assert.NoError(t, dial(top.peer, clusterIP+":80"), "control: the same Service with an endpoint connects")
}

// udpExchange sends a datagram from ns to addr and reports the error of
// reading the reply.
func udpExchange(ns netns.NsHandle, addr string) error {
	var err error
	in(ns, func() {
		var c net.Conn
		if c, err = net.Dial("udp", addr); err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err = c.Write([]byte("x")); err != nil {
			return
		}
		_, err = c.Read(make([]byte, 8))
	})
	return err
}

func TestExpose_AUDPServiceWithoutEndpointsAnswersPortUnreachable(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	var pc net.PacketConn
	var err error
	in(top.vm, func() { pc, err = net.ListenPacket("udp", "10.1.0.10:8080") })
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 8)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	d := &networkdefault.Default{}
	route := func(backends ...string) plugin.Route {
		r := tcpRoute(clusterIP, backends...)
		r.Protocol = plugin.UDP
		return r
	}

	expose(t, d, []plugin.Route{route()})
	err = udpExchange(top.peer, clusterIP+":80")
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ECONNREFUSED)

	require.NoError(t, d.Expose(context.Background(), []plugin.Route{route("10.1.0.10")}, nil))
	assert.NoError(t, udpExchange(top.peer, clusterIP+":80"), "control: with an endpoint the datagram is answered")
}

func TestExpose_ATranslationOfOneAddressDoesNotTouchAnother(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.vm, "10.1.0.10:8080")
	d := &networkdefault.Default{}

	expose(t, d, []plugin.Route{tcpRoute(clusterIP, "10.1.0.10")})

	require.NoError(t, dial(top.peer, clusterIP+":80"))
	assert.Error(t, dial(top.peer, "10.96.0.11:80"), "another cluster IP has no route")
	assert.Error(t, dial(top.peer, clusterIP+":81"), "another port of the cluster IP has no route")
}

func TestExpose_TheExposeTableDoesNotReplaceTheTableOfEnforce(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.vm, "10.1.0.10:8080")
	d := &networkdefault.Default{}
	enforce(t, plugin.MicroVMLimits{MicroVM: "a", Addresses: []string{"10.1.0.10"}, Ingress: &plugin.Limit{}})
	expose(t, d, []plugin.Route{tcpRoute(clusterIP, "10.1.0.10")})

	assert.Error(t, dial(top.peer, clusterIP+":80"), "the limit still drops what a Service translates to the limited MicroVM")

	require.NoError(t, d.Enforce(context.Background(), nil))
	assert.NoError(t, dial(top.peer, clusterIP+":80"), "control: without the limit the same translation connects")
}

const (
	lanAddr = "10.2.0.200"
	lanPool = "10.2.0.128/25"
)

// neighbour returns the link-layer address that ns holds for ip, or nil.
func neighbour(t *testing.T, ns netns.NsHandle, ip string) net.HardwareAddr {
	t.Helper()
	h, err := netlink.NewHandleAt(ns)
	require.NoError(t, err)
	defer h.Close()
	ns2, err := h.NeighList(0, netlink.FAMILY_ALL)
	require.NoError(t, err)
	for _, n := range ns2 {
		if n.IP.Equal(net.ParseIP(ip)) && n.State&(netlink.NUD_REACHABLE|netlink.NUD_STALE|netlink.NUD_DELAY|netlink.NUD_PROBE|netlink.NUD_PERMANENT) != 0 {
			return n.HardwareAddr
		}
	}
	return nil
}

func flushNeighbours(t *testing.T, ns netns.NsHandle) {
	t.Helper()
	h, err := netlink.NewHandleAt(ns)
	require.NoError(t, err)
	defer h.Close()
	list, err := h.NeighList(0, netlink.FAMILY_ALL)
	require.NoError(t, err)
	for _, n := range list {
		if n.State&netlink.NUD_PERMANENT == 0 {
			_ = h.NeighDel(&n)
		}
	}
}

func routerMAC(t *testing.T, name string) net.HardwareAddr {
	t.Helper()
	l, err := netlink.LinkByName(name)
	require.NoError(t, err)
	return l.Attrs().HardwareAddr
}

func lanDefault(t *testing.T) *networkdefault.Default {
	t.Helper()
	d, err := (&networkdefault.Default{}).WithLAN("peer0", []string{lanPool})
	require.NoError(t, err)
	return d
}

func TestExpose_ALANAddressIsAnsweredForWhileAnnouncedAndNotAfter(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	b := listenCounting(t, top.vm, "10.1.0.10:8080")
	d := lanDefault(t)
	routes := []plugin.Route{func() plugin.Route { r := tcpRoute(lanAddr, "10.1.0.10"); r.LAN = true; return r }()}

	require.Error(t, dial(top.peer, lanAddr+":80"), "before the announcement nothing answers ARP for the address")
	expose(t, d, routes, lanAddr)
	// The failed attempt left an incomplete entry that is probed at its own
	// pace; start from none.
	flushNeighbours(t, top.peer)
	require.NoError(t, dial(top.peer, lanAddr+":80"), "while announced")
	assert.Eventually(t, func() bool { return b.Load() == 1 }, 5*time.Second, 10*time.Millisecond, "the connection reached the endpoint")
	assert.Equal(t, routerMAC(t, "peer0").String(), neighbour(t, top.peer, lanAddr).String(), "the peer learned the address from an ARP answer of this node")

	require.NoError(t, d.Expose(context.Background(), routes, nil))
	flushNeighbours(t, top.peer)
	assert.Error(t, dial(top.peer, lanAddr+":80"), "after the announcement stopped, with the translation unchanged")
	assert.Nil(t, neighbour(t, top.peer, lanAddr), "no ARP answer")
}

func TestExpose_ALANAddressOutsideThePoolIsAnErrorAndTheRestStillConverges(t *testing.T) {
	requireNamespaces(t)
	newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	d := lanDefault(t)
	const stale, fresh, outside = "10.2.0.201", "10.2.0.202", "10.2.0.10"
	held := func() []string {
		l, err := netlink.LinkByName("peer0")
		require.NoError(t, err)
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		require.NoError(t, err)
		var got []string
		for _, a := range addrs {
			got = append(got, a.IPNet.String())
		}
		return got
	}
	expose(t, d, nil, stale)
	require.Contains(t, held(), stale+"/32", "control: an address in the pool is announced")

	err := d.Expose(context.Background(), nil, []string{fresh, outside})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside")
	got := held()
	assert.NotContains(t, got, outside+"/32", "the address outside the pool is not announced")
	assert.Contains(t, got, fresh+"/32", "an address in the pool of the same call is announced")
	assert.NotContains(t, got, stale+"/32", "a stale address is withdrawn in the same call")
}

func TestExpose_AnAnnouncedAddressWithoutAnInterfaceIsAnError(t *testing.T) {
	requireNamespaces(t)
	err := (&networkdefault.Default{}).Expose(context.Background(), nil, []string{lanAddr})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--lan-interface")

	assert.NoError(t, (&networkdefault.Default{}).Expose(context.Background(), nil, nil), "control: announcing nothing needs no interface")
}

func TestExpose_AnAddressOfTheLANInterfaceOutsideThePoolIsLeftAlone(t *testing.T) {
	requireNamespaces(t)
	newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	d := lanDefault(t)
	l, err := netlink.LinkByName("peer0")
	require.NoError(t, err)
	extra, err := netlink.ParseAddr("10.2.0.9/32")
	require.NoError(t, err)
	require.NoError(t, netlink.AddrAdd(l, extra))
	expose(t, d, nil, lanAddr)

	require.NoError(t, d.Expose(context.Background(), nil, nil))

	addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
	require.NoError(t, err)
	var got []string
	for _, a := range addrs {
		got = append(got, a.IPNet.String())
	}
	assert.Contains(t, got, "10.2.0.9/32", "an address the operator holds outside the pool")
	assert.NotContains(t, got, lanAddr+"/32", "the announced address that was withdrawn")
	assert.Contains(t, got, "10.2.0.1/24", "the interface's own address")
}

func TestExpose_AnIPv6ServiceAndItsLANAddressAreServedAndAnnounced(t *testing.T) {
	requireNamespaces(t)
	// An interface made later takes the default, not all.
	require.NoError(t, writeFile("/proc/sys/net/ipv6/conf/all/forwarding", "1"))
	require.NoError(t, writeFile("/proc/sys/net/ipv6/conf/default/forwarding", "1"))
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	v6(t, top.vm, "vm0", "vm1", "fd00:1::1/64", "fd00:1::10/64", "fd00:1::1")
	v6(t, top.peer, "peer0", "peer1", "fd00:2::1/64", "fd00:2::5/64", "fd00:2::1")
	b := listenCounting(t, top.vm, "[fd00:1::10]:8080")
	d, err := (&networkdefault.Default{}).WithLAN("peer0", []string{"fd00:2::/112"})
	require.NoError(t, err)
	const lan6 = "fd00:2::c8"
	route := func(addr string, lan bool) plugin.Route {
		r := tcpRoute(addr, "fd00:1::10")
		r.LAN = lan
		return r
	}

	expose(t, d, []plugin.Route{route("fd00:96::a0", false), route(lan6, true)}, lan6)

	require.NoError(t, dial(top.peer, "[fd00:96::a0]:80"), "a cluster IP of IPv6")
	flushNeighbours(t, top.peer)
	require.NoError(t, dial(top.peer, "["+lan6+"]:80"), "a LAN address of IPv6, answered by NDP")
	assert.Eventually(t, func() bool { return b.Load() == 2 }, 5*time.Second, 10*time.Millisecond, "both connections reached the endpoint")
	assert.Equal(t, routerMAC(t, "peer0").String(), neighbour(t, top.peer, lan6).String())

	require.NoError(t, d.Expose(context.Background(), []plugin.Route{route(lan6, true)}, nil))
	flushNeighbours(t, top.peer)
	assert.Error(t, dial(top.peer, "["+lan6+"]:80"), "after the announcement stopped")
}

func TestExpose_ARouteWhoseBackendsAreOfAnotherFamilyRefusesAndABadRouteIsAnError(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.vm, "10.1.0.10:8080")
	d := &networkdefault.Default{}
	expose(t, d, []plugin.Route{tcpRoute(clusterIP, "fd00:1::10")})

	assert.ErrorIs(t, dial(top.peer, clusterIP+":80"), syscall.ECONNREFUSED, "the only backend cannot be translated to")

	for name, r := range map[string]plugin.Route{
		"address":  tcpRoute("nonsense"),
		"port":     {Service: "s", Address: clusterIP, Protocol: plugin.TCP, Port: 0},
		"protocol": {Service: "s", Address: clusterIP, Protocol: plugin.Any, Port: 80},
		"backend":  tcpRoute(clusterIP, "nonsense"),
	} {
		assert.Error(t, d.Expose(context.Background(), []plugin.Route{r}, nil), name)
	}
	assert.NoError(t, d.Expose(context.Background(), []plugin.Route{tcpRoute(clusterIP, "10.1.0.10")}, nil), "control: a good route")
	assert.NoError(t, dial(top.peer, clusterIP+":80"), "and the table of the bad calls was not written")
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// v6 gives the router the address routerAddr on the link named router and
// ns the address addr on far, with a default route through gateway.
func v6(t *testing.T, ns netns.NsHandle, router, far, routerAddr, addr, gateway string) {
	t.Helper()
	noDAD := func(h *netlink.Handle, link netlink.Link, cidr string) {
		a, err := netlink.ParseAddr(cidr)
		require.NoError(t, err)
		a.Flags = unix.IFA_F_NODAD
		require.NoError(t, h.AddrAdd(link, a))
	}
	rl, err := netlink.LinkByName(router)
	require.NoError(t, err)
	rh, err := netlink.NewHandle()
	require.NoError(t, err)
	defer rh.Close()
	noDAD(rh, rl, routerAddr)
	h, err := netlink.NewHandleAt(ns)
	require.NoError(t, err)
	defer h.Close()
	fl, err := h.LinkByName(far)
	require.NoError(t, err)
	noDAD(h, fl, addr)
	require.NoError(t, h.RouteAdd(&netlink.Route{Gw: net.ParseIP(gateway), LinkIndex: fl.Attrs().Index}))
	// A link-local address that is still tentative cannot send the neighbour
	// solicitations the first connection needs.
	require.Eventually(t, func() bool { return settled(rh, rl) && settled(h, fl) }, 10*time.Second, 20*time.Millisecond)
}

// settled reports whether no address of link is tentative.
func settled(h *netlink.Handle, link netlink.Link) bool {
	addrs, err := h.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.Flags&unix.IFA_F_TENTATIVE != 0 {
			return false
		}
	}
	return true
}
