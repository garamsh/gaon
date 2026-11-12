package resourceapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// PutCustomResourceDefinition creates d, or changes the schema and the
// subresources of the stored definition d was read at. The API server writes
// the status: the conditions NamesAccepted and Established.
func (s *service) PutCustomResourceDefinition(ctx context.Context, name string, d *api.CustomResourceDefinition) (*api.CustomResourceDefinition, error) {
	if err := checkName(name, d.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, customResourceDefinitions, "", operationOf(d.Metadata.ResourceVersion), d); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if err := d.ValidateStatus(); err != nil {
		return nil, err
	}
	onUpdate := func(old *api.CustomResourceDefinition) error {
		if err := d.ValidateUpdate(old); err != nil {
			return err
		}
		d.Status = old.Status
		return nil
	}
	if d.Metadata.ResourceVersion != "" {
		return admittedPut(ctx, s, customResourceDefinitions, d, nil, onUpdate)
	}
	// The conditions of a new definition come from the definitions stored when
	// they are read, so the create is fenced on the key every create of a
	// definition writes: of two creates that read the same set, one stores its
	// definition and the other reads again and sees it, whichever API server
	// process it reached.
	for range fenceAttempts {
		lockRev, err := s.namesLockRevision(ctx)
		if err != nil {
			return nil, err
		}
		fenced := *s
		fenced.st = fencedStore{Store: s.st, fence: []Write{{Key: definitionNamesLock, Value: []byte("{}"), Revision: lockRev}}}
		out, err := admittedPut(ctx, &fenced, customResourceDefinitions, d, func() error {
			conditions, err := s.creationConditions(ctx, d)
			d.Status.Conditions = conditions
			return err
		}, onUpdate)
		if !errors.Is(err, errFenceMoved) {
			return out, err
		}
	}
	return nil, api.NewAPIError(api.ReasonConflict, "customresourcedefinitions %q: other definitions were created meanwhile; retry", name)
}

// definitionNamesLock is the key every create of a definition writes. It lies
// outside the prefix of the definitions, so that no list or watch of them reads
// it.
const definitionNamesLock = "/locks/customresourcedefinition-names"

// namesLockRevision returns the revision of definitionNamesLock, which is 0
// while no definition was ever created.
func (s *service) namesLockRevision(ctx context.Context) (int64, error) {
	e, err := s.st.Get(ctx, definitionNamesLock)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	return e.Revision, err
}

// creationConditions are the conditions of a definition at its creation.
// NamesAccepted is false where an older definition of the same group, one that
// holds its names, has any of d's names; the older keeps them.
func (s *service) creationConditions(ctx context.Context, d *api.CustomResourceDefinition) ([]api.Condition, error) {
	now := s.now().UTC()
	others, _, err := customResourceDefinitions.list(ctx, s.st, nil)
	if err != nil {
		return nil, err
	}
	accepted := api.Condition{Type: api.CRDNamesAccepted, Status: api.ConditionTrue, Reason: "NoConflicts", Message: "no other definition of the group has these names", LastTransitionTime: &now}
	established := api.Condition{Type: api.CRDEstablished, Status: api.ConditionTrue, Reason: "Served", Message: "the paths of the kind are served", LastTransitionTime: &now}
	for _, o := range others {
		if o.Spec.Group != d.Spec.Group || !namesAccepted(&o) {
			continue
		}
		if field := sharedName(o.Spec.Names, d.Spec.Names); field != "" {
			accepted.Status, accepted.Reason = api.ConditionFalse, "NameConflict"
			accepted.Message = fmt.Sprintf("names.%s is held by %s", field, o.Metadata.Name)
			established.Status, established.Reason, established.Message = api.ConditionFalse, "NamesNotAccepted", "the names are not accepted, so no path is served"
			break
		}
	}
	return []api.Condition{accepted, established}, nil
}

// sharedName returns the first field of a and b that holds the same name, or
// the empty string.
func sharedName(a, b api.CustomResourceNames) string {
	switch {
	case a.Kind == b.Kind:
		return "kind"
	case a.ListKind == b.ListKind:
		return "listKind"
	case a.Plural == b.Plural:
		return "plural"
	case a.Singular == b.Singular:
		return "singular"
	}
	return ""
}

func namesAccepted(d *api.CustomResourceDefinition) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == api.CRDNamesAccepted {
			return c.Status == api.ConditionTrue
		}
	}
	return false
}

// DeleteCustomResourceDefinition sets the deletion time of the definition if rv
// is its current version, and sets Established false. It then deletes each
// object of the kind as a user's delete would, and removes the definition once
// none is left; an object held by a finalizer keeps the definition until the
// finalizer is removed. A definition already being deleted is carried on the
// same way whatever rv says, so a delete that was cut short can be repeated.
func (s *service) DeleteCustomResourceDefinition(ctx context.Context, name string, rv api.ResourceVersion) (*api.CustomResourceDefinition, error) {
	d, err := s.markDefinitionDeleted(ctx, name, rv)
	if err != nil {
		return nil, err
	}
	if err := s.deleteObjectsOf(ctx, d); err != nil {
		return nil, err
	}
	return s.finishDefinition(ctx, d)
}

