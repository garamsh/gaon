package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/scheduler/internal/scheduler"
)

func TestVolumeNode_KeepsOnlyTheNodeTheLocalVolumesArePinnedTo(t *testing.T) {
	deleting := volume("v", api.StorageClassLocal, "b")
	deletedAt := time.Unix(1, 0)
	deleting.Metadata.DeletionTimestamp = &deletedAt

	tests := []struct {
		name    string
		volumes []*api.Volume
		uses    []string
		wantOn  string // the Node the Machine is bound to, empty when none
		message string // part of the Unschedulable message when it is not bound
	}{
		{name: "names no Volume", wantOn: "a"},
		{name: "control: unpinned", volumes: []*api.Volume{volume("v", api.StorageClassLocal, "")}, uses: []string{"v"}, wantOn: "a"},
		{name: "pinned to b", volumes: []*api.Volume{volume("v", api.StorageClassLocal, "b")}, uses: []string{"v"}, wantOn: "b"},
		{
			name:    "one pinned to b and one unpinned",
			volumes: []*api.Volume{volume("p", api.StorageClassLocal, "b"), volume("u", api.StorageClassLocal, "")},
			uses:    []string{"u", "p"},
			wantOn:  "b",
		},
		{
			name:    "two pinned to b",
			volumes: []*api.Volume{volume("p", api.StorageClassLocal, "b"), volume("q", api.StorageClassLocal, "b")},
			uses:    []string{"p", "q"},
			wantOn:  "b",
		},
		{
			name:    "a class that is not local is not pinned by its nodeName",
			volumes: []*api.Volume{volume("v", "ceph", "b")},
			uses:    []string{"v"},
			wantOn:  "a",
		},
		{
			name:    "pinned to different Nodes",
			volumes: []*api.Volume{volume("p", api.StorageClassLocal, "a"), volume("q", api.StorageClassLocal, "b")},
			uses:    []string{"p", "q"},
			message: `VolumeNode refused 2 (volumes "p" and "q" are pinned to different Nodes)`,
		},
		{name: "does not exist", uses: []string{"v"}, message: `VolumeNode refused 2 (volume "v" does not exist)`},
		{name: "being deleted", volumes: []*api.Volume{deleting}, uses: []string{"v"}, message: `VolumeNode refused 2 (volume "v" is being deleted)`},
		{
			name:    "pinned to a Node that is not listed",
			volumes: []*api.Volume{volume("v", api.StorageClassLocal, "gone")},
			uses:    []string{"v"},
			message: `VolumeNode refused 2 (local volumes are pinned to Node "gone")`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, client := newFakeAPI(t)
			f.addNode(node("a", "4", "8Gi"))
			f.addNode(node("b", "4", "8Gi"))
			for _, v := range tt.volumes {
				f.addVolume(v)
			}
			f.addMachine(withVolumes(newMachine("m1", "1", "1Gi"), tt.uses...))

			reconcile(t, client)

			m := f.machine("m1")
			assert.Equal(t, tt.wantOn, m.Spec.NodeName)
			if tt.wantOn != "" {
				assert.Equal(t, 1, f.bindingCount())
				return
			}
			assert.Zero(t, f.bindingCount())
			require.Len(t, m.Status.Conditions, 1)
			assert.Equal(t, scheduler.ReasonUnschedulable, m.Status.Conditions[0].Reason)
			assert.Contains(t, m.Status.Conditions[0].Message, tt.message)
		})
	}
}

