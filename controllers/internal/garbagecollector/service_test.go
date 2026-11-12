package garbagecollector_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/garbagecollector"
	"github.com/garamsh/gaon/lib/api"
)

// start runs the collector against f until the test ends and returns once its
// first sweep has finished, with the clock to start the next.
func start(t *testing.T, f *fakeAPI) *fakeClock {
	t.Helper()
	clock := newFakeClock()
	svc := garbagecollector.NewService(f.definitions(), f.clientFor(), clock, garbagecollector.Config{Interval: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
	clock.pending = clock.sweepDone(t)
	return clock
}

func TestRun_DeletesAnObjectOnlyWhenEveryOwnerIsGone(t *testing.T) {
	f := newFakeAPI(t)
	f.add(mvmType, "alive", "uid-alive")
	f.add(volumeType, "all-gone", "uid-v1", ownerOf(mvmType, "gone-1", "uid-1"), ownerOf(mvmType, "gone-2", "uid-2"))
	f.add(volumeType, "one-present", "uid-v2", ownerOf(mvmType, "gone-1", "uid-1"), ownerOf(mvmType, "alive", "uid-alive"))
	f.add(volumeType, "owner-present", "uid-v3", ownerOf(mvmType, "alive", "uid-alive"))
	f.add(volumeType, "no-owner", "uid-v4")
	require.True(t, f.has(mvmType, "alive"), "setup: the present owner is stored")
	require.False(t, f.has(mvmType, "gone-1"), "setup: the other owners are not")

	start(t, f)

	assert.Equal(t, []string{"Volume/all-gone"}, f.deleted())
	for _, kept := range []string{"one-present", "owner-present", "no-owner"} {
		assert.True(t, f.has(volumeType, kept), kept)
	}
}

func TestRun_CountsAnOwnerWhoseUidDiffersAsGone(t *testing.T) {
	f := newFakeAPI(t)
	f.add(mvmType, "web", "uid-new")
	f.add(volumeType, "of-the-old-web", "uid-v1", ownerOf(mvmType, "web", "uid-old"))
	f.add(volumeType, "of-this-web", "uid-v2", ownerOf(mvmType, "web", "uid-new"))
	require.True(t, f.has(mvmType, "web"), "setup: an owner named web is stored, so only its uid separates the two")

	start(t, f)

	assert.Equal(t, []string{"Volume/of-the-old-web"}, f.deleted())
	assert.True(t, f.has(volumeType, "of-this-web"), "control: the matching uid keeps its dependent")
}

func TestRun_TreatsAnUidThatIsMissingOnEitherSideAsUnknownAndMatchesTheName(t *testing.T) {
	f := newFakeAPI(t)
	f.add(mvmType, "with-uid", "uid-1")
	f.add(mvmType, "without-uid", "")
	f.add(volumeType, "ref-without-uid-owner-present", "uid-v1", ownerOf(mvmType, "with-uid", ""))
	f.add(volumeType, "owner-without-uid-ref-has-one", "uid-v2", ownerOf(mvmType, "without-uid", "uid-x"))
	f.add(volumeType, "ref-without-uid-owner-absent", "uid-v3", ownerOf(mvmType, "absent", ""))
	require.True(t, f.has(mvmType, "without-uid"), "setup: an owner with no uid is stored")

	start(t, f)

	assert.Equal(t, []string{"Volume/ref-without-uid-owner-absent"}, f.deleted())
	assert.True(t, f.has(volumeType, "ref-without-uid-owner-present"))
	assert.True(t, f.has(volumeType, "owner-without-uid-ref-has-one"))
}

func TestRun_CollectsObjectsOfRegisteredKindsAndOwnedByThem(t *testing.T) {
	f := newFakeAPI(t)
	f.register(widgetType, true)
	f.register(gizmoType, false)
	f.add(mvmType, "alive", "uid-alive")
	f.add(widgetType, "orphan", "uid-w1", ownerOf(mvmType, "gone", "uid-1"))
	f.add(widgetType, "owned", "uid-w2", ownerOf(mvmType, "alive", "uid-alive"))
	f.add(widgetType, "parent", "uid-w3")
	f.add(volumeType, "of-a-gone-widget", "uid-v1", ownerOf(widgetType, "missing", "uid-m"))
	f.add(volumeType, "of-a-present-widget", "uid-v2", ownerOf(widgetType, "parent", "uid-w3"))
	f.add(gizmoType, "orphan", "uid-g1", ownerOf(mvmType, "gone", "uid-1"))
	require.True(t, f.has(widgetType, "parent"), "setup: the registered owner is stored")

	start(t, f)

	assert.Equal(t, []string{"Volume/of-a-gone-widget", "Widget/orphan"}, f.deleted())
	assert.True(t, f.has(widgetType, "owned"))
	assert.True(t, f.has(volumeType, "of-a-present-widget"))
	assert.True(t, f.has(gizmoType, "orphan"), "control: a kind whose definition is not Established is not collected")
}

func TestRun_KeepsAnObjectWhoseOwnerIsOfAKindItDoesNotKnow(t *testing.T) {
	f := newFakeAPI(t)
	f.add(volumeType, "of-an-unserved-kind", "uid-v1", ownerOf(gizmoType, "x", "uid-x"))
	f.add(volumeType, "of-a-served-kind", "uid-v2", ownerOf(mvmType, "x", "uid-x"))

	start(t, f)

	assert.Equal(t, []string{"Volume/of-a-served-kind"}, f.deleted(), "control: the same shape with a served kind is collected")
	assert.True(t, f.has(volumeType, "of-an-unserved-kind"))
}

func TestRun_KeepsAnObjectWhoseOwnerKindCannotBeListed(t *testing.T) {
	f := newFakeAPI(t)
	f.failList(mvmType)
	f.add(volumeType, "of-a-failing-kind", "uid-v1", ownerOf(mvmType, "x", "uid-x"))
	f.add(volumeType, "of-a-listed-kind", "uid-v2", ownerOf(volumeType, "x", "uid-x"))

	start(t, f)

	assert.Equal(t, []string{"Volume/of-a-listed-kind"}, f.deleted(), "control: a kind that lists is collected")
	assert.True(t, f.has(volumeType, "of-a-failing-kind"))
}

func TestRun_ReadsAnOwnerAbsentFromTheListBeforeDeleting(t *testing.T) {
	f := newFakeAPI(t)
	f.add(mvmType, "created-after-the-list", "uid-1")
	f.hide(mvmType, "created-after-the-list")
	f.add(volumeType, "of-that-owner", "uid-v1", ownerOf(mvmType, "created-after-the-list", "uid-1"))
	f.add(volumeType, "of-a-gone-owner", "uid-v2", ownerOf(mvmType, "gone", "uid-2"))

	start(t, f)

	assert.Equal(t, []string{"Volume/of-a-gone-owner"}, f.deleted(), "control: an owner the read does not find is gone")
	assert.True(t, f.has(volumeType, "of-that-owner"))
}

func TestRun_KeepsAnObjectWhoseOwnerReadFailsAndDeletesOneWhoseOwnerReadsNotFound(t *testing.T) {
	f := newFakeAPI(t)
	f.add(mvmType, "unreadable", "uid-1")
	f.hide(mvmType, "unreadable")
	f.failRead(mvmType, "unreadable")
	f.add(volumeType, "of-an-unreadable-owner", "uid-v1", ownerOf(mvmType, "unreadable", "uid-1"))
	f.add(volumeType, "of-a-missing-owner", "uid-v2", ownerOf(mvmType, "missing", "uid-2"))

	start(t, f)

	assert.Equal(t, []string{"Volume/of-a-missing-owner"}, f.deleted(), "control: an owner whose read answers not found is gone")
	assert.True(t, f.has(volumeType, "of-an-unreadable-owner"))
}

func TestRun_LeavesAlone_AnObjectBeingDeletedAndAKindWithNoDelete(t *testing.T) {
	f := newFakeAPI(t)
	now := time.Now()
	dying := f.add(volumeType, "being-deleted", "uid-v1", ownerOf(mvmType, "gone", "uid-1"))
	dying.Metadata.DeletionTimestamp = &now
	f.add(nodeType, "a-node", "uid-n1", ownerOf(mvmType, "gone", "uid-1"))
	f.add(volumeType, "plain", "uid-v2", ownerOf(mvmType, "gone", "uid-1"))
	require.NotNil(t, dying.Metadata.DeletionTimestamp, "setup: the object carries a deletionTimestamp")

	start(t, f)

	assert.Equal(t, []string{"Volume/plain"}, f.deleted(), "control: the same owner, with neither condition, is collected")
	assert.True(t, f.has(volumeType, "being-deleted"))
	assert.True(t, f.has(nodeType, "a-node"))
}

func TestRun_SweepsAgainAndToleratesAnObjectChangedSinceTheList(t *testing.T) {
	f := newFakeAPI(t)
	f.add(volumeType, "busy", "uid-v1", ownerOf(mvmType, "gone", "uid-1"))
	f.afterList(volumeType, func() {
		f.afterList(volumeType, nil)
		f.touch(volumeType, "busy")
	})

	clock := start(t, f)
	require.Empty(t, f.deleted(), "the delete at the listed version conflicts and is not forced")
	require.True(t, f.has(volumeType, "busy"))
	clock.pending <- time.Now()
	clock.sweepDone(t)

	assert.Equal(t, []string{"Volume/busy"}, f.deleted(), "the next sweep reads the new version and deletes")
}

func TestRun_CollectsASecretAConfigMapAndAServiceAccountWhoseOwnerIsGone(t *testing.T) {
	secretType := api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindSecret, Plural: "secrets"}
	configMapType := api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindConfigMap, Plural: "configmaps"}
	accountType := api.CustomResourceType{Group: api.Group, Version: api.Version, Kind: api.KindServiceAccount, Plural: "serviceaccounts"}
	f := newFakeAPI(t)
	f.add(mvmType, "alive", "uid-alive")
	for _, typ := range []api.CustomResourceType{secretType, configMapType, accountType} {
		f.add(typ, "orphan", "uid-o", ownerOf(mvmType, "gone", "uid-gone"))
		f.add(typ, "owned", "uid-k", ownerOf(mvmType, "alive", "uid-alive"))
	}

	start(t, f)

	assert.ElementsMatch(t, []string{"Secret/orphan", "ConfigMap/orphan", "ServiceAccount/orphan"}, f.deleted())
}
