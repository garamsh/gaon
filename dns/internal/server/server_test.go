package server_test

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/dns/internal/server"
	"github.com/garamsh/gaon/lib/api"
)

const (
	zoneName   = "cluster.local"
	fakeToken  = "fabricated-token-1"
	retryAfter = 20 * time.Millisecond
)

var networks = []string{"udp", "tcp"}

// unit is a running server and the files it reads.
type unit struct {
	udp, tcp  string
	tokenFile string
}

// addr returns where the unit answers over network.
func (u *unit) addr(network string) string {
	if network == "tcp" {
		return u.tcp
	}
	return u.udp
}

// start runs a server against f that forwards to upstreams, and stops it when
// the test ends.
func start(t *testing.T, f *fakeAPI, upstreams ...netip.AddrPort) *unit {
	t.Helper()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, ca, 0o600))
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(fakeToken+"\n"), 0o600))

	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, server.Config{
			Zone:          zoneName,
			Upstreams:     upstreams,
			APIServer:     f.URL,
			CAFile:        caFile,
			TokenFile:     tokenFile,
			RetryInterval: retryAfter,
		}, udp, tcp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	return &unit{udp: udp.LocalAddr().String(), tcp: tcp.Addr().String(), tokenFile: tokenFile}
}

// startSynced runs a server and waits until it has listed both kinds.
func startSynced(t *testing.T, f *fakeAPI, upstreams ...netip.AddrPort) *unit {
	t.Helper()
	u := start(t, f, upstreams...)
	require.Eventually(t, func() bool {
		return ask(t, "udp", u.udp, "probe.default.svc."+zoneName+".", dns.TypeA).Rcode != dns.RcodeServerFailure
	}, 5*time.Second, 10*time.Millisecond, "the server never finished its first list")
	return u
}

func ask(t *testing.T, network, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := &dns.Client{Net: network, Timeout: 3 * time.Second}
	reply, _, err := c.Exchange(new(dns.Msg).SetQuestion(name, qtype), addr)
	require.NoError(t, err, "%s question for %s", network, name)
	return reply
}

// ips returns the addresses in the answer section, sorted by text.
func ips(reply *dns.Msg) []string {
	var out []string
	for _, rr := range reply.Answer {
		switch rr := rr.(type) {
		case *dns.A:
			out = append(out, rr.A.String())
		case *dns.AAAA:
			out = append(out, rr.AAAA.String())
		}
	}
	return out
}

func svcName(service string) string { return service + ".default.svc." + zoneName + "." }

func TestClusterIPAnswersAOverUDPAndTCP(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10"), service("db", "10.96.0.20")}, nil)
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			reply := ask(t, network, u.addr(network), svcName("web"), dns.TypeA)
			assert.Equal(t, dns.RcodeSuccess, reply.Rcode)
			assert.True(t, reply.Authoritative)
			assert.Equal(t, []string{"10.96.0.10"}, ips(reply))
			require.Len(t, reply.Answer, 1)
			assert.Equal(t, uint32(5), reply.Answer[0].Header().Ttl)

			// Control: another Service has its own address, so the answer is
			// the named Service's.
			assert.Equal(t, []string{"10.96.0.20"}, ips(ask(t, network, u.addr(network), svcName("db"), dns.TypeA)))
		})
	}
}

func TestNameComparisonIgnoresCase(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	reply := ask(t, "udp", u.udp, "WEB.Default.SVC.Cluster.LOCAL.", dns.TypeA)
	require.Equal(t, []string{"10.96.0.10"}, ips(reply))
	assert.Equal(t, "WEB.Default.SVC.Cluster.LOCAL.", reply.Answer[0].Header().Name)
}

func TestOtherFamilyOfAClusterIPHasNoData(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("v4", "10.96.0.10"), service("v6", "fd00:96::10")}, nil)
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			noData := ask(t, network, u.addr(network), svcName("v4"), dns.TypeAAAA)
			assert.Equal(t, dns.RcodeSuccess, noData.Rcode)
			assert.Empty(t, noData.Answer)
			require.Len(t, noData.Ns, 1, "a negative answer carries the start of authority")
			assert.Equal(t, uint32(5), noData.Ns[0].Header().Ttl)

			// Controls: the same name answers its own family, and the IPv6
			// Service answers AAAA and has no data for A.
			assert.Equal(t, []string{"10.96.0.10"}, ips(ask(t, network, u.addr(network), svcName("v4"), dns.TypeA)))
			assert.Equal(t, []string{"fd00:96::10"}, ips(ask(t, network, u.addr(network), svcName("v6"), dns.TypeAAAA)))
			assert.Empty(t, ask(t, network, u.addr(network), svcName("v6"), dns.TypeA).Answer)
		})
	}
}

