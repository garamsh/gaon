package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// LeaderElectionFlags holds what the --leader-election flags fill.
type LeaderElectionFlags struct {
	Enabled       bool
	Name          string
	Identity      string
	Duration      time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

// RegisterLeaderElectionFlags registers the flags of a binary that runs on
// several Nodes and acts on one of them: --leader-elect, --leader-election-name
// (defaulting to defaultName), --leader-election-identity,
// --leader-election-duration, --leader-election-renew-deadline and
// --leader-election-retry-period.
func RegisterLeaderElectionFlags(fs *flag.FlagSet, defaultName string) *LeaderElectionFlags {
	f := &LeaderElectionFlags{}
	fs.BoolVar(&f.Enabled, "leader-elect", true, "act only while holding the coordination Lease; turn off only for a single instance")
	fs.StringVar(&f.Name, "leader-election-name", defaultName, "name of the coordination Lease the instances contend for")
	fs.StringVar(&f.Identity, "leader-election-identity", "", "name of this instance in the Lease; default is the host name and a random suffix")
	fs.DurationVar(&f.Duration, "leader-election-duration", 15*time.Second, "how long a Lease no one renews stays held, in whole seconds")
	fs.DurationVar(&f.RenewDeadline, "leader-election-renew-deadline", 10*time.Second, "how long the holder keeps acting without a renewal the server acknowledged; below the duration")
	fs.DurationVar(&f.RetryPeriod, "leader-election-retry-period", 2*time.Second, "time between attempts to take or to renew the Lease; below the renew deadline")
	return f
}

// CoordinationRoot returns the group and version root of the coordination
// group on the server that root, the root of another group and version, names.
func CoordinationRoot(root string) (string, error) {
	i := strings.LastIndex(root, "/apis/")
	if i < 0 {
		return "", fmt.Errorf("leader election: %q has no /apis/ path to take the server address from", root)
	}
	return root[:i] + "/apis/" + CoordinationGroupVersion, nil
}

// Elector returns the LeaderElector the flags describe, whose Lease is on the
// server that root names. It is nil when --leader-elect is off.
func (f *LeaderElectionFlags) Elector(root string, httpClient *http.Client) (*LeaderElector, error) {
	if !f.Enabled {
		return nil, nil
	}
	coordination, err := CoordinationRoot(root)
	if err != nil {
		return nil, err
	}
	identity := f.Identity
	if identity == "" {
		if identity, err = DefaultIdentity(); err != nil {
			return nil, err
		}
	}
	e := &LeaderElector{
		Leases: NewClient(coordination, httpClient), Name: f.Name, Identity: identity, Clock: SystemClock{},
		Duration: f.Duration, RenewDeadline: f.RenewDeadline, RetryPeriod: f.RetryPeriod,
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// DefaultIdentity returns the host name and a random suffix, so that two
// processes on one host, and a process and its restart, differ.
func DefaultIdentity() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("leader election identity: %w", err)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("leader election identity: no random source")
	}
	return host + "_" + hex.EncodeToString(b[:]), nil
}

// RunLeading runs lead as the elector decides, or at once when elector is nil.
// lead is called with a context that ends when the process may no longer act,
// and again, after it returns, when the process takes the Lease again. A lead
// that returns an error ends RunLeading with it, and releases the Lease.
func RunLeading(ctx context.Context, elector *LeaderElector, lead func(ctx context.Context) error) error {
	if elector == nil {
		return lead(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failure error
	err := elector.Run(ctx, func(termCtx context.Context) {
		if err := lead(termCtx); err != nil && !errors.Is(err, context.Canceled) {
			failure = err
			cancel()
		}
	})
	if failure != nil {
		return failure
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
