package storagelocal_test

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// growing is a Volume of 10Mi whose disk holds 10Mi and whose storage was raised to 20Mi.
func growing(r *rig, phase api.VolumePhase) {
	v := volume("data", node)
	v.Spec.Storage = "20Mi"
	v.Status = api.VolumeStatus{Phase: phase, Capacity: "10485760"}
	if phase == api.VolumeBound {
		v.Status.Attachment = &api.Attachment{MicroVMName: "db", BindingEpoch: 1}
		r.mvmd.AddDisk("volume-data", 10<<20)
		r.mvmd.Hold("db-e1", "volume-data")
	} else {
		r.mvmd.AddDisk("volume-data", 10<<20)
	}
	r.gaon.SetVolume(v)
}

func resizing(r *rig, name string) *api.Condition {
	v, ok := r.gaon.Volume(name)
	if !ok {
		return nil
	}
	i := slices.IndexFunc(v.Status.Conditions, func(c api.Condition) bool { return c.Type == api.VolumeConditionResizing })
	if i < 0 {
		return nil
	}
	return &v.Status.Conditions[i]
}

func resizeCalls(r *rig) []string {
	return slices.DeleteFunc(r.mvmd.Calls(), func(c string) bool { return !strings.HasPrefix(c, "ResizeDisk") })
}

func TestLocal_RunGrowsTheDiskToStorageAndWritesCapacityAndRemovesResizingTogether(t *testing.T) {
	r := newRig(t)
	growing(r, api.VolumeAvailable)
	v := r.volume(t, "data")
	v.Status.Conditions = []api.Condition{
		{Type: api.VolumeConditionResizing, Status: api.ConditionTrue, Reason: "Failed", Message: "earlier"},
		{Type: "Other", Status: api.ConditionTrue, Reason: "Kept"},
	}
	r.gaon.SetVolume(v)
	var seen []api.Condition
	var mu sync.Mutex
	r.mvmd.OnResize(func() {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, *resizing(r, "data"))
	})
	r.run(t)

	eventually(t, "capacity is 20Mi", func() bool {
		got, _ := r.gaon.Volume("data")
		return got.Status.Capacity == "20971520"
	})

	got := r.volume(t, "data")
	assert.Nil(t, resizing(r, "data"), "Resizing goes in the write that raises capacity")
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "Other", got.Status.Conditions[0].Type, "control: another condition stays")
	assert.Equal(t, api.VolumeAvailable, got.Status.Phase)
	for _, st := range r.gaon.Statuses() {
		hasResizing := slices.ContainsFunc(st.Conditions, func(c api.Condition) bool { return c.Type == api.VolumeConditionResizing })
		assert.Equal(t, st.Capacity != "20971520", hasResizing, "Resizing is removed in the write that raises capacity: %+v", st)
	}
	disk, _ := r.mvmd.Disk("volume-data")
	assert.EqualValues(t, 20<<20, disk.GetSizeBytes())
	assert.Equal(t, []string{"ResizeDisk volume-data 20971520"}, resizeCalls(r)[:1])
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen)
	assert.Equal(t, "Failed", seen[0].Reason, "control: a standing condition is not rewritten before the call")
}

func TestLocal_RunShowsInProgressWhileMvmdWorks(t *testing.T) {
	r := newRig(t)
	growing(r, api.VolumeAvailable)
	var during *api.Condition
	var mu sync.Mutex
	r.mvmd.OnResize(func() {
		c := resizing(r, "data")
		mu.Lock()
		defer mu.Unlock()
		if during == nil {
			during = c
		}
	})
	r.run(t)

	eventually(t, "capacity is 20Mi", func() bool {
		got, _ := r.gaon.Volume("data")
		return got.Status.Capacity == "20971520"
	})

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, during, "Resizing stands when mvmd is called")
	assert.Equal(t, api.ConditionTrue, during.Status)
	assert.Equal(t, "InProgress", during.Reason)
}

func TestLocal_RunReportsWhyAGrowthIsOpenAndLeavesThePhase(t *testing.T) {
	tests := []struct {
		name    string
		kind    v1alpha2.OutcomeKind
		reason  string
		message bool
	}{
		{"holder cannot be resized", v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED, "HolderNotResizable", false},
		{"mvmd refuses the size", v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "Failed", true},
		{"mvmd fails otherwise", v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED, "Failed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			growing(r, api.VolumeBound)
			r.mvmd.FailDiskResizesWith(tt.kind)
			r.run(t)

			eventually(t, "Resizing carries "+tt.reason, func() bool {
				c := resizing(r, "data")
				return c != nil && c.Reason == tt.reason
			})

			c := resizing(r, "data")
			assert.Equal(t, api.ConditionTrue, c.Status)
			if tt.message {
				assert.Contains(t, c.Message, "mvmd refuses the growth")
			}
			got := r.volume(t, "data")
			assert.Equal(t, api.VolumeBound, got.Status.Phase, "a growth does not change the phase")
			assert.Equal(t, api.Quantity("10485760"), got.Status.Capacity)
		})
	}
}

func TestLocal_RunTriesAnOpenGrowthAgainAtEachResyncUntilItLands(t *testing.T) {
	r := newRig(t)
	growing(r, api.VolumeBound)
	r.mvmd.FailDiskResizesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_PRECONDITION_FAILED)
	r.run(t)
	eventually(t, "HolderNotResizable", func() bool {
		c := resizing(r, "data")
		return c != nil && c.Reason == "HolderNotResizable"
	})
	eventually(t, "mvmd is called again", func() bool { return len(resizeCalls(r)) >= 3 })

	r.mvmd.FailDiskResizesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_FAILED)
	eventually(t, "the reason follows the new answer", func() bool {
		c := resizing(r, "data")
		return c != nil && c.Reason == "Failed"
	})

	r.mvmd.FailDiskResizesWith(v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED)
	eventually(t, "capacity is 20Mi", func() bool {
		got, _ := r.gaon.Volume("data")
		return got.Status.Capacity == "20971520"
	})

	assert.Nil(t, resizing(r, "data"))
	assert.Equal(t, api.VolumeBound, r.volume(t, "data").Status.Phase)
}

func TestLocal_RunDoesNotCallResizeDiskWhileStorageIsNotAboveCapacity(t *testing.T) {
	r := newRig(t)
	v := volume("data", node)
	v.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
	r.mvmd.AddDisk("volume-data", 10<<20)
	r.gaon.SetVolume(v)
	other := volume("lowered", node)
	other.Spec.Storage = "5Mi"
	other.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
	r.mvmd.AddDisk("volume-lowered", 10<<20)
	r.gaon.SetVolume(other)
	growing2 := volume("grows", node)
	growing2.Spec.Storage = "20Mi"
	growing2.Status = api.VolumeStatus{Phase: api.VolumeAvailable, Capacity: "10485760"}
	r.mvmd.AddDisk("volume-grows", 10<<20)
	r.gaon.SetVolume(growing2)
	r.run(t)

	eventually(t, "control: a raised one is grown", func() bool {
		got, _ := r.gaon.Volume("grows")
		return got.Status.Capacity == "20971520"
	})
	assert.Equal(t, []string{"ResizeDisk volume-grows 20971520"}, slices.Compact(resizeCalls(r)))
	assert.Nil(t, resizing(r, "data"))
	assert.Nil(t, resizing(r, "lowered"))
}
