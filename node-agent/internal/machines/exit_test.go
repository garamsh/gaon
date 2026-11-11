package machines

// These tests are internal for the reason restart_test.go gives: the rig
// drives reconcileAll on a clock that moves only when told to.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

func TestOnce_AFailedExitIsFailedWithAReasonNamingTheExit(t *testing.T) {
	notExited := func(s v1alpha2.ExecStatus) *v1alpha2.ExecRecord {
		return &v1alpha2.ExecRecord{ExecId: "entrypoint", Status: s}
	}
	tests := []struct {
		name    string
		prepare func(*rig)
		record  *v1alpha2.ExecRecord
		state   v1alpha2.MachineState
		reason  string
		message string
	}{
		{"a non-zero exit code", func(*rig) {}, exited(3, 0), v1alpha2.MachineState_MACHINE_STATE_STOPPED, ReasonExitFailed, "code 3"},
		{"a signal with code zero", func(*rig) {}, exited(0, 9), v1alpha2.MachineState_MACHINE_STATE_STOPPED, ReasonExitFailed, "signal 9"},
		{"a record that says cut short", func(*rig) {}, notExited(v1alpha2.ExecStatus_EXEC_STATUS_CUT_SHORT), v1alpha2.MachineState_MACHINE_STATE_STOPPED, ReasonExitFailed, "CUT_SHORT"},
		{"a read that ends with no record", func(r *rig) { r.mvmd.SilenceEntrypoint(rigID, true) }, nil, v1alpha2.MachineState_MACHINE_STATE_STOPPED, ReasonExitFailed, "no exit record"},
		{"a faulted Machine", func(*rig) {}, nil, v1alpha2.MachineState_MACHINE_STATE_FAULTED, ReasonFaulted, "gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.begin(api.RunOnce)
			tt.prepare(r)
			if tt.record != nil {
				r.mvmd.SetEntrypointRecord(rigID, tt.record)
			}
			r.mvmd.SetState(rigID, tt.state, "gone")

			r.mustRound()
			r.clk.Advance(time.Hour)
			r.mustRound()

			s := r.gaon.Machine("m1").Status
			assert.Equal(t, api.MachineFailed, s.Phase)
			require.Len(t, s.Conditions, 1)
			assert.Equal(t, ConditionReady, s.Conditions[0].Type)
			assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
			assert.Equal(t, tt.reason, s.Conditions[0].Reason)
			assert.Contains(t, s.Conditions[0].Message, tt.message)
			assert.Equal(t, 1, r.starts(), "a Once Machine is not started again")
			assert.EqualValues(t, 0, s.RestartCount)
		})
	}
}

func TestOnce_ACleanExitIsSucceeded(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunOnce)
	r.exit(exited(0, 0))

	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	s := r.gaon.Machine("m1").Status
	assert.Equal(t, api.MachineSucceeded, s.Phase)
	require.Len(t, s.Conditions, 1)
	assert.Equal(t, api.ConditionFalse, s.Conditions[0].Status)
	assert.Equal(t, ReasonStopped, s.Conditions[0].Reason)
	assert.Equal(t, 1, r.starts(), "a Once Machine is not started again")
}

func TestOnce_AMachineStillRunningIsNotJudged(t *testing.T) {
	r := newRig(t, nil)
	r.begin(api.RunOnce)

	r.mustRound()

	assert.Equal(t, api.MachineRunning, r.gaon.Machine("m1").Status.Phase)
}
