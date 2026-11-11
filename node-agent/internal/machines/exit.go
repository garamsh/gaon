package machines

import (
	"context"
	"fmt"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// ended handles a Machine that mvmd holds stopped under `Once`. It judges the
// exit once and reports it: a clean one as the outcome's clean, a failure as
// the outcome's exitFailure. The Machine is never started again.
func (s *service) ended(ctx context.Context, id string, r *v1alpha2.Machine) (outcome, error) {
	st := s.runOf(id)
	if !st.exited {
		failed, rec, err := s.classify(ctx, id, r, st.last)
		if err != nil {
			return outcome{runtime: r}, err
		}
		st.exited, st.failed, st.last = true, failed, rec
	}
	if st.failed {
		return outcome{runtime: r, exitFailure: exitFailure(st.last)}, nil
	}
	return outcome{runtime: r, clean: true}, nil
}

// exitFailure names how the entrypoint's record shows it failed.
func exitFailure(rec *v1alpha2.ExecRecord) string {
	switch {
	case rec == nil:
		return "the entrypoint has no exit record"
	case rec.GetStatus() != v1alpha2.ExecStatus_EXEC_STATUS_EXITED:
		return fmt.Sprintf("the entrypoint record says %s, not exited", rec.GetStatus())
	case rec.GetTermSignal() != 0:
		return fmt.Sprintf("the entrypoint was ended by signal %d", rec.GetTermSignal())
	case rec.GetExitCode() != 0:
		return fmt.Sprintf("the entrypoint exited with code %d", rec.GetExitCode())
	}
	return "the entrypoint record is the one read at the previous exit"
}
