package resourceapi

import (
	"context"
	"errors"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// PutMicroVM creates m with phase Pending, or changes the spec of the stored
// MicroVM m was read at. Neither takes the status or the binding from m. An
// update that clears the last finalizer of a MicroVM being deleted removes it.
func (s *service) PutMicroVM(ctx context.Context, name string, m *api.MicroVM) (*api.MicroVM, error) {
	if err := checkName(name, m.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, microvms, "", operationOf(m.Metadata.ResourceVersion), m); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.Metadata.ResourceVersion != "" {
		return s.updateMicroVM(ctx, m)
	}
	if m.Spec.NodeName != "" || m.Spec.BindingEpoch != 0 {
		return nil, api.NewAPIError(api.ReasonInvalid, "spec.nodeName and spec.bindingEpoch are set by the binding only")
	}
	m.Metadata.Generation, m.Metadata.DeletionTimestamp = 1, nil
	m.Status = api.MicroVMStatus{Phase: api.MicroVMPending}
	if err := validate(ctx, s, microvms, "", api.OperationCreate, m, nil); err != nil {
		return nil, err
	}
	return m, microvms.create(ctx, s.st, m)
}

func (s *service) updateMicroVM(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error) {
	old, rev, err := microvms.get(ctx, s.st, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(microvms.plural, m.Metadata.Name, m.Metadata.ResourceVersion, old.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := m.ValidateUpdate(old); err != nil {
		return nil, err
	}
	deleting := old.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(m.Metadata.Finalizers, func(f string) bool { return !slices.Contains(old.Metadata.Finalizers, f) }) {
		return nil, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to a microvm being deleted")
	}
	if deleting && len(m.Metadata.Finalizers) == 0 {
		if err := validate(ctx, s, microvms, "", api.OperationUpdate, m, old); err != nil {
			return nil, err
		}
		old.Metadata.Finalizers = nil
		return old, microvms.remove(ctx, s.st, old, rev)
	}
	m.Status = old.Status
	m.Metadata.DeletionTimestamp = old.Metadata.DeletionTimestamp
	m.Metadata.Generation = nextGeneration(old.Metadata.Generation, old.Spec, m.Spec)
	if err := validate(ctx, s, microvms, "", api.OperationUpdate, m, old); err != nil {
		return nil, err
	}
	return m, microvms.update(ctx, s.st, m, rev)
}

// BindMicroVM sets the node and the epoch of the MicroVM if b carries its
// current version and the epoch after its current one. It pins each Volume
// the MicroVM uses to the node in the same transaction, and writes nothing if
// the MicroVM or any Volume it uses changed since they were read.
func (s *service) BindMicroVM(ctx context.Context, name string, b api.Binding) (*api.MicroVM, error) {
	if err := s.mutateBinding(ctx, name, &b); err != nil {
		return nil, err
	}
	m, rev, err := microvms.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(microvms.plural, name, b.ResourceVersion, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := m.Bind(b); err != nil {
		return nil, err
	}
	m.Metadata.Generation++
	used, err := s.pinVolumes(ctx, m, b.NodeName)
	if err != nil {
		return nil, err
	}
	if err := s.validateBinding(ctx, name, b); err != nil {
		return nil, err
	}
	for _, u := range used {
		if u.pinned == nil {
			continue
		}
		if err := validate(ctx, s, volumes, "", api.OperationUpdate, u.pinned, u.stored); err != nil {
			return nil, err
		}
	}
	return m, s.commitBinding(ctx, m, rev, used)
}

// usedVolume is a Volume a MicroVM being bound uses, as stored and at the
// revision it was read at. pinned is the Volume the binding writes, or nil
// where the binding changes nothing of it.
type usedVolume struct {
	stored   *api.Volume
	revision int64
	pinned   *api.Volume
}

// pinVolumes reads the Volumes m uses and pins them to node, sending each
// change to the mutating webhooks. A webhook may not change what the pin set.
func (s *service) pinVolumes(ctx context.Context, m *api.MicroVM, node string) ([]*usedVolume, error) {
	var used []*usedVolume
	byName := map[string]*usedVolume{}
	pinning := map[string]*api.Volume{}
	for _, mv := range m.Spec.Volumes {
		if byName[mv.Name] != nil {
			continue
		}
		v, vrev, err := volumes.get(ctx, s.st, mv.Name)
		if errors.Is(err, api.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		u := &usedVolume{stored: v, revision: vrev}
		byName[mv.Name] = u
		used = append(used, u)
		c := *v
		c.Metadata.Finalizers = slices.Clone(v.Metadata.Finalizers)
		pinning[mv.Name] = &c
	}
	changed, err := m.PinVolumes(node, pinning)
	if err != nil {
		return nil, err
	}
	for _, v := range changed {
		u := byName[v.Metadata.Name]
		pinned := *v
		if err := mutateAgainst(ctx, s, volumes, "", api.OperationUpdate, v, u.stored); err != nil {
			return nil, err
		}
		if !sameJSON(v.Spec, pinned.Spec) || !slices.Equal(v.Metadata.Finalizers, pinned.Metadata.Finalizers) {
			return nil, api.NewAPIError(api.ReasonInvalid, "a webhook changed the spec or the finalizers the binding set on volume %q", v.Metadata.Name)
		}
		v.Status, v.Metadata.DeletionTimestamp = u.stored.Status, u.stored.Metadata.DeletionTimestamp
		v.Metadata.Generation = nextGeneration(u.stored.Metadata.Generation, u.stored.Spec, v.Spec)
		u.pinned = v
	}
	return used, nil
}

// commitBinding writes m and the pinned Volumes in one transaction. Each Volume
// m uses holds its place in it, so that a change to one of them since it was
// read refuses the binding.
func (s *service) commitBinding(ctx context.Context, m *api.MicroVM, rev int64, used []*usedVolume) error {
	value, err := microvms.encode(m)
	if err != nil {
		return err
	}
	writes := []Write{{Key: microvms.key(m.Metadata.Name), Value: value, Revision: rev}}
	for _, u := range used {
		w := Write{Key: volumes.key(u.stored.Metadata.Name), Revision: u.revision}
		if u.pinned != nil {
			if w.Value, err = volumes.encode(u.pinned); err != nil {
				return err
			}
		}
		writes = append(writes, w)
	}
	got, err := s.st.Transact(ctx, writes)
	if errors.Is(err, ErrStale) {
		return api.NewAPIError(api.ReasonConflict, "microvm %q or a volume it uses was changed since it was read", m.Metadata.Name)
	}
	if err != nil {
		return err
	}
	m.Metadata.ResourceVersion = version(got)
	for _, u := range used {
		if u.pinned != nil {
			u.pinned.Metadata.ResourceVersion = version(got)
		}
	}
	return nil
}

// PutMicroVMStatus replaces the status of the stored MicroVM m was read at,
// if m's status carries the epoch of the stored spec.
func (s *service) PutMicroVMStatus(ctx context.Context, name string, next *api.MicroVM) (*api.MicroVM, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, microvms, "status", api.OperationUpdate, next); err != nil {
		return nil, err
	}
	m, rev, err := microvms.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(microvms.plural, name, next.Metadata.ResourceVersion, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatusUpdate(m); err != nil {
		return nil, err
	}
	old := *m
	m.Status = next.Status
	if err := validate(ctx, s, microvms, "status", api.OperationUpdate, m, &old); err != nil {
		return nil, err
	}
	return m, microvms.update(ctx, s.st, m, rev)
}

// DeleteMicroVM sets the deletion time of the MicroVM if rv is its current
// version, and removes it when it holds no finalizer. A MicroVM already being
// deleted is returned as it is.
func (s *service) DeleteMicroVM(ctx context.Context, name string, rv api.ResourceVersion) (*api.MicroVM, error) {
	m, rev, err := microvms.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(microvms.plural, name, rv, m.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if m.Metadata.DeletionTimestamp != nil {
		return m, nil
	}
	if err := admitDelete(ctx, s, microvms, m); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	m.Metadata.DeletionTimestamp = &now
	if len(m.Metadata.Finalizers) > 0 {
		return m, microvms.update(ctx, s.st, m, rev)
	}
	return m, microvms.remove(ctx, s.st, m, rev)
}