func TestLoadBalancerAnswersItsClusterIP(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	lb := service("lb", "10.96.0.30")
	lb.Spec.Type = api.ServiceLoadBalancer
	lb.Status.LoadBalancer = &api.LoadBalancerStatus{Ingress: []api.LoadBalancerIngress{{IP: "192.168.1.50"}}}
	f.setLists([]api.Service{lb}, nil)
	u := startSynced(t, f)

	assert.Equal(t, []string{"10.96.0.30"}, ips(ask(t, "udp", u.udp, svcName("lb"), dns.TypeA)))
}

func TestHeadlessServiceAnswersItsReadyAddresses(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists(
		[]api.Service{service("ring", api.ClusterIPNone), service("empty", api.ClusterIPNone)},
		[]api.Endpoints{
			endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1", "ring-1": "10.0.0.2"}),
			endpointsOf("empty", nil),
		})
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			reply := ask(t, network, u.addr(network), svcName("ring"), dns.TypeA)
			assert.Equal(t, dns.RcodeSuccess, reply.Rcode)
			assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips(reply))

			// A headless Service with no ready address is NXDOMAIN, and
			// the control above shows the same query succeeding with some.
			none := ask(t, network, u.addr(network), svcName("empty"), dns.TypeA)
			assert.Equal(t, dns.RcodeNameError, none.Rcode)
			assert.Empty(t, none.Answer)
		})
	}
}

func TestHeadlessServiceWithoutEndpointsObjectIsNXDOMAIN(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("ring", api.ClusterIPNone)}, nil)
	u := startSynced(t, f)

	assert.Equal(t, dns.RcodeNameError, ask(t, "udp", u.udp, svcName("ring"), dns.TypeA).Rcode)
}

func TestPerMicroVMName(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists(
		[]api.Service{service("ring", api.ClusterIPNone), service("web", "10.96.0.10")},
		[]api.Endpoints{endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1", "ring-1": "10.0.0.2"})})
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			addr := u.addr(network)
			assert.Equal(t, []string{"10.0.0.2"}, ips(ask(t, network, addr, "ring-1."+svcName("ring"), dns.TypeA)))
			// Controls: another MicroVM answers its own address, and a
			// MicroVM the Endpoints does not name, or a name below a
			// Service with an address of its own, is NXDOMAIN.
			assert.Equal(t, []string{"10.0.0.1"}, ips(ask(t, network, addr, "ring-0."+svcName("ring"), dns.TypeA)))
			assert.Equal(t, dns.RcodeNameError, ask(t, network, addr, "ring-9."+svcName("ring"), dns.TypeA).Rcode)
			assert.Equal(t, dns.RcodeNameError, ask(t, network, addr, "ring-0."+svcName("web"), dns.TypeA).Rcode)
		})
	}
}

func TestNameThatIsNotAServiceIsNXDOMAIN(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			addr := u.addr(network)
			// Control: the Service itself resolves.
			require.Equal(t, dns.RcodeSuccess, ask(t, network, addr, svcName("web"), dns.TypeA).Rcode)
			for _, name := range []string{
				svcName("missing"),
				"web.other.svc." + zoneName + ".",
				"web.svc." + zoneName + ".",
				"web.default." + zoneName + ".",
				zoneName + ".",
			} {
				reply := ask(t, network, addr, name, dns.TypeA)
				assert.Equal(t, dns.RcodeNameError, reply.Rcode, name)
				assert.Empty(t, reply.Answer, name)
				assert.Len(t, reply.Ns, 1, name)
			}
		})
	}
}

func TestTypesNotServedHaveNoDataWhereTheNameExists(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	for _, qtype := range []uint16{dns.TypeSRV, dns.TypeTXT, dns.TypePTR} {
		name := dns.TypeToString[qtype]
		exists := ask(t, "udp", u.udp, svcName("web"), qtype)
		assert.Equal(t, dns.RcodeSuccess, exists.Rcode, name)
		assert.Empty(t, exists.Answer, name)

		// Control: the same type for a name that does not exist is NXDOMAIN.
		assert.Equal(t, dns.RcodeNameError, ask(t, "udp", u.udp, svcName("missing"), qtype).Rcode, name)
	}
}

