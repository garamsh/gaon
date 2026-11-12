package api_test

import (
	"context"
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestCoordinationRoot_KeepsTheServerAndReplacesTheGroup(t *testing.T) {
	root, err := api.CoordinationRoot("https://10.0.0.5:8443/apis/compute.gaon.garam.sh/v1alpha1")
	require.NoError(t, err)
	assert.Equal(t, "https://10.0.0.5:8443/apis/"+api.CoordinationGroupVersion, root)

	_, err = api.CoordinationRoot("http://127.0.0.1:8080")
	assert.ErrorContains(t, err, "/apis/", "a root with no group path names no server to derive from")
}

func TestLeaderElectionFlags_DescribeTheElectorTheProcessRuns(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := api.RegisterLeaderElectionFlags(fs, "gaon-scheduler")
	require.NoError(t, fs.Parse([]string{"-leader-election-identity", "node-1"}))

	e, err := f.Elector("http://127.0.0.1:1/apis/compute.gaon.garam.sh/v1alpha1", nil)
	require.NoError(t, err)
	require.NotNil(t, e, "election is on by default")
	assert.Equal(t, "gaon-scheduler", e.Name)
	assert.Equal(t, "node-1", e.Identity)
	assert.Equal(t, 15*time.Second, e.Duration)
	assert.Less(t, e.RenewDeadline, e.Duration)

	f.Duration = e.RenewDeadline
	_, err = f.Elector("http://127.0.0.1:1/apis/x/v1", nil)
	assert.Error(t, err, "a duration that is not above the renew deadline is refused at start")

	f.Enabled = false
	e, err = f.Elector("no apis path", nil)
	require.NoError(t, err, "control: with election off nothing is derived or validated")
	assert.Nil(t, e)
}

func TestDefaultIdentity_DiffersBetweenCalls(t *testing.T) {
	a, err := api.DefaultIdentity()
	require.NoError(t, err)
	b, err := api.DefaultIdentity()
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two processes on one host must not share an identity")
}

func TestRunLeading_WithoutAnElectorRunsAtOnceAndReturnsItsError(t *testing.T) {
	boom := errors.New("boom")
	assert.ErrorIs(t, api.RunLeading(context.Background(), nil, func(context.Context) error { return boom }), boom)
}

func TestRunLeading_AFailingTermEndsItWithTheErrorAndReleasesTheLease(t *testing.T) {
	store := &leaseStore{}
	clock := newElectionClock()
	e := &api.LeaderElector{
		Leases: &memberClient{store: store}, Name: "scheduler", Identity: "a", Clock: clock,
		Duration: electionDuration, RenewDeadline: electionDeadline, RetryPeriod: electionRetry,
	}
	boom := errors.New("boom")
	err := api.RunLeading(context.Background(), e, func(context.Context) error { return boom })
	assert.ErrorIs(t, err, boom)
	l, ok := store.get()
	require.True(t, ok)
	assert.Empty(t, l.Spec.Holder, "the Lease is released, so another instance takes over at once")
}
