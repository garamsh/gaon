package microvms

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// entrypointExec is the exec identifier of a MicroVM's entrypoint.
const entrypointExec = "entrypoint"

// run is what the agent remembers of one mvmd machine between rounds. It is
// lost when the agent restarts and dropped when the MicroVM is no longer
// wanted here, so a binding at a later epoch starts with none.
type run struct {
	// started is when the agent started the MicroVM or first saw it Running.
	started time.Time
	// exited is set once the exit that ended the run has been judged; failed,
	// last and due then describe it.
	exited bool
	failed bool
	// last is the entrypoint's record as read at that exit, nil when none was.
	last *v1alpha2.ExecRecord
	// due is the earliest time the MicroVM is started again.
	due time.Time
	// streak counts the restarts since the MicroVM last ran for the reset
	// time, and so sets the wait before the next.
	streak int
	// unwritten counts the restarts not yet written to status.restartCount.
	unwritten int32
	// liveness is why the agent stopped the MicroVM for a failing liveness
	// probe, until the next start; the exit it caused is judged failed.
	liveness string
	// halted is set while the agent holds the MicroVM stopped for Halted, so
	// resuming it is a start and not a restart.
	halted bool
	// delivered is the digest of the content last put in each file of the
	// guest whose value comes from a Secret or a ConfigMap, by path. It is
	// nil when the guest holds what mvmd sent at boot, which is not known to
	// be current.
	delivered map[string][sha256.Size]byte
	// token is the state of the ServiceAccount token file in the guest.
	token tokenState
}

func (s *service) runOf(id string) *run {
	r, ok := s.runs[id]
	if !ok {
		r = &run{}
		s.runs[id] = r
	}
	return r
}

// prune drops the memory of every mvmd machine not in wanted.
func (s *service) prune(wanted map[string]bool) {
	for id := range s.runs {
		if !wanted[id] {
			delete(s.runs, id)
		}
	}
}

// wait is the delay before the restart that follows streak earlier ones.
func (s *service) wait(streak int) time.Duration {
	d := s.params.RestartBackoff
	for i := 0; i < streak && d < s.params.RestartBackoffMax; i++ {
		d *= 2
	}
	return min(d, s.params.RestartBackoffMax)
}

// restarts reports whether the run strategy starts a MicroVM again after it
// ends: always, or after a failure.
func restarts(strategy api.RunStrategy) bool {
	return strategy == api.RunAlways || strategy == api.RunRerunOnFailure
}

// restart handles a MicroVM that mvmd holds stopped or faulted under a run
// strategy that restarts it. It judges the exit once, then starts the MicroVM
// again when the wait is over, and not before.
func (s *service) restart(ctx context.Context, m *api.MicroVM, id string, r *v1alpha2.Machine) (outcome, error) {
	st := s.runOf(id)
	now := s.clock.Now()
	if st.halted {
		o, err := s.startAgain(ctx, m, id, r)
		if err != nil || o.failure != nil {
			return o, err
		}
		st.halted = false
		return o, nil
	}
	if !st.exited {
		failed, rec, err := s.judge(ctx, id, r, st)
		if err != nil {
			return outcome{runtime: r}, err
		}
		if !st.started.IsZero() && now.Sub(st.started) >= s.params.RestartResetAfter {
			st.streak = 0
		}
		st.exited, st.failed, st.last = true, failed, rec
		st.due = now.Add(s.wait(st.streak))
	}
	if m.Spec.RunStrategy == api.RunRerunOnFailure && !st.failed {
		return outcome{runtime: r, clean: true}, nil
	}
	if now.Before(st.due) {
		return outcome{runtime: r}, nil
	}
	if r.GetState() == v1alpha2.MachineState_MACHINE_STATE_FAULTED {
		stopped, err := s.runtime.StopMachine(ctx, stopRequest(id, m))
		if err != nil {
			return outcome{runtime: r}, fmt.Errorf("stop %s: %w", id, err)
		}
		if r = stopped.GetMachine(); r.GetState() != v1alpha2.MachineState_MACHINE_STATE_STOPPED {
			return outcome{runtime: r}, fmt.Errorf("stop %s: mvmd holds it %s", id, r.GetState())
		}
	}
	o, err := s.startAgain(ctx, m, id, r)
	if err != nil || o.failure != nil {
		return o, err
	}
	st.exited = false
	st.streak++
	st.unwritten++
	return o, nil
}

// judge decides whether the exit of the MicroVM r failed: one the agent caused
// for a failing liveness probe did, whatever its record says, and any other is
// classified. Either way the record is read, so that the next exit is compared
// with it; one that cannot be read leaves the earlier record in place.
func (s *service) judge(ctx context.Context, id string, r *v1alpha2.Machine, st *run) (bool, *v1alpha2.ExecRecord, error) {
	failed, rec, err := s.classify(ctx, id, r, st.last)
	if st.liveness == "" || err != nil {
		return failed, rec, err
	}
	if rec == nil {
		rec = st.last
	}
	return true, rec, nil
}

// classify judges how the entrypoint of the MicroVM r ended. A faulted
// MicroVM, and one whose entrypoint has no EXITED record or exited with a
// non-zero code or signal, failed. A clean record equal to the one read at the
// previous exit of this MicroVM counts as failed too: mvmd does not say
// whether a console log is cleared at a start, so a clean record that may be
// the earlier run's is not proof of a clean exit.
func (s *service) classify(ctx context.Context, id string, r *v1alpha2.Machine, last *v1alpha2.ExecRecord) (bool, *v1alpha2.ExecRecord, error) {
	if r.GetState() == v1alpha2.MachineState_MACHINE_STATE_FAULTED {
		return true, nil, nil
	}
	read, cancel := context.WithTimeout(ctx, s.params.ReadTimeout)
	defer cancel()
	rec, err := v1alpha2.ReadExecOutput(read, s.runtime, id, entrypointExec, 0, func(*v1alpha2.OutputChunk) error { return nil })
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return false, nil, ctx.Err()
	case v1alpha2.KindOf(err) != v1alpha2.OutcomeKind_OUTCOME_KIND_UNSPECIFIED,
		errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return true, nil, nil
	default:
		return false, nil, fmt.Errorf("read the entrypoint of %s: %w", id, err)
	}
	if rec.GetStatus() != v1alpha2.ExecStatus_EXEC_STATUS_EXITED || rec.GetExitCode() != 0 || rec.GetTermSignal() != 0 {
		return true, rec, nil
	}
	return last != nil && proto.Equal(last, rec), rec, nil
}