func TestVolumeNode_TheNodeThatHoldsTheVolumeStillHasToFit(t *testing.T) {
	f, client := newFakeAPI(t)
	f.addNode(node("a", "1", "1Gi"))
	f.addNode(node("b", "4", "8Gi"))
	f.addVolume(volume("v", api.StorageClassLocal, "a"))
	f.addMachine(withVolumes(newMachine("m1", "2", "1Gi"), "v"))

	reconcile(t, client)

	m := f.machine("m1")
	assert.Empty(t, m.Spec.NodeName, "a holds the Volume and is too small; b is large enough and does not hold it")
	require.Len(t, m.Status.Conditions, 1)
	assert.Contains(t, m.Status.Conditions[0].Message, "NodeResourcesFit refused 1")
	assert.Contains(t, m.Status.Conditions[0].Message, `VolumeNode refused 1 (local volumes are pinned to Node "a")`)
}

func TestVolumeNode_AMachineWhoseNodeExpiredIsNotReboundAwayFromItsLocalVolume(t *testing.T) {
	setup := func(t *testing.T, v *api.Volume) (*fakeAPI, api.Machine) {
		f, client := newFakeAPI(t)
		f.addNode(withCondition(node("n1", "4", "8Gi"), api.NodeLeaseExpired, api.ConditionTrue))
		f.addNode(node("n2", "4", "8Gi"))
		f.addVolume(v)
		f.addMachine(withVolumes(bound(newMachine("m1", "1", "1Gi"), "n1", 1), "v"))
		reconcile(t, client)
		return f, f.machine("m1")
	}

	f, m := setup(t, volume("v", api.StorageClassLocal, "n1"))
	assert.Equal(t, "n1", m.Spec.NodeName)
	assert.Equal(t, api.Epoch(1), m.Spec.BindingEpoch)
	assert.Zero(t, f.bindingCount())
	assert.Zero(t, f.statusPutCount(), "a bound Machine keeps the status of its dead Node's epoch")

	_, m = setup(t, volume("v", api.StorageClassLocal, ""))
	assert.Equal(t, "n2", m.Spec.NodeName, "control: the same Machine with an unpinned Volume is rebound")
	assert.Equal(t, api.Epoch(2), m.Spec.BindingEpoch)
}

func TestReconcile_AForbiddenVolumeListIsReportedAndMachinesThatNameNoVolumeAreStillBound(t *testing.T) {
	setup := func(t *testing.T, fail *api.APIError) (*fakeAPI, error) {
		f, client := newFakeAPI(t)
		f.volumesFail = fail
		f.addNode(node("a", "4", "8Gi"))
		f.addNode(node("b", "4", "8Gi"))
		f.addVolume(volume("v", api.StorageClassLocal, "b"))
		f.addMachine(newMachine("plain", "1", "1Gi"))
		f.addMachine(withVolumes(newMachine("disk", "1", "1Gi"), "v"))
		return f, newService(client).Reconcile(context.Background())
	}

	f, err := setup(t, api.NewAPIError(api.ReasonForbidden, "no"))
	require.ErrorIs(t, err, api.ErrForbidden)
	assert.Contains(t, err.Error(), "list volumes")
	assert.Equal(t, "a", f.machine("plain").Spec.NodeName)
	assert.Empty(t, f.machine("disk").Spec.NodeName, "its Volume cannot be read, so no Node is known to take it")
	assert.Zero(t, f.statusPutCount(), "no Unschedulable condition is written on a guess")

	f, err = setup(t, nil)
	require.NoError(t, err)
	assert.Equal(t, "a", f.machine("plain").Spec.NodeName, "control: the list succeeds")
	assert.Equal(t, "b", f.machine("disk").Spec.NodeName)
}

func TestReconcile_AVolumeListThatFailsOtherwiseEndsThePass(t *testing.T) {
	f, client := newFakeAPI(t)
	f.volumesFail = api.NewAPIError(api.ReasonInternalError, "boom")
	f.addNode(node("a", "4", "8Gi"))
	f.addMachine(newMachine("plain", "1", "1Gi"))

	err := newService(client).Reconcile(context.Background())

	require.Error(t, err)
	assert.NotErrorIs(t, err, api.ErrForbidden)
	assert.Empty(t, f.machine("plain").Spec.NodeName)
}
