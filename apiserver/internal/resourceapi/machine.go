package resourceapi

import (
	"context"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// PutMachine creates m with phase Pending, or changes the spec of the stored
// Machine m was read at. Neither takes the status or the binding from m. An
// update that clears the last finalizer of a Machine being deleted removes it.
func (s *service) PutMachine(ctx context.Context, name string, m *api.Machine) (*api.Machine, error) {
	if err := checkName(name, m.Metadata.Name); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.Metadata.ResourceVersion != "" {
		return s.updateMachine(ctx, m)
	}
	if m.Spec.NodeName != "" || m.Spec.BindingEpoch != 0 {
		return nil, api.NewAPIError(api.ReasonInvalid, "spec.nodeName and spec.bindingEpoch are set by the binding only")
	}
	m.Metadata.Generation, m.Metadata.DeletionTimestamp = 1, nil
	m.Status = api.MachineStatus{Phase: api.MachinePending}
	return m, machines.create(ctx, s.st, m)
}

func (s *service) updateMachine(ctx context.Context, m *api.Machine) (*api.Machine, error) {
	old, rev, err := machines.get(ctx, s.st, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(machines.plural, m.Metadata.Name, m.Metadata.ResourceVersion, old.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := m.ValidateUpdate(old); err != nil {
		return nil, err
	}
	deleting := old.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(m.Metadata.Finalizers, func(f string) bool { return !slices.Contains(old.Metadata.Finalizers, f) }) {
		return nil, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a machine being deleted")
	}
	if deleting && len(m.Metadata.Finalizers) == 0 {
		old.Metadata.Finalizers = nil
		return old, machines.remove(ctx, s.st, old, rev)
	}
	m.Status = old.Status
	m.Metadata.DeletionTimestamp = old.Metadata.DeletionTimestamp
	m.Metadata.Generation = nextGeneration(old.Metadata.Generation, old.Spec, m.Spec)
	return m, machines.update(ctx, s.st, m, rev)
}

// BindMachine sets the node and the epoch of the Machine if b carries its
// current version and the epoch after its current one.
func (s *service) BindMachine(ctx context.Context, name string, b api.Binding) (*api.Machine, error) {
	m, rev, err := machines.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(machines.plural, name, b.ResourceVersion, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := m.Bind(b); err != nil {
		return nil, err
	}
	m.Metadata.Generation++
	return m, machines.update(ctx, s.st, m, rev)
}

// PutMachineStatus replaces the status of the stored Machine m was read at,
// if m's status carries the epoch of the stored spec.
func (s *service) PutMachineStatus(ctx context.Context, name string, next *api.Machine) (*api.Machine, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	m, rev, err := machines.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(machines.plural, name, next.Metadata.ResourceVersion, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatusUpdate(m); err != nil {
		return nil, err
	}
	m.Status = next.Status
	return m, machines.update(ctx, s.st, m, rev)
}

// DeleteMachine sets the deletion time of the Machine if rv is its current
// version, and removes it when it holds no finalizer. A Machine already being
// deleted is returned as it is.
func (s *service) DeleteMachine(ctx context.Context, name string, rv api.ResourceVersion) (*api.Machine, error) {
	m, rev, err := machines.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(machines.plural, name, rv, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if m.Metadata.DeletionTimestamp != nil {
		return m, nil
	}
	now := s.now().UTC()
	m.Metadata.DeletionTimestamp = &now
	if len(m.Metadata.Finalizers) > 0 {
		return m, machines.update(ctx, s.st, m, rev)
	}
	return m, machines.remove(ctx, s.st, m, rev)
}
