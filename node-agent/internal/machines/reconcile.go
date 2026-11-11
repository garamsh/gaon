package machines

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// reconcileAll removes what is not wanted here, releases the Machines being
// deleted that mvmd no longer holds, then brings each wanted Machine to its
// run strategy. The error joins what failed; one Machine's failure does not
// stop the others.
func (s *service) reconcileAll(ctx context.Context) error {
	held, err := s.collect(ctx)
	errs := []error{err}
	s.prune(s.cache.wanted())
	if held != nil {
		errs = append(errs, s.release(ctx, held))
	}
	for _, name := range s.cache.names() {
		errs = append(errs, s.reconcile(ctx, name))
	}
	return errors.Join(errs...)
}

// reconcile brings the Machine name to its run strategy, when it is wanted
// here, and reports its status.
func (s *service) reconcile(ctx context.Context, name string) error {
	m, ok := s.cache.machines[name]
	if !ok || !s.cache.mine(m) {
		return nil
	}
	if m.Spec.RunStrategy == api.RunOnDemand {
		s.log.Debug("on-demand Machines are not run", "machine", name)
		return nil
	}
	if err := s.setFinalizer(ctx, name, true); err != nil {
		return fmt.Errorf("machine %s: %w", name, err)
	}
	if m, ok = s.cache.machines[name]; !ok || !s.cache.mine(m) {
		return nil
	}
	o, err := s.ensure(ctx, &m)
	if err != nil {
		return fmt.Errorf("machine %s: %w", name, err)
	}
	return s.report(ctx, name, o)
}

// collect stops and deletes every Machine mvmd holds that the agent created
// and that is no longer wanted here: unbound, bound elsewhere, deleted, or
// bound at an earlier epoch. It returns the names of the Machines mvmd still
// holds under an identifier the agent formed, which is nil when mvmd could not
// be listed.
func (s *service) collect(ctx context.Context) (map[string]bool, error) {
	listed, err := s.runtime.ListMachines(ctx, &v1alpha2.ListMachinesRequest{})
	if err != nil {
		return nil, fmt.Errorf("list runtime machines: %w", err)
	}
	wanted := s.cache.wanted()
	held := map[string]bool{}
	var errs []error
	for _, r := range listed.GetMachines() {
		name, _, ours := parseID(r.GetId())
		if !ours {
			continue
		}
		if wanted[r.GetId()] {
			held[name] = true
			continue
		}
		if err := s.remove(ctx, r.GetId()); err != nil {
			held[name] = true
			errs = append(errs, err)
		}
	}
	return held, errors.Join(errs...)
}