func TestServfailBeforeTheFirstListCompletes(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	release := f.holdLists()
	u := start(t, f)

	for _, network := range networks {
		reply := ask(t, network, u.addr(network), svcName("web"), dns.TypeA)
		assert.Equal(t, dns.RcodeServerFailure, reply.Rcode, network)
		assert.Empty(t, reply.Answer, network)
	}

	// Control: once the lists complete, the same name answers.
	release()
	require.Eventually(t, func() bool {
		return len(ips(ask(t, "udp", u.udp, svcName("web"), dns.TypeA))) == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestAnswerFollowsAServiceBeingChangedAndDeleted(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10"), service("db", "10.96.0.20")}, nil)
	u := startSynced(t, f)
	require.Equal(t, dns.RcodeSuccess, ask(t, "udp", u.udp, svcName("web"), dns.TypeA).Rcode)

	f.pushService(t, api.EventDeleted, service("web", "10.96.0.10"))
	require.Eventually(t, func() bool {
		return ask(t, "udp", u.udp, svcName("web"), dns.TypeA).Rcode == dns.RcodeNameError
	}, 5*time.Second, 10*time.Millisecond, "the deleted Service still answers")
	// Control: a Service that was not deleted still answers.
	assert.Equal(t, []string{"10.96.0.20"}, ips(ask(t, "udp", u.udp, svcName("db"), dns.TypeA)))

	f.pushService(t, api.EventAdded, service("fresh", "10.96.0.40"))
	require.Eventually(t, func() bool {
		return len(ips(ask(t, "tcp", u.tcp, svcName("fresh"), dns.TypeA))) == 1
	}, 5*time.Second, 10*time.Millisecond, "an added Service does not answer")
}

func TestAnswerFollowsEndpointsChanging(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists(
		[]api.Service{service("ring", api.ClusterIPNone)},
		[]api.Endpoints{endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1"})})
	u := startSynced(t, f)
	require.Equal(t, []string{"10.0.0.1"}, ips(ask(t, "udp", u.udp, svcName("ring"), dns.TypeA)))

	f.pushEndpoints(t, api.EventModified, endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1", "ring-1": "10.0.0.2"}))
	require.Eventually(t, func() bool {
		return len(ips(ask(t, "udp", u.udp, svcName("ring"), dns.TypeA))) == 2
	}, 5*time.Second, 10*time.Millisecond)

	f.pushEndpoints(t, api.EventDeleted, endpointsOf("ring", nil))
	require.Eventually(t, func() bool {
		return ask(t, "udp", u.udp, svcName("ring"), dns.TypeA).Rcode == dns.RcodeNameError
	}, 5*time.Second, 10*time.Millisecond)
}

func TestLastStateIsServedWhileTheAPIServerIsUnreachable(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	f.setDown(true)
	// The unit has seen the server refuse it, so the answers below are the
	// last state and not a live read.
	require.Eventually(t, func() bool { return f.refused.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
	for _, network := range networks {
		assert.Equal(t, []string{"10.96.0.10"}, ips(ask(t, network, u.addr(network), svcName("web"), dns.TypeA)), network)
		assert.Equal(t, dns.RcodeNameError, ask(t, network, u.addr(network), svcName("missing"), dns.TypeA).Rcode, network)
	}

	// Control: when the server returns with a different list, the unit
	// lists again and answers it.
	f.setLists([]api.Service{service("web", "10.96.0.99")}, nil)
	f.setDown(false)
	require.Eventually(t, func() bool {
		return len(ips(ask(t, "udp", u.udp, svcName("web"), dns.TypeA))) == 1 &&
			ips(ask(t, "udp", u.udp, svcName("web"), dns.TypeA))[0] == "10.96.0.99"
	}, 5*time.Second, 10*time.Millisecond, "the unit did not list again")
}

func TestTokenFileIsReadAgainWhenTheUnitLists(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	// The server now wants a new token, and the file still holds the old one.
	f.setLists([]api.Service{service("web", "10.96.0.10"), service("later", "10.96.0.50")}, nil)
	f.setToken("fabricated-token-2")
	require.Eventually(t, func() bool { return f.unauthorized.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, dns.RcodeNameError, ask(t, "udp", u.udp, svcName("later"), dns.TypeA).Rcode,
		"the old token reads nothing")

	require.NoError(t, os.WriteFile(u.tokenFile, []byte("fabricated-token-2\n"), 0o600))
	require.Eventually(t, func() bool {
		return len(ips(ask(t, "udp", u.udp, svcName("later"), dns.TypeA))) == 1
	}, 5*time.Second, 10*time.Millisecond, "the replaced token file was not read")
}

func TestUDPAnswerTooLargeIsTruncatedAndTCPCarriesAll(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	microVMs := map[string]string{}
	for i := range 100 {
		microVMs[fmt.Sprintf("ring-%d", i)] = fmt.Sprintf("10.0.%d.%d", i/200, i+1)
	}
	f.setLists([]api.Service{service("ring", api.ClusterIPNone), service("small", api.ClusterIPNone)},
		[]api.Endpoints{endpointsOf("ring", microVMs), endpointsOf("small", map[string]string{"small-0": "10.9.0.1"})})
	u := startSynced(t, f)

	udp := ask(t, "udp", u.udp, svcName("ring"), dns.TypeA)
	assert.True(t, udp.Truncated)
	assert.Less(t, len(udp.Answer), 100)
	tcp := ask(t, "tcp", u.tcp, svcName("ring"), dns.TypeA)
	assert.False(t, tcp.Truncated)
	assert.Len(t, tcp.Answer, 100)

	// Control: an answer that fits is not truncated over UDP.
	assert.False(t, ask(t, "udp", u.udp, svcName("small"), dns.TypeA).Truncated)
}

// upstream is a resolver on 127.0.0.1 that answers every A question with
// 203.0.113.9 and records what it was asked.
type upstream struct {
	addr netip.AddrPort

	mu   sync.Mutex
	seen []string
}

func (up *upstream) asked() []string {
	up.mu.Lock()
	defer up.mu.Unlock()
	return append([]string(nil), up.seen...)
}

func (up *upstream) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	up.mu.Lock()
	up.seen = append(up.seen, w.LocalAddr().Network()+" "+req.Question[0].Name)
	up.mu.Unlock()
	reply := new(dns.Msg).SetReply(req)
	if req.Question[0].Qtype == dns.TypeA {
		reply.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("203.0.113.9"),
		}}
	}
	_ = w.WriteMsg(reply)
}

// startUpstream serves UDP and TCP on one ephemeral port of 127.0.0.1.
func startUpstream(t *testing.T) *upstream {
	t.Helper()
	up := &upstream{}
	var tcp net.Listener
	var udp net.PacketConn
	for range 20 {
		var err error
		tcp, err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		udp, err = net.ListenPacket("udp", tcp.Addr().String())
		if err == nil {
			break
		}
		tcp.Close()
		tcp, udp = nil, nil
	}
	require.NotNil(t, udp, "no port was free on both UDP and TCP")
	up.addr = netip.MustParseAddrPort(tcp.Addr().String())

	started := make(chan struct{}, 2)
	notify := func() { started <- struct{}{} }
	servers := []*dns.Server{
		{PacketConn: udp, Handler: up, NotifyStartedFunc: notify},
		{Listener: tcp, Handler: up, NotifyStartedFunc: notify},
	}
	for _, s := range servers {
		go func() { _ = s.ActivateAndServe() }()
	}
	<-started
	<-started
	t.Cleanup(func() {
		for _, s := range servers {
			_ = s.Shutdown()
		}
	})
	return up
}

func TestNameOutsideTheZoneIsForwardedUnchanged(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	up := startUpstream(t)
	u := startSynced(t, f, up.addr)

	reply := ask(t, "udp", u.udp, "Example.ORG.", dns.TypeA)
	assert.Equal(t, []string{"203.0.113.9"}, ips(reply))
	assert.Equal(t, uint32(60), reply.Answer[0].Header().Ttl, "the upstream's answer is returned as it is")
	assert.Equal(t, []string{"udp Example.ORG."}, up.asked())

	assert.Equal(t, []string{"203.0.113.9"}, ips(ask(t, "tcp", u.tcp, "example.net.", dns.TypeA)))
	assert.Equal(t, []string{"udp Example.ORG.", "tcp example.net."}, up.asked())

	// A reverse query is a name outside the zone.
	ask(t, "udp", u.udp, "10.0.96.10.in-addr.arpa.", dns.TypePTR)
	assert.Equal(t, "udp 10.0.96.10.in-addr.arpa.", up.asked()[2])

	// Control: a name under the zone is answered from the Services and
	// never reaches the upstream, whether or not it exists.
	assert.Equal(t, []string{"10.96.0.10"}, ips(ask(t, "udp", u.udp, svcName("web"), dns.TypeA)))
	assert.Equal(t, dns.RcodeNameError, ask(t, "udp", u.udp, svcName("missing"), dns.TypeA).Rcode)
	assert.Len(t, up.asked(), 3)
}

func TestForwardingTriesTheNextUpstreamAndFailsWhenNoneAnswers(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	dead := netip.MustParseAddrPort("127.0.0.1:1")
	up := startUpstream(t)

	both := startSynced(t, f, dead, up.addr)
	assert.Equal(t, []string{"203.0.113.9"}, ips(ask(t, "udp", both.udp, "example.org.", dns.TypeA)))

	onlyDead := startSynced(t, f, dead)
	for _, network := range networks {
		reply := ask(t, network, onlyDead.addr(network), "example.org.", dns.TypeA)
		assert.Equal(t, dns.RcodeServerFailure, reply.Rcode, network)
	}
	// Control: the zone is still answered from the Services.
	assert.Equal(t, []string{"10.96.0.10"}, ips(ask(t, "udp", onlyDead.udp, svcName("web"), dns.TypeA)))
}

func TestNameOutsideTheZoneIsRefusedWithoutAnUpstream(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)

	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			refused := ask(t, network, u.addr(network), "example.org.", dns.TypeA)
			assert.Equal(t, dns.RcodeRefused, refused.Rcode)
			assert.Empty(t, refused.Answer)
			// Control: a name under the zone is not refused.
			assert.Equal(t, dns.RcodeSuccess, ask(t, network, u.addr(network), svcName("web"), dns.TypeA).Rcode)
		})
	}
}

func TestQuestionThatIsNotAQueryOrHasNoSingleQuestion(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists([]api.Service{service("web", "10.96.0.10")}, nil)
	u := startSynced(t, f)
	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}

	notify := new(dns.Msg).SetQuestion(svcName("web"), dns.TypeA)
	notify.Opcode = dns.OpcodeNotify
	reply, _, err := c.Exchange(notify, u.udp)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeNotImplemented, reply.Rcode)

	two := new(dns.Msg).SetQuestion(svcName("web"), dns.TypeA)
	two.Question = append(two.Question, dns.Question{Name: svcName("web"), Qtype: dns.TypeAAAA, Qclass: dns.ClassINET})
	reply, _, err = c.Exchange(two, u.udp)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeFormatError, reply.Rcode)

	// Control: one question of the same name is answered.
	reply, _, err = c.Exchange(new(dns.Msg).SetQuestion(svcName("web"), dns.TypeA), u.udp)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, reply.Rcode)
}

