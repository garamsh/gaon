// Package machines runs the Machines bound to this node on mvmd and reports
// their status.
package machines

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// Reasons the agent writes in a Machine's Ready condition.
const (
	ReasonInvalidSpec = "InvalidSpec"
	ReasonRejected    = "Rejected"
	ReasonCreated     = "Created"
	ReasonRunning     = "Running"
	ReasonStopped     = "Stopped"
	ReasonFaulted     = "Faulted"
	ReasonExitFailed  = "ExitFailed"
	ReasonSuspended   = "Suspended"
)

// ConditionReady is the type of the condition that names why a Machine is in
// its phase.
const ConditionReady = "Ready"

var (
	idPattern    = regexp.MustCompile(`^(.+)-e([0-9]+)$`)
	imagePattern = regexp.MustCompile(`^([^@:]+(?::[0-9]+)?(?:/[^@:]+)*)@(sha256:[0-9a-f]{64})$`)
)

// machineID is the identifier mvmd holds a Machine under. It carries the
// epoch, so a binding of the same Machine at a later epoch is a different
// mvmd machine and the earlier one is removed, never reused.
func machineID(name string, epoch api.Epoch) string {
	return fmt.Sprintf("%s-e%d", name, epoch)
}

// parseID is the inverse of machineID. It reports false for an identifier the
// agent did not create.
func parseID(id string) (string, api.Epoch, bool) {
	m := idPattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	epoch, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], api.Epoch(epoch), true
}

// invalidSpec marks an error that no retry fixes: the Machine's spec cannot
// be run as written.
type invalidSpec struct{ err error }

func (e invalidSpec) Error() string { return e.err.Error() }
func (e invalidSpec) Unwrap() error { return e.err }

// createRequest translates a Machine's spec into mvmd's create request.
func createRequest(m *api.Machine) (*v1alpha2.CreateMachineRequest, *v1alpha2.ImageRef, error) {
	image := imagePattern.FindStringSubmatch(m.Spec.Image)
	if image == nil {
		return nil, nil, invalidSpec{fmt.Errorf("spec.image %q is not <repository>@sha256:<64 hex digits>", m.Spec.Image)}
	}
	cpu := m.Spec.Resources.Limits.CPU
	if cpu == "" {
		cpu = m.Spec.Resources.Requests.CPU
	}
	memory := m.Spec.Resources.Limits.Memory
	if memory == "" {
		memory = m.Spec.Resources.Requests.Memory
	}
	if cpu == "" || memory == "" {
		return nil, nil, invalidSpec{errors.New("spec.resources names no cpu or no memory")}
	}
	cpuCount, err := cpus(cpu)
	if err != nil {
		return nil, nil, invalidSpec{err}
	}
	memoryBytes, err := byteCount(memory)
	if err != nil {
		return nil, nil, invalidSpec{err}
	}
	req := &v1alpha2.CreateMachineRequest{
		MachineId:   machineID(m.Metadata.Name, m.Spec.BindingEpoch),
		RequestId:   machineID(m.Metadata.Name, m.Spec.BindingEpoch),
		ImageDigest: image[2],
		Kernel:      m.Spec.Kernel,
		Resources:   &v1alpha2.Resources{CpuCount: cpuCount, MemoryBytes: memoryBytes},
	}
	if m.Spec.RootDiskSize != "" {
		if req.RootDiskBytes, err = byteCount(m.Spec.RootDiskSize); err != nil {
			return nil, nil, invalidSpec{err}
		}
	}
	return req, &v1alpha2.ImageRef{Repository: image[1], Digest: image[2]}, nil
}

// outcome is what the agent knows about a Machine after a round, from which
// its status follows.
type outcome struct {
	// runtime is mvmd's Machine, nil when mvmd holds none.
	runtime *v1alpha2.Machine
	// failure, when set, is a failure the agent met and the reason for it.
	failure    error
	failReason string
	// clean is set when the entrypoint exited cleanly and the run strategy
	// does not start the Machine again.
	clean bool
	// exitFailure, when set, is how a Machine that is not started again
	// failed.
	exitFailure string
}

