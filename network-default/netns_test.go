package networkdefault_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// envInNamespaces marks the process the tests re-run themselves in, which has
// a user and a network namespace of its own where it is root.
const envInNamespaces = "GAON_NETWORK_DEFAULT_TEST_NAMESPACES"

// inNamespaces reports whether this process is the one in its own namespaces.
var inNamespaces = os.Getenv(envInNamespaces) == "1"

// ran and skipped count the tests that asked requireNamespaces and were let
// run or sent to skip.
var ran, skipped atomic.Int64

// skipReason holds the reason the first skipped test gave.
var skipReason atomic.Value

// runAndReport runs the tests and says how many of those needing the
// namespaces ran and how many skipped, and the reason the first skip gave.
func runAndReport(m *testing.M) int {
	code := m.Run()
	fmt.Fprintf(os.Stderr, "network-default: of the %d tests needing their own namespaces, %d ran and %d skipped",
		ran.Load()+skipped.Load(), ran.Load(), skipped.Load())
	if r, ok := skipReason.Load().(string); ok {
		fmt.Fprintf(os.Stderr, " (%s)", r)
	}
	fmt.Fprintln(os.Stderr)
	return code + 1
}

// TestMain re-runs the test binary in new user and network namespaces, where
// it may write nftables rules and create links without privilege on the host.
// Where the host refuses the namespaces the tests run as they are, and those
// that need them skip.
func TestMain(m *testing.M) {
	if inNamespaces {
		os.Exit(runAndReport(m))
	}
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(os.Environ(), envInNamespaces+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exit):
		os.Exit(exit.ExitCode())
	}
	os.Exit(runAndReport(m))
}

// forwardingProbe is the write of the router's forwarding switch the tests
// need, made once: a host can allow the namespaces and still refuse what the
// process does in them. Ubuntu 24.04 does, where AppArmor confines a process
// in an unprivileged user namespace to the profile unprivileged_userns, which
// refuses the write although the process is root there with every capability.
var forwardingProbe = sync.OnceValue(func() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o600)
})

// requireNamespaces skips the test where the host refused the namespaces, or
// refused the write of /proc/sys/net/ipv4/ip_forward in them, and says which.
func requireNamespaces(t *testing.T) {
	t.Helper()
	if !inNamespaces {
		skip(t, "the host does not allow an unprivileged process its own user and network namespaces")
	}
	if err := forwardingProbe(); errors.Is(err, os.ErrPermission) {
		skip(t, fmt.Sprintf("the host refused the write of /proc/sys/net/ipv4/ip_forward in the new namespaces: %v; AppArmor profile: %s", err, profile()))
	}
	ran.Add(1)
}

// skip counts the test as skipped and skips it with reason.
func skip(t *testing.T, reason string) {
	t.Helper()
	skipped.Add(1)
	skipReason.CompareAndSwap(nil, reason)
	t.Skip(reason)
}

// profile returns the AppArmor profile the process runs under, or what stands
// in its place.
func profile() string {
	b, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return "unreadable"
	}
	return strings.TrimSpace(string(b))
}

// topology is a router, the namespace the tests run in, between two hosts:
// the MicroVMs' host and a peer. Each is a namespace joined to the router by
// a link.
type topology struct {
	vm, peer netns.NsHandle
}

const (
	vmNet   = "10.1.0.0/24"
	peerNet = "10.2.0.0/24"
)

// newTopology makes the namespaces and the links and gives the MicroVMs' host
// the addresses vmAddrs, and the peer peerAddrs, which are 10.1.0.0/24 and
// 10.2.0.0/24 addresses. It turns on forwarding in the router, which
// requireNamespaces has written, and reads it back.
func newTopology(t *testing.T, vmAddrs, peerAddrs []string) *topology {
	t.Helper()
	require.NoError(t, forwardingProbe())
	on, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(on)))
	top := &topology{vm: newNamespace(t), peer: newNamespace(t)}
	link(t, top.vm, "vm", "10.1.0.1/24", vmAddrs, "10.1.0.1")
	link(t, top.peer, "peer", "10.2.0.1/24", peerAddrs, "10.2.0.1")
	return top
}

// newNamespace returns a new network namespace, closed when the test ends.
func newNamespace(t *testing.T) netns.NsHandle {
	t.Helper()
	var ns netns.NsHandle
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The thread is left in the new namespace and ends with the goroutine.
		runtime.LockOSThread()
		ns, err = netns.New()
	}()
	<-done
	require.NoError(t, err)
	t.Cleanup(func() { _ = ns.Close() })
	return ns
}

// link joins the router to ns by a veth pair named for kind, gives the
// router the address routerAddr, the other end addrs and a default route
// through gateway, and sets both up.
func link(t *testing.T, ns netns.NsHandle, kind, routerAddr string, addrs []string, gateway string) {
	t.Helper()
	require.NoError(t, netlink.LinkAdd(&netlink.Veth{
		LinkAttrs:     netlink.LinkAttrs{Name: kind + "0"},
		PeerName:      kind + "1",
		PeerNamespace: netlink.NsFd(int(ns)),
	}))
	router, err := netlink.LinkByName(kind + "0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = netlink.LinkDel(router) })
	a, err := netlink.ParseAddr(routerAddr)
	require.NoError(t, err)
	require.NoError(t, netlink.AddrAdd(router, a))
	require.NoError(t, netlink.LinkSetUp(router))

	h, err := netlink.NewHandleAt(ns)
	require.NoError(t, err)
	defer h.Close()
	far, err := h.LinkByName(kind + "1")
	require.NoError(t, err)
	for _, s := range addrs {
		a, err := netlink.ParseAddr(s)
		require.NoError(t, err)
		require.NoError(t, h.AddrAdd(far, a))
	}
	require.NoError(t, h.LinkSetUp(far))
	require.NoError(t, h.RouteAdd(&netlink.Route{Gw: net.ParseIP(gateway), LinkIndex: far.Attrs().Index}))
}

// in runs fn on a thread that is in ns, which ends with fn.
func in(ns netns.NsHandle, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		if err := netns.Set(ns); err != nil {
			panic(err)
		}
		fn()
	}()
	<-done
}

// listen accepts TCP connections on addr in ns until the test ends.
func listen(t *testing.T, ns netns.NsHandle, addr string) {
	t.Helper()
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
			_ = c.Close()
		}
	}()
}
