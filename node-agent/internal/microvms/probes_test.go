package microvms

// These tests are internal for the reason restart_test.go is: they drive the
// rounds on a fake clock.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	probePassing = v1alpha2.ProbeStatus_PROBE_STATUS_PASSING
	probeFailing = v1alpha2.ProbeStatus_PROBE_STATUS_FAILING
)

func tcpProbe(port int32) *api.Probe {
	return &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Number: port}}}
}

// beginMicroVM stores m and runs it until mvmd holds it Running.
func (r *rig) beginMicroVM(m api.MicroVM) {
	r.t.Helper()
	r.put(m)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
}

func (r *rig) stops() int {
	n := 0
	for _, c := range r.mvmd.Calls() {
		if c == "StopMachine "+rigID {
			n++
		}
	}
	return n
}

func TestProbes_ReachMvmdsCreateRequest(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.Ports = []api.MicroVMPort{{Name: "web", Port: 8080, Protocol: api.ProtocolTCP}}
	m.Spec.ReadinessProbe = &api.Probe{
		HTTPGet:             &api.HTTPGetAction{Port: api.PortRef{Name: "web"}},
		InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 3, SuccessThreshold: 2, FailureThreshold: 4,
	}
	m.Spec.LivenessProbe = &api.Probe{Exec: &api.ExecAction{Command: []string{"/bin/check", "--quick"}}}
	r := newRig(t, nil)
	r.beginMicroVM(m)

	req, ok := r.mvmd.CreateRequest(rigID)
	require.True(t, ok)
	require.Len(t, req.Probes, 2)
	ready, live := req.Probes[0], req.Probes[1]
	assert.Equal(t, "readiness", ready.Name)
	assert.Equal(t, &v1alpha2.HttpGetProbe{Port: 8080, Path: "/"}, ready.GetHttpGet(), "a named port is its number and an empty path is /")
	assert.Equal(t, 2*time.Second, ready.InitialDelay.AsDuration())
	assert.Equal(t, 5*time.Second, ready.Period.AsDuration())
	assert.Equal(t, 3*time.Second, ready.Timeout.AsDuration())
	assert.EqualValues(t, 2, ready.SuccessThreshold)
	assert.EqualValues(t, 4, ready.FailureThreshold)
	assert.Equal(t, "liveness", live.Name)
	assert.Equal(t, []string{"/bin/check", "--quick"}, live.GetExec().GetArgv())
	assert.Nil(t, live.Period, "a zero field is left unset, which mvmd defaults as the API does")
	assert.Nil(t, live.InitialDelay)
	assert.Zero(t, live.FailureThreshold)

	control := newRig(t, nil)
	control.begin(api.RunAlways)
	controlReq, ok := control.mvmd.CreateRequest(rigID)
	require.True(t, ok)
	assert.Empty(t, controlReq.Probes, "a MicroVM with no probe sends none")
}

func TestProbes_AnUnnamedPortIsTheMicroVMsFailure(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: "web"}}}
	r := newRig(t, nil)
	r.put(m)
	r.mustRound()

	s := r.gaon.MicroVM("m1").Status
	assert.Equal(t, api.MicroVMFailed, s.Phase)
	assert.Equal(t, ReasonInvalidSpec, r.ready().Reason)
	assert.Contains(t, r.ready().Message, "spec.readinessProbe.tcpSocket.port")
	assert.Empty(t, r.mvmd.IDs(), "no machine is made for it")

	declared := rigMicroVM(1, api.RunAlways)
	declared.Spec.Ports = []api.MicroVMPort{{Name: "web", Port: 8080, Protocol: api.ProtocolTCP}}
	declared.Spec.ReadinessProbe = &api.Probe{TCPSocket: &api.TCPSocketAction{Port: api.PortRef{Name: "web"}}}
	control := newRig(t, nil)
	control.beginMicroVM(declared)
	req, ok := control.mvmd.CreateRequest(rigID)
	require.True(t, ok)
	require.Len(t, req.Probes, 1)
	assert.EqualValues(t, 8080, req.Probes[0].GetTcp().GetPort(), "a declared name is accepted and sent as its number")
}

func TestProbes_AProbeMvmdRefusesIsTheMicroVMsRejection(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.ReadinessProbe = &api.Probe{TCPSocket: tcpProbe(80).TCPSocket, PeriodSeconds: 25 * 3600}
	r := newRig(t, nil)
	r.put(m)
	r.mustRound()

	assert.Equal(t, api.MicroVMFailed, r.gaon.MicroVM("m1").Status.Phase)
	assert.Equal(t, ReasonRejected, r.ready().Reason)
	assert.Contains(t, r.ready().Message, `probe "readiness"`)

	control := rigMicroVM(1, api.RunAlways)
	control.Spec.ReadinessProbe = &api.Probe{TCPSocket: tcpProbe(80).TCPSocket, PeriodSeconds: 24 * 3600}
	ok := newRig(t, nil)
	ok.beginMicroVM(control)
}