func TestMicroVMNameThatIsNotADNSLabelIsNXDOMAIN(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	f.setLists(
		[]api.Service{service("ring", api.ClusterIPNone)},
		[]api.Endpoints{endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1", "x_y": "10.0.0.2"})})
	u := startSynced(t, f)

	assert.Equal(t, dns.RcodeNameError, ask(t, "udp", u.udp, "x_y."+svcName("ring"), dns.TypeA).Rcode)
	// Control: a MicroVM whose name is a label answers under the same Service.
	assert.Equal(t, []string{"10.0.0.1"}, ips(ask(t, "udp", u.udp, "ring-0."+svcName("ring"), dns.TypeA)))
}

func TestAddressListedTwiceIsAnsweredOnce(t *testing.T) {
	f := newFakeAPI(t, fakeToken)
	both := endpointsOf("ring", map[string]string{"ring-0": "10.0.0.1"})
	other := endpointsOf("ring", map[string]string{"ring-1": "10.0.0.1", "ring-2": "10.0.0.2"})
	both.Subsets = append(both.Subsets, other.Subsets...)
	f.setLists([]api.Service{service("ring", api.ClusterIPNone)}, []api.Endpoints{both})
	u := startSynced(t, f)

	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips(ask(t, "udp", u.udp, svcName("ring"), dns.TypeA)))
}
