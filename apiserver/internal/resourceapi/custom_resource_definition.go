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
	s.defs.Lock()
	defer s.defs.Unlock()
	return admittedPut(ctx, s, customResourceDefinitions, d,
		func() error {
			conditions, err := s.creationConditions(ctx, d)
			d.Status.Conditions = conditions
			return err
		},
		func(old *api.CustomResourceDefinition) error {
			if err := d.ValidateUpdate(old); err != nil {
				return err
			}
			d.Status = old.Status
			return nil
		})
}

// creationConditions are the conditions of a definition at its creation.
// NamesAccepted is false where an older definition of the same group, one that
// holds its names, has any of d's names; the older keeps them.
func (s *service) creationConditions(ctx context.Context, d *api.CustomResourceDefinition) ([]api.Condition, error) {
	now := s.now().UTC()
	others, _, err := customResourceDefinitions.list(ctx, s.st)
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
	s.defs.Lock()
	defer s.defs.Unlock()
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
	return d, customResourceDefinitions.update(ctx, s.st, d, rev)
}

// deleteObjectsOf deletes each object of the definition's kind. An object that
// changes between the read and the delete is read again.
func (s *service) deleteObjectsOf(ctx context.Context, d *api.CustomResourceDefinition) error {
	k := customKind(d)
	items, _, err := k.list(ctx, s.st)
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
	s.defs.Lock()
	defer s.defs.Unlock()
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
	items, _, err := k.list(ctx, s.st)
	if err != nil || len(items) > 0 {
		return cur, err
	}
	if err := s.closeWatches(ctx, k); err != nil {
		return nil, err
	}
	return cur, customResourceDefinitions.remove(ctx, s.st, cur, rev)
}

// closeWatches writes and removes a key beside the objects of k, which ends
// every watch of them (kind.own).
func (s *service) closeWatches(ctx context.Context, k kind[api.CustomObject]) error {
	key := k.prefix() + "removed/"
	rev, err := s.st.Create(ctx, key, []byte("{}"))
	if err != nil {
		return err
	}
	_, err = s.st.Delete(ctx, key, rev)
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

func (s *service) ListCustomResourceDefinitions(ctx context.Context) (*api.CustomResourceDefinitionList, error) {
	items, rv, err := customResourceDefinitions.list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.CustomResourceDefinitionList{TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinitionList}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCustomResourceDefinitions(ctx context.Context, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	return customResourceDefinitions.watch(ctx, s.st, rv)
}