func TestReadiness_IsFalseUntilMvmdReportsTheProbePassing(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.ReadinessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)

	assert.Equal(t, api.MicroVMRunning, r.gaon.MicroVM("m1").Status.Phase)
	assert.Equal(t, api.ConditionFalse, r.ready().Status)
	assert.Equal(t, ReasonReadinessProbePending, r.ready().Reason, "mvmd reports the probe UNKNOWN until it meets a threshold")

	r.mvmd.SetProbe(rigID, "readiness", probeFailing, "connection refused")
	r.mustRound()
	assert.Equal(t, api.ConditionFalse, r.ready().Status)
	assert.Equal(t, ReasonReadinessProbeFailing, r.ready().Reason)
	assert.Equal(t, "connection refused", r.ready().Message)

	r.mvmd.SetProbe(rigID, "readiness", probePassing, "")
	r.mustRound()
	assert.Equal(t, api.ConditionTrue, r.ready().Status)
	assert.Equal(t, ReasonRunning, r.ready().Reason)

	r.mvmd.SetProbe(rigID, "readiness", probeFailing, "connection refused")
	r.mustRound()
	assert.Equal(t, api.ConditionFalse, r.ready().Status, "a probe that fails again makes the MicroVM not ready again")
	assert.Equal(t, api.MicroVMRunning, r.gaon.MicroVM("m1").Status.Phase, "readiness does not end the run")
	assert.Equal(t, 0, r.stops())

	control := newRig(t, nil)
	control.begin(api.RunAlways)
	assert.Equal(t, api.ConditionTrue, control.ready().Status, "a MicroVM with no readiness probe is ready once it runs")
}

func TestReadiness_IsFalseAgainAfterARestartUntilTheProbePassesAgain(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.ReadinessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)
	r.mvmd.SetProbe(rigID, "readiness", probePassing, "")
	r.mustRound()
	require.Equal(t, api.ConditionTrue, r.ready().Status)

	r.restartAfter(10 * time.Second)

	assert.Equal(t, api.ConditionFalse, r.ready().Status, "mvmd reports the probe UNKNOWN after a boot")
	assert.Equal(t, ReasonReadinessProbePending, r.ready().Reason)
}

func TestLiveness_AFailingProbeStopsTheMicroVMAndItIsStartedAgainAfterTheBackoff(t *testing.T) {
	for _, strategy := range []api.RunStrategy{api.RunAlways, api.RunRerunOnFailure} {
		t.Run(string(strategy), func(t *testing.T) {
			m := rigMicroVM(1, strategy)
			m.Spec.LivenessProbe = tcpProbe(80)
			r := newRig(t, nil)
			r.beginMicroVM(m)
			// The entrypoint's own record reads as a clean exit: the agent's
			// stop is a failure whatever the record says.
			r.mvmd.SetEntrypointRecord(rigID, exited(0, 0))

			r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
			r.mustRound()

			r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
			assert.Equal(t, 1, r.stops())
			r.clk.Advance(10*time.Second - time.Second)
			r.mustRound()
			r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
			r.clk.Advance(time.Second)
			r.mustRound()
			r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
			assert.Equal(t, 2, r.starts())
			assert.EqualValues(t, 1, r.gaon.MicroVM("m1").Status.RestartCount)

			r.mustRound()
			assert.Equal(t, 1, r.stops(), "the restarted guest reports its probe UNKNOWN and is left running")
		})
	}
}

func TestLiveness_ARestartFollowsTheDoublingBackoff(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)

	for _, wait := range []time.Duration{10 * time.Second, 20 * time.Second} {
		r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
		r.mustRound()
		r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
		r.clk.Advance(wait - time.Second)
		r.mustRound()
		r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
		r.clk.Advance(time.Second)
		r.mustRound()
		r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	}
}

func TestLiveness_APassingProbeLeavesTheMicroVMRunning(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)
	r.mvmd.SetProbe(rigID, "liveness", probePassing, "")

	for i := 0; i < 3; i++ {
		r.clk.Advance(time.Hour)
		r.mustRound()
	}

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	assert.Zero(t, r.stops())
	assert.Equal(t, 1, r.starts())
}

func TestLiveness_AFailingProbeOfAOnceMicroVMFailsItAndItIsNotStartedAgain(t *testing.T) {
	m := rigMicroVM(1, api.RunOnce)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)

	r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	assert.Equal(t, 1, r.starts())
	assert.Equal(t, api.MicroVMFailed, r.gaon.MicroVM("m1").Status.Phase)
	assert.Equal(t, ReasonExitFailed, r.ready().Reason)
	assert.Equal(t, "the liveness probe failed: connection refused", r.ready().Message)
}

func TestLiveness_AHaltedMicroVMIsStoppedAsHaltedAndNotRestarted(t *testing.T) {
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)
	r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
	m.Spec.RunStrategy = api.RunHalted
	r.put(m)

	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_STOPPED)
	assert.Equal(t, 1, r.starts())
	assert.Equal(t, api.MicroVMStopped, r.gaon.MicroVM("m1").Status.Phase)
}

