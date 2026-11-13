package networkdefault_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/plugin"
	networkdefault "github.com/garamsh/gaon/network-default"
)

// reaches reports whether a TCP connection from ns to addr is accepted.
func reaches(_ any, dial func(func()), addr string) bool {
	ok := false
	dial(func() {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			ok = true
		}
	})
	return ok
}

func enforce(t *testing.T, limits ...plugin.MicroVMLimits) {
	t.Helper()
	// Every test runs in the same namespace, so the next one starts clean.
	t.Cleanup(func() { _ = (&networkdefault.Default{}).Enforce(context.Background(), nil) })
	require.NoError(t, (&networkdefault.Default{}).Enforce(context.Background(), limits))
}

func TestEnforce_EgressToAnExceptPrefixIsRefusedAndTheRestOfTheCIDRIsAllowed(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24", "10.2.0.130/24"})
	listen(t, top.peer, "10.2.0.5:80")
	listen(t, top.peer, "10.2.0.130:80")
	from := func(f func()) { in(top.vm, f) }

	require.True(t, reaches(top.vm, from, "10.2.0.5:80"), "before any policy")

	enforce(t, plugin.MicroVMLimits{
		MicroVM:   "a",
		Addresses: []string{"10.1.0.10"},
		Egress:    &plugin.Limit{Allow: []plugin.Allow{{Prefix: peerNet, Except: []string{"10.2.0.128/25"}}}},
	})
	require.True(t, reaches(top.vm, from, "10.2.0.5:80"), "inside the cidr and outside the except")
	require.False(t, reaches(top.vm, from, "10.2.0.130:80"), "inside the except prefix")
}

func TestEnforce_AMicroVMNoLimitSelectsIsUnrestrictedWhileAnotherIsLimited(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24", "10.1.0.11/24"}, []string{"10.2.0.5/24"})
	listen(t, top.peer, "10.2.0.5:80")

	enforce(t, plugin.MicroVMLimits{
		MicroVM:   "limited",
		Addresses: []string{"10.1.0.10"},
		Egress:    &plugin.Limit{},
	})
	// Dial from the second address, which no MicroVM's limit names.
	var limited, free bool
	in(top.vm, func() {
		for _, c := range []struct {
			src string
			out *bool
		}{{"10.1.0.10", &limited}, {"10.1.0.11", &free}} {
			d := net.Dialer{Timeout: 500 * time.Millisecond, LocalAddr: &net.TCPAddr{IP: net.ParseIP(c.src)}}
			if conn, err := d.Dial("tcp", "10.2.0.5:80"); err == nil {
				_ = conn.Close()
				*c.out = true
			}
		}
	})
	require.False(t, limited, "an empty egress limit allows nothing")
	require.True(t, free, "an unselected MicroVM is unrestricted")
}

func TestEnforce_APortLimitsWhichConnectionsAreAllowed(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.peer, "10.2.0.5:80")
	listen(t, top.peer, "10.2.0.5:81")
	from := func(f func()) { in(top.vm, f) }

	enforce(t, plugin.MicroVMLimits{
		MicroVM:   "a",
		Addresses: []string{"10.1.0.10"},
		Egress:    &plugin.Limit{Allow: []plugin.Allow{{Prefix: peerNet, Ports: []plugin.PolicyPort{{Protocol: plugin.TCP, Port: 80}}}}},
	})
	require.True(t, reaches(top.vm, from, "10.2.0.5:80"))
	require.False(t, reaches(top.vm, from, "10.2.0.5:81"))
}

func TestEnforce_IngressLimitRefusesAPeerNoAllowNames(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24", "10.2.0.6/24"})
	listen(t, top.vm, "10.1.0.10:80")
	var okSrc, badSrc bool
	enforce(t, plugin.MicroVMLimits{
		MicroVM:   "a",
		Addresses: []string{"10.1.0.10"},
		Ingress:   &plugin.Limit{Allow: []plugin.Allow{{Prefix: "10.2.0.5/32"}}},
	})
	in(top.peer, func() {
		for _, c := range []struct {
			src string
			out *bool
		}{{"10.2.0.5", &okSrc}, {"10.2.0.6", &badSrc}} {
			d := net.Dialer{Timeout: 500 * time.Millisecond, LocalAddr: &net.TCPAddr{IP: net.ParseIP(c.src)}}
			if conn, err := d.Dial("tcp", "10.1.0.10:80"); err == nil {
				_ = conn.Close()
				*c.out = true
			}
		}
	})
	require.True(t, okSrc)
	require.False(t, badSrc)
}

func TestEnforce_ASecondCallReplacesTheFirstAndAnEmptyOneRemovesTheLimit(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.peer, "10.2.0.5:80")
	from := func(f func()) { in(top.vm, f) }
	deny := plugin.MicroVMLimits{MicroVM: "a", Addresses: []string{"10.1.0.10"}, Egress: &plugin.Limit{}}

	enforce(t, deny)
	require.False(t, reaches(top.vm, from, "10.2.0.5:80"))
	enforce(t, deny) // the same again must not fail on the table being there
	require.False(t, reaches(top.vm, from, "10.2.0.5:80"))
	enforce(t)
	require.True(t, reaches(top.vm, from, "10.2.0.5:80"))
}

func TestEnforce_TheRepliesOfAnAllowedConnectionPassAnIngressLimitThatAllowsNothing(t *testing.T) {
	requireNamespaces(t)
	top := newTopology(t, []string{"10.1.0.10/24"}, []string{"10.2.0.5/24"})
	listen(t, top.peer, "10.2.0.5:80")
	from := func(f func()) { in(top.vm, f) }

	enforce(t, plugin.MicroVMLimits{
		MicroVM:   "a",
		Addresses: []string{"10.1.0.10"},
		Ingress:   &plugin.Limit{},
		Egress:    &plugin.Limit{Allow: []plugin.Allow{{Prefix: peerNet}}},
	})
	require.True(t, reaches(top.vm, from, "10.2.0.5:80"))
}

func TestEnforce_RefusesLimitsItCannotRead(t *testing.T) {
	d := &networkdefault.Default{}
	for name, l := range map[string]plugin.MicroVMLimits{
		"address": {MicroVM: "a", Addresses: []string{"nope"}},
		"prefix":  {MicroVM: "a", Addresses: []string{"10.1.0.10"}, Egress: &plugin.Limit{Allow: []plugin.Allow{{Prefix: "x"}}}},
		"except":  {MicroVM: "a", Addresses: []string{"10.1.0.10"}, Egress: &plugin.Limit{Allow: []plugin.Allow{{Prefix: peerNet, Except: []string{"x"}}}}},
		"port":    {MicroVM: "a", Addresses: []string{"10.1.0.10"}, Egress: &plugin.Limit{Allow: []plugin.Allow{{Ports: []plugin.PolicyPort{{Protocol: plugin.TCP, Port: 0}}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, d.Enforce(context.Background(), []plugin.MicroVMLimits{l}))
		})
	}
}
