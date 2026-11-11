package apiserver_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestPutNode_CreateStartsDiscovered(t *testing.T) {
	e := newEnv(t)
	n := node("n1")
	n.Status.State = api.NodeStateAdmitted

	created, err := e.api.PutNode(ctx(t), n)
	require.NoError(t, err)

	assert.Equal(t, api.NodeStateDiscovered, created.Status.State)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	_, err = e.api.PutNode(ctx(t), node("n1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)
}

func TestPutNode_UpdateKeepsStatusAndIsCompareAndSwap(t *testing.T) {
	e := newEnv(t)
	n := e.createNode(t, "n1")

	admit := *n
	admit.Spec.Admitted = true
	admit.Status.State = api.NodeStateAdmitted
	updated, err := e.api.PutNode(ctx(t), &admit)
	require.NoError(t, err)
	assert.True(t, updated.Spec.Admitted)
	assert.Equal(t, api.NodeStateDiscovered, updated.Status.State, "a spec write does not move the state")

	_, err = e.api.PutNode(ctx(t), n)
	assert.ErrorIs(t, err, api.ErrConflict)
}

func TestPutNodeStatus_StateAgreesWithAdmitted(t *testing.T) {
	e := newEnv(t)
	n := e.createNode(t, "n1")

	wrong := *n
	wrong.Status.State = api.NodeStateAdmitted
	_, err := e.api.PutNodeStatus(ctx(t), &wrong)
	assert.ErrorIs(t, err, api.ErrInvalid, "Admitted while spec.admitted is false")

	right := *n
	right.Status.State = api.NodeStateDiscovered
	settled := mustNodeStatus(t, e, &right)

	admit := *settled
	admit.Spec.Admitted = true
	admitted, err := e.api.PutNode(ctx(t), &admit)
	require.NoError(t, err)

	back := *admitted
	back.Status.State = api.NodeStateDiscovered
	_, err = e.api.PutNodeStatus(ctx(t), &back)
	assert.ErrorIs(t, err, api.ErrInvalid, "Discovered while spec.admitted is true")

	ok := *admitted
	ok.Status.State = api.NodeStateAdmitted
	got, err := e.api.PutNodeStatus(ctx(t), &ok)
	require.NoError(t, err)
	assert.Equal(t, api.NodeStateAdmitted, got.Status.State)
}

func mustNodeStatus(t *testing.T, e *env, n *api.Node) *api.Node {
	t.Helper()
	out, err := e.api.PutNodeStatus(ctx(t), n)
	require.NoError(t, err)
	return out
}

func TestPutNodeStatus_ValidatesAndWritesStatusOnly(t *testing.T) {
	e := newEnv(t)
	n := e.createNode(t, "n1")

	badState := *n
	badState.Status.State = "Gone"
	_, err := e.api.PutNodeStatus(ctx(t), &badState)
	assert.ErrorIs(t, err, api.ErrInvalid)

	badCondition := *n
	badCondition.Status.State = api.NodeStateDiscovered
	badCondition.Status.Conditions = []api.Condition{{Type: api.NodeReady, Status: "Maybe"}}
	_, err = e.api.PutNodeStatus(ctx(t), &badCondition)
	assert.ErrorIs(t, err, api.ErrInvalid)

	next := *n
	next.Spec.Admitted = true
	next.Status.State = api.NodeStateDiscovered
	next.Status.Addresses = []string{"10.0.0.1"}
	written, err := e.api.PutNodeStatus(ctx(t), &next)
	require.NoError(t, err)
	assert.False(t, written.Spec.Admitted, "a status write does not change the spec")
	assert.Equal(t, []string{"10.0.0.1"}, written.Status.Addresses)

	_, err = e.api.PutNodeStatus(ctx(t), &next)
	assert.ErrorIs(t, err, api.ErrConflict)
	absent := node("absent")
	absent.Metadata.ResourceVersion = "2"
	absent.Status.State = api.NodeStateDiscovered
	_, err = e.api.PutNodeStatus(ctx(t), absent)
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestPutLease_NamesAnExistingNode(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutLease(ctx(t), lease("n1"))
	assert.ErrorIs(t, err, api.ErrInvalid, "no Node of that name")
	_, err = e.api.GetLease(ctx(t), "n1")
	assert.ErrorIs(t, err, api.ErrNotFound)

	e.createNode(t, "n1")
	created, err := e.api.PutLease(ctx(t), lease("n1"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
}

func TestPutLease_RenewsOnTheCurrentVersion(t *testing.T) {
	e := newEnv(t)
	e.createNode(t, "n1")
	first, err := e.api.PutLease(ctx(t), lease("n1"))
	require.NoError(t, err)

	renew := *first
	renew.Spec.RenewTime = time.Now().UTC().Add(time.Second)
	second, err := e.api.PutLease(ctx(t), &renew)
	require.NoError(t, err)
	assert.NotEqual(t, first.Metadata.ResourceVersion, second.Metadata.ResourceVersion)

	_, err = e.api.PutLease(ctx(t), first)
	assert.ErrorIs(t, err, api.ErrConflict)
	_, err = e.api.PutLease(ctx(t), lease("n1"))
	assert.ErrorIs(t, err, api.ErrAlreadyExists)
}