func TestGracePeriod_ReachesEveryStop(t *testing.T) {
	seven, zero := int32(7), int32(0)
	tests := []struct {
		name  string
		grace *int32
		want  time.Duration
	}{
		{"an absent period is the default of 30 seconds", nil, 30 * time.Second},
		{"a period of 7 seconds", &seven, 7 * time.Second},
		{"a period of 0 is sent as 0 and not left unset", &zero, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fresh := func() api.MicroVM {
				m := rigMicroVM(1, api.RunAlways)
				m.Spec.TerminationGracePeriodSeconds = tt.grace
				m.Spec.LivenessProbe = tcpProbe(80)
				return m
			}

			t.Run("halting a running MicroVM", func(t *testing.T) {
				r := newRig(t, nil)
				m := fresh()
				r.beginMicroVM(m)
				m.Spec.RunStrategy = api.RunHalted
				r.put(m)
				r.mustRound()
				r.requireGrace(tt.want)
			})
			t.Run("a failing liveness probe", func(t *testing.T) {
				r := newRig(t, nil)
				m := fresh()
				r.beginMicroVM(m)
				r.mvmd.SetProbe(rigID, "liveness", probeFailing, "")
				r.mustRound()
				r.requireGrace(tt.want)
			})
			t.Run("stopping a faulted MicroVM before a restart", func(t *testing.T) {
				r := newRig(t, nil)
				m := fresh()
				r.beginMicroVM(m)
				r.mvmd.SetState(rigID, v1alpha2.MachineState_MACHINE_STATE_FAULTED, "gone")
				r.mustRound()
				r.clk.Advance(time.Hour)
				r.mustRound()
				r.requireGrace(tt.want)
			})
			t.Run("removing a MicroVM bound elsewhere", func(t *testing.T) {
				r := newRig(t, nil)
				m := fresh()
				r.beginMicroVM(m)
				m.Spec.NodeName = "n2"
				r.put(m)
				r.mustRound()
				r.requireGrace(tt.want)
			})
		})
	}
}

func TestLiveness_ACleanExitAfterTheRestartIsNotJudgedByTheEarlierProbeFailure(t *testing.T) {
	m := rigMicroVM(1, api.RunRerunOnFailure)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)
	r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
	r.mustRound()
	r.clk.Advance(10 * time.Second)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)

	r.exit(exited(0, 0))
	r.mustRound()
	r.clk.Advance(time.Hour)
	r.mustRound()

	assert.Equal(t, api.MicroVMSucceeded, r.gaon.MicroVM("m1").Status.Phase)
	assert.Equal(t, 2, r.starts(), "the clean exit is not restarted")
}

func TestLiveness_TheRecordOfALivenessStopIsComparedWithTheNextExit(t *testing.T) {
	m := rigMicroVM(1, api.RunRerunOnFailure)
	m.Spec.LivenessProbe = tcpProbe(80)
	r := newRig(t, nil)
	r.beginMicroVM(m)
	// The entrypoint handles the stop's shutdown request and exits 0.
	r.mvmd.SetEntrypointRecord(rigID, exited(0, 0))
	r.mvmd.SetProbe(rigID, "liveness", probeFailing, "connection refused")
	r.mustRound()
	r.clk.Advance(10 * time.Second)
	r.mustRound()
	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)

	// The next run ends with no record of its own, so mvmd still holds the
	// earlier one, which is not evidence of a clean exit.
	r.mvmd.SetState(rigID, v1alpha2.MachineState_MACHINE_STATE_STOPPED, "the VMM ended with no call asking it to")
	r.mustRound()
	r.clk.Advance(20 * time.Second)
	r.mustRound()

	r.requireState(v1alpha2.MachineState_MACHINE_STATE_RUNNING)
	assert.Equal(t, 3, r.starts(), "the stale clean record is judged failed and the MicroVM restarted")
}

func TestGracePeriod_ARemovalOfAMicroVMTheAgentNoLongerHoldsIsTheDefault(t *testing.T) {
	seven := int32(7)
	m := rigMicroVM(1, api.RunAlways)
	m.Spec.TerminationGracePeriodSeconds = &seven
	r := newRig(t, nil)
	r.beginMicroVM(m)

	r.gaon.DeleteMicroVM("m1")
	r.svc.cache.replace(nil)
	r.mustRound()

	r.requireGrace(30 * time.Second)
}

// requireGrace checks that the one stop mvmd received carried grace, set.
func (r *rig) requireGrace(want time.Duration) {
	r.t.Helper()
	stops := r.mvmd.StopRequests()
	require.Len(r.t, stops, 1)
	require.NotNil(r.t, stops[0].GracePeriod, "the grace period is set, whatever its value")
	assert.Equal(r.t, durationpb.New(want).AsDuration(), stops[0].GracePeriod.AsDuration())
}
