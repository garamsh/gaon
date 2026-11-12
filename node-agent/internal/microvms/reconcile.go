package microvms

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// reconcileAll removes what is not wanted here, releases the MicroVMs being
// deleted that mvmd no longer holds, then brings each wanted MicroVM to its
// run strategy. The error joins what failed; one MicroVM's failure does not
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

// reconcile brings the MicroVM name to its run strategy, when it is wanted
// here, and reports its status.
func (s *service) reconcile(ctx context.Context, name string) error {
	m, ok := s.cache.microvms[name]
	if !ok || !s.cache.mine(m) {
		return nil
	}
	if m.Spec.RunStrategy == api.RunOnDemand {
		s.log.Debug("on-demand MicroVMs are not run", "microvm", name)
		return nil
	}
	if err := s.setFinalizer(ctx, name, true); err != nil {
		return fmt.Errorf("microvm %s: %w", name, err)
	}
	if m, ok = s.cache.microvms[name]; !ok || !s.cache.mine(m) {
		return nil
	}
	o, err := s.ensure(ctx, &m)
	if err != nil {
		if o.waitReason != "" {
			// A plugin that does not answer is visible in the MicroVM's
			// condition while the call is retried.
			if rerr := s.report(ctx, name, o); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return fmt.Errorf("microvm %s: %w", name, err)
	}
	return s.report(ctx, name, o)
}

// collect stops and deletes every MicroVM mvmd holds that the agent created
// and that is no longer wanted here: unbound, bound elsewhere, deleted, or
// bound at an earlier epoch. It returns the names of the MicroVMs mvmd still
// holds under an identifier the agent formed, which is nil when mvmd could not
// be listed.
func (s *service) collect(ctx context.Context) (map[string]bool, error) {
	listed, err := s.runtime.ListMachines(ctx, &v1alpha2.ListMachinesRequest{})
	if err != nil {
		return nil, fmt.Errorf("list runtime microvms: %w", err)
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
	if err := s.storage.Release(ctx, id); err != nil {
		return fmt.Errorf("release %s: %w", id, err)
	}
	s.log.Info("removed microvm", "id", id)
	return nil
}

func notFound(err error) bool {
	return v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND
}

// ensure creates the MicroVM in mvmd when it is missing and starts or stops
// it as its run strategy asks. A failure the spec or mvmd's answer settles is
// returned in the outcome, to be reported; any other is returned as an error,
// to be retried on the next round.
func (s *service) ensure(ctx context.Context, m *api.MicroVM) (outcome, error) {
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
		if st.halted {
			return s.resume(ctx, id, r)
		}
		return s.ended(ctx, id, r)
	}
	return outcome{runtime: r}, nil
}

// create asks the network for the policy, pulls the image, readies the
// volumes and creates the MicroVM in mvmd with both, then starts it. A
// plugin's refusal is the MicroVM's failure.
func (s *service) create(ctx context.Context, m *api.MicroVM) (outcome, error) {
	req, image, err := createRequest(m)
	if err != nil {
		return outcome{failure: err, failReason: ReasonInvalidSpec}, nil
	}
	policy, err := s.network.Policy(ctx, m)
	if err != nil {
		return refused("network", ReasonNetworkPluginUnavailable, err)
	}
	req.NetworkPolicy = policyToRuntime(policy)
	if _, err := s.runtime.PullImage(ctx, &v1alpha2.PullImageRequest{Image: image}); err != nil {
		return outcome{}, fmt.Errorf("pull %s: %w", image.GetDigest(), err)
	}
	disks, err := s.storage.Attach(ctx, m)
	if err != nil {
		return refused("storage", ReasonStoragePluginUnavailable, err)
	}
	req.Disks = disksToRuntime(disks)
	created, err := s.runtime.CreateMachine(ctx, req)
	switch {
	case err == nil:
	case v1alpha2.KindOf(err) == v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED:
		if err := s.storage.Release(ctx, req.GetMachineId()); err != nil {
			s.log.Warn("release after a rejected create", "id", req.GetMachineId(), "error", err)
		}
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

// refused makes the outcome of a plugin's error: a *Refusal is the MicroVM's
// failure. Any other error, such as a plugin that is not running, is returned
// to be retried, with an outcome that waits for the plugin under reason.
func refused(kind, reason string, err error) (outcome, error) {
	var r *Refusal
	if errors.As(err, &r) {
		return outcome{failure: r, failReason: r.Reason}, nil
	}
	return outcome{waitReason: reason, waitMessage: fmt.Sprintf("the %s plugin did not serve the call, which is retried: %v", kind, err)},
		fmt.Errorf("%s plugin: %w", kind, err)
}

// rejection is mvmd's refusal of a create, reported as the MicroVM's failure.
type rejection struct{ err error }

func (e rejection) Error() string { return "mvmd rejected the MicroVM: " + reasonOf(e.err) }
func (e rejection) Unwrap() error { return e.err }

func (s *service) start(ctx context.Context, id string) (*v1alpha2.Machine, error) {
	started, err := s.runtime.StartMachine(ctx, &v1alpha2.StartMachineRequest{MachineId: id})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", id, err)
	}
	s.runOf(id).started = s.clock.Now()
	return started.GetMachine(), nil
}

// report writes the status for the MicroVM name from o, when it differs from
// the stored one. A conflict re-reads the MicroVM and tries once more while
// the binding is the same.
func (s *service) report(ctx context.Context, name string, o outcome) error {
	for attempt := 0; attempt < 2; attempt++ {
		m, ok := s.cache.microvms[name]
		if !ok || !s.cache.mine(m) {
			return nil
		}
		st := s.runOf(machineID(name, m.Spec.BindingEpoch))
		pending := st.unwritten
		next := microvmStatus(&m, o, s.clock.Now(), pending)
		if reflect.DeepEqual(next, m.Status) {
			return nil
		}
		m.Status = next
		written, err := s.api.PutMicroVMStatus(ctx, &m)
		switch {
		case err == nil:
			s.cache.observe(*written)
			st.unwritten -= pending
			return nil
		case errors.Is(err, api.ErrConflict):
			fresh, getErr := s.api.GetMicroVM(ctx, name)
			if getErr != nil {
				return fmt.Errorf("read microvm %s: %w", name, getErr)
			}
			s.observe(api.EventModified, *fresh)
		case errors.Is(err, api.ErrInvalid):
			// The API server refuses a status of an epoch that is not the
			// spec's: the MicroVM was rebound meanwhile.
			s.log.Info("status refused", "microvm", name, "error", err)
			return nil
		default:
			return fmt.Errorf("put status of %s: %w", name, err)
		}
	}
	return nil
}