// remove stops the mvmd machine id and deletes it. Both calls are repeatable.
func (s *service) remove(ctx context.Context, id string) error {
	if _, err := s.runtime.StopMachine(ctx, &v1alpha2.StopMachineRequest{MachineId: id}); err != nil && !notFound(err) {
		return fmt.Errorf("stop %s: %w", id, err)
	}
	if _, err := s.runtime.DeleteMachine(ctx, &v1alpha2.DeleteMachineRequest{MachineId: id}); err != nil && !notFound(err) {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	s.log.Info("removed machine", "id", id)
	return nil
}

func notFound(err error) bool {
	return v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND
}

// ensure creates the Machine in mvmd when it is missing and starts or stops
// it as its run strategy asks. A failure the spec or mvmd's answer settles is
// returned in the outcome, to be reported; any other is returned as an error,
// to be retried on the next round.
func (s *service) ensure(ctx context.Context, m *api.Machine) (outcome, error) {
	id := machineID(m.Metadata.Name, m.Spec.BindingEpoch)
	got, err := s.runtime.InspectMachine(ctx, &v1alpha2.InspectMachineRequest{MachineId: id})
	switch {
	case err == nil:
	case notFound(err):
		if m.Spec.RunStrategy == api.RunHalted {
			return outcome{}, nil
		}
		return s.create(ctx, m)
	default:
		return outcome{}, fmt.Errorf("inspect %s: %w", id, err)
	}
	r := got.GetMachine()
	st := s.runOf(id)
	switch {
	case m.Spec.RunStrategy == api.RunHalted:
		if r.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING || r.GetState() == v1alpha2.MachineState_MACHINE_STATE_PAUSED {
			stopped, err := s.runtime.StopMachine(ctx, &v1alpha2.StopMachineRequest{MachineId: id})
			if err != nil {
				return outcome{}, fmt.Errorf("stop %s: %w", id, err)
			}
			r = stopped.GetMachine()
		}
		st.halted = r.GetState() == v1alpha2.MachineState_MACHINE_STATE_STOPPED
	case r.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING:
		if st.started.IsZero() {
			st.started = s.clock.Now()
		}
	case r.GetState() == v1alpha2.MachineState_MACHINE_STATE_CREATED:
		r, err = s.start(ctx, id)
		if err != nil {
			return outcome{runtime: got.GetMachine()}, err
		}
	case restarts(m.Spec.RunStrategy) && (r.GetState() == v1alpha2.MachineState_MACHINE_STATE_STOPPED || r.GetState() == v1alpha2.MachineState_MACHINE_STATE_FAULTED):
		return s.restart(ctx, m, id, r)
	case m.Spec.RunStrategy == api.RunOnce && r.GetState() == v1alpha2.MachineState_MACHINE_STATE_STOPPED:
		return s.ended(ctx, id, r)
	}
	return outcome{runtime: r}, nil
}

// create pulls the image and creates the Machine in mvmd, then starts it.
func (s *service) create(ctx context.Context, m *api.Machine) (outcome, error) {
	req, image, err := createRequest(m)
	if err != nil {
		return outcome{failure: err, failReason: ReasonInvalidSpec}, nil
	}
	if _, err := s.runtime.PullImage(ctx, &v1alpha2.PullImageRequest{Image: image}); err != nil {
		return outcome{}, fmt.Errorf("pull %s: %w", image.GetDigest(), err)
	}
	created, err := s.runtime.CreateMachine(ctx, req)
	switch {
	case err == nil:
	case v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED:
		return outcome{failure: rejection{err}, failReason: ReasonRejected}, nil
	default:
		return outcome{}, fmt.Errorf("create %s: %w", req.GetMachineId(), err)
	}
	started, err := s.start(ctx, req.GetMachineId())
	if err != nil {
		return outcome{runtime: created.GetMachine()}, err
	}
	return outcome{runtime: started}, nil
}

// rejection is mvmd's refusal of a create, reported as the Machine's failure.
type rejection struct{ err error }

func (e rejection) Error() string { return "mvmd rejected the Machine: " + reasonOf(e.err) }
func (e rejection) Unwrap() error { return e.err }

func (s *service) start(ctx context.Context, id string) (*v1alpha2.Machine, error) {
	started, err := s.runtime.StartMachine(ctx, &v1alpha2.StartMachineRequest{MachineId: id})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", id, err)
	}
	s.runOf(id).started = s.clock.Now()
	return started.GetMachine(), nil
}

// report writes the status for the Machine name from o, when it differs from
// the stored one. A conflict re-reads the Machine and tries once more while
// the binding is the same.
func (s *service) report(ctx context.Context, name string, o outcome) error {
	for attempt := 0; attempt < 2; attempt++ {
		m, ok := s.cache.machines[name]
		if !ok || !s.cache.mine(m) {
			return nil
		}
		st := s.runOf(machineID(name, m.Spec.BindingEpoch))
		pending := st.unwritten
		next := machineStatus(&m, o, s.clock.Now(), pending)
		if reflect.DeepEqual(next, m.Status) {
			return nil
		}
		m.Status = next
		written, err := s.api.PutMachineStatus(ctx, &m)
		switch {
		case err == nil:
			s.cache.observe(*written)
			st.unwritten -= pending
			return nil
		case errors.Is(err, api.ErrConflict):
			fresh, getErr := s.api.GetMachine(ctx, name)
			if getErr != nil {
				return fmt.Errorf("read machine %s: %w", name, getErr)
			}
			s.observe(api.EventModified, *fresh)
		case errors.Is(err, api.ErrInvalid):
			// The API server refuses a status of an epoch that is not the
			// spec's: the Machine was rebound meanwhile.
			s.log.Info("status refused", "machine", name, "error", err)
			return nil
		default:
			return fmt.Errorf("put status of %s: %w", name, err)
		}
	}
	return nil
}
