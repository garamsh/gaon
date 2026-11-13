package plugin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/lib/plugin"
	"github.com/garamsh/gaon/lib/plugin/plugintest"
)

func start(t *testing.T, serve func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("the plugin did not stop")
		}
	})
}

func waitSocket(t *testing.T, dir string, kind plugin.Kind, name string) {
	t.Helper()
	path, err := plugin.SocketPath(dir, kind, name)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 5*time.Second, 10*time.Millisecond)
}

func microvm(name string) *api.MicroVM {
	m := &api.MicroVM{}
	m.Metadata.Name = name
	m.Spec.Image = "img"
	return m
}

type network struct {
	policy *plugin.NetworkPolicy
	err    error
	seen   *api.MicroVM
}

func (n *network) Policy(_ context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	n.seen = m
	return n.policy, n.err
}

type storage struct {
	disks    []plugin.DiskAttachment
	err      error
	released []string
}

func (s *storage) Attach(context.Context, *api.MicroVM) ([]plugin.DiskAttachment, error) {
	return s.disks, s.err
}

func (s *storage) Release(_ context.Context, id string) error {
	s.released = append(s.released, id)
	return s.err
}

func TestNetwork_APolicyArrivesWithEveryFieldAndTheMicroVMItWasAskedFor(t *testing.T) {
	dir := plugintest.SocketDir(t)
	want := &plugin.NetworkPolicy{
		Egress: []plugin.NetworkRule{{Prefix: "10.0.0.0/8", Protocol: plugin.Any}},
		Ingress: []plugin.NetworkRule{
			{Prefix: "192.168.0.0/16", Protocol: plugin.TCP, Ports: []plugin.PortRange{{First: 80, Last: 80}, {First: 8000, Last: 8100}}},
			{Prefix: "fd00::/8", Protocol: plugin.ICMPv6},
			{Prefix: "10.1.0.0/16", Protocol: plugin.UDP, Ports: []plugin.PortRange{{First: 53, Last: 53}}},
			{Prefix: "10.2.0.0/16", Protocol: plugin.ICMP},
		},
	}
	n := &network{policy: want}
	start(t, func(ctx context.Context) error { return plugin.ServeNetwork(ctx, dir, "test", n) })
	waitSocket(t, dir, plugin.KindNetwork, "test")
	c, err := plugin.DialNetwork(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	got, err := c.Policy(t.Context(), microvm("a"))

	require.NoError(t, err)
	assert.Equal(t, want, got)
	require.NotNil(t, n.seen)
	assert.Equal(t, "a", n.seen.Metadata.Name)
	assert.Equal(t, "img", n.seen.Spec.Image)
}

// enforcingNetwork is a network that also implements plugin.Enforcer.
type enforcingNetwork struct {
	network
	limits []plugin.MicroVMLimits
	err    error
}

func (n *enforcingNetwork) Enforce(_ context.Context, limits []plugin.MicroVMLimits) error {
	n.limits = limits
	return n.err
}

func TestNetwork_LimitsArriveWithEveryFieldAndAnUnsetDirectionStaysUnset(t *testing.T) {
	dir := plugintest.SocketDir(t)
	want := []plugin.MicroVMLimits{
		{
			MicroVM:   "a",
			Addresses: []string{"10.0.0.2", "fd00::2"},
			Ingress: &plugin.Limit{Allow: []plugin.Allow{
				{Prefix: "10.1.0.0/16", Except: []string{"10.1.2.0/24"}, Ports: []plugin.PolicyPort{{Protocol: plugin.TCP, Port: 80}, {Protocol: plugin.UDP, Port: 53}}},
				{},
			}},
			Egress: &plugin.Limit{},
		},
		{MicroVM: "b", Addresses: []string{"10.0.0.3"}},
	}
	n := &enforcingNetwork{}
	start(t, func(ctx context.Context) error { return plugin.ServeNetwork(ctx, dir, "test", n) })
	waitSocket(t, dir, plugin.KindNetwork, "test")
	c, err := plugin.DialNetwork(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Enforce(t.Context(), want))

	assert.Equal(t, want, n.limits)
	// The control: an empty limit is a limit that allows nothing, and an
	// unset one is no limit, so the two did not collapse on the wire.
	require.NotNil(t, n.limits[0].Egress)
	assert.Nil(t, n.limits[1].Ingress)

	n.err = errors.New("the table could not be written")
	err = c.Enforce(t.Context(), want)
	assert.Equal(t, codes.Unknown, status.Code(err))
	assert.Contains(t, err.Error(), "the table could not be written")
}

func TestNetwork_ANetworkThatDoesNotEnforceAnswersUnimplemented(t *testing.T) {
	dir := plugintest.SocketDir(t)
	start(t, func(ctx context.Context) error { return plugin.ServeNetwork(ctx, dir, "test", &network{}) })
	waitSocket(t, dir, plugin.KindNetwork, "test")
	c, err := plugin.DialNetwork(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	err = c.Enforce(t.Context(), nil)

	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestNetwork_ARefusalArrivesAsARefusalAndAnyOtherErrorDoesNot(t *testing.T) {
	dir := plugintest.SocketDir(t)
	n := &network{err: &plugin.Refusal{Reason: "UnknownNetwork", Message: "no such network"}}
	start(t, func(ctx context.Context) error { return plugin.ServeNetwork(ctx, dir, "test", n) })
	waitSocket(t, dir, plugin.KindNetwork, "test")
	c, err := plugin.DialNetwork(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Policy(t.Context(), microvm("a"))
	var r *plugin.Refusal
	require.ErrorAs(t, err, &r)
	assert.Equal(t, plugin.Refusal{Reason: "UnknownNetwork", Message: "no such network"}, *r)

	// The control: an error that is not a refusal is not one on the other
	// side either, so the agent retries it.
	n.err = errors.New("the database is down")
	_, err = c.Policy(t.Context(), microvm("a"))
	require.Error(t, err)
	assert.False(t, errors.As(err, &r))
	assert.Equal(t, codes.Unknown, status.Code(err))
	assert.Contains(t, err.Error(), "the database is down")
}

func TestNetwork_APluginThatIsNotRunningFailsTheCallWithoutRefusing(t *testing.T) {
	dir := plugintest.SocketDir(t)
	c, err := plugin.DialNetwork(dir, "absent")
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Policy(t.Context(), microvm("a"))

	require.Error(t, err)
	var r *plugin.Refusal
	assert.False(t, errors.As(err, &r))
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestNetwork_APluginStartedAfterTheClientWasMadeAnswersTheNextCall(t *testing.T) {
	dir := plugintest.SocketDir(t)
	c, err := plugin.DialNetwork(dir, "late")
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Policy(t.Context(), microvm("a"))
	require.Error(t, err)

	start(t, func(ctx context.Context) error {
		return plugin.ServeNetwork(ctx, dir, "late", &network{policy: &plugin.NetworkPolicy{}})
	})
	waitSocket(t, dir, plugin.KindNetwork, "late")

	require.Eventually(t, func() bool {
		_, err := c.Policy(t.Context(), microvm("a"))
		return err == nil
	}, 10*time.Second, 50*time.Millisecond)
}

func TestStorage_DisksArriveAndReleaseCarriesTheMachineID(t *testing.T) {
	dir := plugintest.SocketDir(t)
	s := &storage{disks: []plugin.DiskAttachment{{DiskID: "volume-a"}, {DiskID: "volume-b", ReadOnly: true}}}
	start(t, func(ctx context.Context) error { return plugin.ServeStorage(ctx, dir, "test", s) })
	waitSocket(t, dir, plugin.KindStorage, "test")
	c, err := plugin.DialStorage(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	got, err := c.Attach(t.Context(), microvm("a"))
	require.NoError(t, err)
	assert.Equal(t, s.disks, got)

	require.NoError(t, c.Release(t.Context(), "machine-1"))
	assert.Equal(t, []string{"machine-1"}, s.released)
}

func TestStorage_ARefusalArrivesAsARefusalAndAFailedReleaseAsAnError(t *testing.T) {
	dir := plugintest.SocketDir(t)
	s := &storage{err: &plugin.Refusal{Reason: "VolumeNotFound", Message: "volume x"}}
	start(t, func(ctx context.Context) error { return plugin.ServeStorage(ctx, dir, "test", s) })
	waitSocket(t, dir, plugin.KindStorage, "test")
	c, err := plugin.DialStorage(dir, "test")
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Attach(t.Context(), microvm("a"))
	var r *plugin.Refusal
	require.ErrorAs(t, err, &r)
	assert.Equal(t, "VolumeNotFound", r.Reason)

	s.err = errors.New("disk busy")
	err = c.Release(t.Context(), "m")
	require.Error(t, err)
	assert.False(t, errors.As(err, &r))
	_, err = c.Attach(t.Context(), microvm("a"))
	require.Error(t, err)
	assert.False(t, errors.As(err, &r))
}

func TestServe_RefusesASocketAnotherProcessServesAndReplacesAStaleOne(t *testing.T) {
	dir := plugintest.SocketDir(t)
	start(t, func(ctx context.Context) error {
		return plugin.ServeNetwork(ctx, dir, "test", &network{policy: &plugin.NetworkPolicy{}})
	})
	waitSocket(t, dir, plugin.KindNetwork, "test")

	err := plugin.ServeNetwork(t.Context(), dir, "test", &network{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "served by another process")

	// A socket file nothing listens on is what a killed plugin leaves.
	stale := filepath.Join(dir, "network-stale.sock")
	require.NoError(t, os.WriteFile(stale, nil, 0o600))
	start(t, func(ctx context.Context) error {
		return plugin.ServeNetwork(ctx, dir, "stale", &network{policy: &plugin.NetworkPolicy{}})
	})
	waitSocket(t, dir, plugin.KindNetwork, "stale")
	c, err := plugin.DialNetwork(dir, "stale")
	require.NoError(t, err)
	defer c.Close()
	require.Eventually(t, func() bool {
		_, err := c.Policy(t.Context(), microvm("a"))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
}

func TestSocketPath_NamesTheKindAndRefusesANameThatLeavesTheDirectory(t *testing.T) {
	p, err := plugin.SocketPath("/run/gaon/plugins", plugin.KindStorage, "local")
	require.NoError(t, err)
	assert.Equal(t, "/run/gaon/plugins/storage-local.sock", p)

	for _, name := range []string{"", "../x", "a/b", "A", "-a", "a-", "a.b"} {
		_, err := plugin.SocketPath("/d", plugin.KindNetwork, name)
		assert.Error(t, err, "name %q", name)
	}
	_, err = plugin.SocketPath("", plugin.KindNetwork, "a")
	assert.Error(t, err)
}

func TestSocketPath_RefusesAPathOverTheLimitAndNamesIt(t *testing.T) {
	// "/" + "network-a.sock" is 15 bytes, so a directory of 92 bytes makes 107.
	atLimit := "/" + strings.Repeat("d", 91)
	p, err := plugin.SocketPath(atLimit, plugin.KindNetwork, "a")
	require.NoError(t, err)
	require.Len(t, p, plugin.MaxSocketPathLen, "the control must sit on the limit")

	_, err = plugin.SocketPath(atLimit+"d", plugin.KindNetwork, "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the 107")
}

func TestServe_RefusesADirectoryWhoseSocketPathIsOverTheLimitBeforeBinding(t *testing.T) {
	dir := plugintest.SocketDir(t)
	long := filepath.Join(dir, strings.Repeat("d", plugin.MaxSocketPathLen))

	err := plugin.ServeNetwork(t.Context(), long, "test", &network{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the 107")
	_, statErr := os.Stat(long)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing was made for a socket that cannot be bound")

	start(t, func(ctx context.Context) error {
		return plugin.ServeNetwork(ctx, dir, "test", &network{policy: &plugin.NetworkPolicy{}})
	})
	waitSocket(t, dir, plugin.KindNetwork, "test")
}

func TestServe_RestrictsTheSocketToItsUserAndMakesTheDirectoryItCreatesPrivate(t *testing.T) {
	dir := filepath.Join(plugintest.SocketDir(t), "plugins")
	start(t, func(ctx context.Context) error {
		return plugin.ServeNetwork(ctx, dir, "test", &network{policy: &plugin.NetworkPolicy{}})
	})
	c, err := plugin.DialNetwork(dir, "test")
	require.NoError(t, err)
	defer c.Close()
	// A call that is answered was made after the socket was restricted.
	require.Eventually(t, func() bool {
		_, err := c.Policy(t.Context(), microvm("a"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	path, err := plugin.SocketPath(dir, plugin.KindNetwork, "test")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the plugin's user may connect")
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(), "a directory the plugin makes is its user's alone")
}
