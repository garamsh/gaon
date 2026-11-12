//go:build extensions

package extensions

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/server/v3/embed"
)

// runTag is in the name of everything the test starts outside the checkout:
// the processes, the directory of their files and the sockets in it.
const runTag = "gaon-93"

// service is a gaon service started as its own process. Its output goes to the
// log file, which the test reads.
type service struct {
	name string
	log  string
	cmd  *exec.Cmd
	done chan struct{}
}

// startService runs the service binary bin from binDir with args, in a process
// named runTag-name, and stops it when the test ends.
func startService(t *testing.T, dir, binDir, name, bin string, args ...string) *service {
	t.Helper()
	link := filepath.Join(dir, runTag+"-"+name)
	require.NoError(t, os.Symlink(filepath.Join(binDir, bin), link))
	logFile := filepath.Join(dir, runTag+"-"+name+".log")
	out, err := os.Create(logFile)
	require.NoError(t, err)
	cmd := exec.Command(link, args...) //nolint:gosec // the binary make built
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(t, cmd.Start(), "start %s", name)
	s := &service{name: name, log: logFile, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); _ = out.Close(); close(s.done) }()
	t.Cleanup(s.stop)
	return s
}

// stop interrupts the process and kills it when it has not ended in 15s.
func (s *service) stop() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

func (s *service) output(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(s.log)
	require.NoError(t, err)
	return string(b)
}

func (s *service) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

var servingAddress = regexp.MustCompile(`address=(\S+)`)

// serverAddress waits for the API server to log the address it listens on.
func (s *service) serverAddress(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(s.output(t), "\n") {
			if strings.Contains(line, "serving the resource API") {
				if m := servingAddress.FindStringSubmatch(line); m != nil {
					return m[1]
				}
			}
		}
		require.False(t, s.exited(), "%s ended before it served:\n%s", s.name, s.output(t))
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not log its address:\n%s", s.name, s.output(t))
	return ""
}

// startEtcd runs one etcd member in this process on ephemeral ports and
// returns its client endpoint.
func startEtcd(t *testing.T, dir string) string {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = filepath.Join(dir, runTag+"-etcd")
	cfg.LogLevel = "panic"
	cfg.TickMs, cfg.ElectionMs = 10, 100
	client, peer := ephemeralURL(t), ephemeralURL(t)
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

// ephemeralURL asks the kernel for a free port on loopback.
func ephemeralURL(t *testing.T) url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return url.URL{Scheme: "http", Host: ln.Addr().String()}
}

// within bounds a call by d.
func within(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
