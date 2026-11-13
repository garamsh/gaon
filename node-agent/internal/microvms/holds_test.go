package microvms_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestHolds_ReturnsAMicroVMBoundHereAtItsEpochAndUIDAndNoOther(t *testing.T) {
	e := start(t, true, nil)
	m := microvm("m1", 2, node, api.RunAlways)
	m.Metadata.UID = "uid-1"
	e.gaon.SetMicroVM(m)
	eventually(t, "mvmd runs m1-e2", e.running("m1-e2"))

	got, ok := e.svc.Holds("m1", 2, "uid-1")
	require.True(t, ok)
	assert.Equal(t, "m1", got.Metadata.Name)

	tests := []struct {
		name  string
		vm    string
		epoch api.Epoch
		uid   api.UID
	}{
		{"another epoch", "m1", 1, "uid-1"},
		{"a later epoch", "m1", 3, "uid-1"},
		{"another uid", "m1", 2, "uid-2"},
		{"another name", "m2", 2, "uid-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := e.svc.Holds(tt.vm, tt.epoch, tt.uid)
			assert.False(t, ok)
		})
	}
}

func TestHolds_DoesNotReturnAMicroVMBoundToAnotherNode(t *testing.T) {
	e := start(t, true, nil)
	m := microvm("m1", 2, "other", api.RunAlways)
	m.Metadata.UID = "uid-1"
	e.gaon.SetMicroVM(m)
	control := microvm("m2", 1, node, api.RunAlways)
	control.Metadata.UID = "uid-2"
	e.gaon.SetMicroVM(control)
	eventually(t, "mvmd runs m2-e1", e.running("m2-e1"))

	_, ok := e.svc.Holds("m1", 2, "uid-1")
	assert.False(t, ok)
	_, ok = e.svc.Holds("m2", 1, "uid-2")
	assert.True(t, ok)
}

// Holds is read from the request goroutines while the loop changes the cache:
// run with -race, it fails without the cache's lock.
func TestHolds_IsSafeToCallWhileTheLoopChangesTheCache(t *testing.T) {
	e := start(t, true, nil)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				e.svc.Holds("m1", 1, "uid-1")
			}
		}
	}()
	for i := range 5 {
		m := microvm("m1", 1, node, api.RunAlways)
		m.Metadata.UID = "uid-1"
		m.Metadata.Generation = int64(i + 1)
		e.gaon.SetMicroVM(m)
		eventually(t, "m1 is held", func() bool { _, ok := e.svc.Holds("m1", 1, "uid-1"); return ok })
	}
	close(stop)
	wg.Wait()
}