// machineStatus maps an outcome to the status the agent writes for m, adding
// the restarts not yet written to restartCount and keeping the
// fields it does not own and the transition time of a condition that did
// not change.
func machineStatus(m *api.Machine, o outcome, now time.Time, restarts int32) api.MachineStatus {
	next := api.MachineStatus{BindingEpoch: m.Spec.BindingEpoch, ObservedGeneration: m.Metadata.Generation}
	var ready api.Condition
	switch {
	case o.failure != nil:
		next.Phase = api.MachineFailed
		ready = api.Condition{Type: ConditionReady, Status: api.ConditionFalse, Reason: o.failReason, Message: o.failure.Error()}
	case o.runtime == nil:
		next.Phase = api.MachinePending
		ready = api.Condition{Type: ConditionReady, Status: api.ConditionFalse, Reason: ReasonCreated}
		if m.Spec.RunStrategy == api.RunHalted {
			next.Phase = api.MachineStopped
			ready.Reason = ReasonStopped
		}
	default:
		next.Addresses = o.runtime.GetAddresses()
		next.Phase, ready = fromState(m, o.runtime)
		switch {
		case o.clean:
			next.Phase = api.MachineSucceeded
			ready.Message = "the entrypoint exited cleanly and is not started again"
		case o.exitFailure != "":
			next.Phase = api.MachineFailed
			ready.Reason = ReasonExitFailed
			ready.Message = o.exitFailure
		}
	}
	if m.Status.BindingEpoch == m.Spec.BindingEpoch {
		next.RestartCount, next.LastActiveTime, next.LastExit = m.Status.RestartCount, m.Status.LastActiveTime, m.Status.LastExit
	}
	next.RestartCount += restarts
	ready.LastTransitionTime = transition(m, ready, now)
	next.Conditions = []api.Condition{ready}
	return next
}

func fromState(m *api.Machine, r *v1alpha2.Machine) (api.MachinePhase, api.Condition) {
	cond := func(status api.ConditionStatus, reason, message string) api.Condition {
		return api.Condition{Type: ConditionReady, Status: status, Reason: reason, Message: message}
	}
	switch r.GetState() {
	case v1alpha2.MachineState_MACHINE_STATE_RUNNING:
		return api.MachineRunning, cond(api.ConditionTrue, ReasonRunning, "")
	case v1alpha2.MachineState_MACHINE_STATE_PAUSED:
		return api.MachineSuspended, cond(api.ConditionFalse, ReasonSuspended, "")
	case v1alpha2.MachineState_MACHINE_STATE_FAULTED:
		return api.MachineFailed, cond(api.ConditionFalse, ReasonFaulted, r.GetLastFailureReason())
	case v1alpha2.MachineState_MACHINE_STATE_STOPPED:
		switch m.Spec.RunStrategy {
		case api.RunHalted:
			return api.MachineStopped, cond(api.ConditionFalse, ReasonStopped, "")
		}
		return api.MachinePending, cond(api.ConditionFalse, ReasonStopped, "the Machine stopped and is started again")
	default:
		return api.MachinePending, cond(api.ConditionFalse, ReasonCreated, strings.TrimSpace(r.GetLastFailureReason()))
	}
}

// transition returns the time the Ready condition last changed: the stored
// one when its status and reason are as before, else now.
func transition(m *api.Machine, ready api.Condition, now time.Time) *time.Time {
	if m.Status.BindingEpoch == m.Spec.BindingEpoch {
		for _, c := range m.Status.Conditions {
			if c.Type == ready.Type && c.Status == ready.Status && c.Reason == ready.Reason && c.LastTransitionTime != nil {
				return c.LastTransitionTime
			}
		}
	}
	t := now.UTC()
	return &t
}