func (s *service) markDefinitionDeleted(ctx context.Context, name string, rv api.ResourceVersion) (*api.CustomResourceDefinition, error) {
	// The update below is conditioned on the revision read, so a write made
	// meanwhile, through this process or another, makes it read again: the
	// definition is then either already being deleted, which is carried on, or
	// at another version, which the check of rv refuses.
	for {
		d, rev, err := customResourceDefinitions.get(ctx, s.st, name)
		if err != nil {
			return nil, err
		}
		if d.Metadata.DeletionTimestamp != nil {
			return d, nil
		}
		if err := checkVersion(customResourceDefinitions.plural, name, rv, d.Metadata.ResourceVersion); err != nil {
			return nil, err
		}
		if err := admitDelete(ctx, s, customResourceDefinitions, d); err != nil {
			return nil, err
		}
		now := s.now().UTC()
		d.Metadata.DeletionTimestamp = &now
		for i, c := range d.Status.Conditions {
			if c.Type == api.CRDEstablished {
				d.Status.Conditions[i] = api.Condition{Type: api.CRDEstablished, Status: api.ConditionFalse, Reason: "Terminating", Message: "the definition is being deleted", LastTransitionTime: &now}
			}
		}
		err = customResourceDefinitions.update(ctx, s.st, d, rev)
		if isReason(err, api.ReasonConflict) {
			continue
		}
		return d, err
	}
}

// deleteObjectsOf deletes each object of the definition's kind. An object that
// changes between the read and the delete is read again.
func (s *service) deleteObjectsOf(ctx context.Context, d *api.CustomResourceDefinition) error {
	k := customKind(d)
	items, _, err := k.list(ctx, s.st, nil)
	if err != nil {
		return err
	}
	p := pathOf(d)
	for _, item := range items {
		name := item.Metadata.Name
		for {
			o, _, err := k.get(ctx, s.st, name)
			if err == nil {
				_, err = s.DeleteCustomObject(ctx, p, name, o.Metadata.ResourceVersion)
			}
			if isReason(err, api.ReasonConflict) {
				continue
			}
			// NotFound: a concurrent delete removed the object or the definition.
			if err != nil && !isReason(err, api.ReasonNotFound) {
				return err
			}
			break
		}
	}
	return nil
}

// finishDefinition removes a definition that is being deleted and holds no
// object, and ends the watches of its objects. It returns the definition as it
// stands.
func (s *service) finishDefinition(ctx context.Context, d *api.CustomResourceDefinition) (*api.CustomResourceDefinition, error) {
	// No object is created once the definition is being deleted: a create is
	// fenced on the definition's revision, and the deletion time was written
	// before any object was listed here. So an empty list stays empty, and the
	// removal, conditioned on the revision read, ends the definition. Another
	// process may finish first, which is not a failure.
	for {
		cur, rev, err := customResourceDefinitions.get(ctx, s.st, d.Metadata.Name)
		if isReason(err, api.ReasonNotFound) {
			return d, nil
		}
		if err != nil {
			return nil, err
		}
		if cur.Metadata.DeletionTimestamp == nil {
			return cur, nil
		}
		k := customKind(cur)
		items, _, err := k.list(ctx, s.st, nil)
		if err != nil || len(items) > 0 {
			return cur, err
		}
		if err := s.closeWatches(ctx, k); err != nil {
			return nil, err
		}
		err = customResourceDefinitions.remove(ctx, s.st, cur, rev)
		if isReason(err, api.ReasonConflict) || isReason(err, api.ReasonNotFound) {
			continue
		}
		return cur, err
	}
}

// closeWatches writes and removes a key beside the objects of k, which ends
// every watch of them (kind.own). Another process may be doing the same: a key
// it wrote already ended the watches, so a key that exists or is gone is done.
func (s *service) closeWatches(ctx context.Context, k kind[api.CustomObject]) error {
	key := k.prefix() + "removed/"
	rev, err := s.st.Create(ctx, key, []byte("{}"))
	if errors.Is(err, ErrExists) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = s.st.Delete(ctx, key, rev); errors.Is(err, ErrNotFound) || errors.Is(err, ErrStale) {
		return nil
	}
	return err
}

func isReason(err error, reason api.Reason) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.Reason == reason
}

func (s *service) GetCustomResourceDefinition(ctx context.Context, name string) (*api.CustomResourceDefinition, error) {
	d, _, err := customResourceDefinitions.get(ctx, s.st, name)
	return d, err
}

func (s *service) ListCustomResourceDefinitions(ctx context.Context, sel api.Selector) (*api.CustomResourceDefinitionList, error) {
	items, rv, err := customResourceDefinitions.list(ctx, s.st, sel)
	if err != nil {
		return nil, err
	}
	return &api.CustomResourceDefinitionList{TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinitionList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCustomResourceDefinitions(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
	return customResourceDefinitions.watch(ctx, s.st, rv, sel)
}
