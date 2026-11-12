package resourceapi

import (
	"context"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

// CustomPath names the kind a request addresses: the group and the version
// of the root and the plural of the path.
type CustomPath struct {
	Group, Version, Plural string
}

// customKind is the kind of the objects a definition registers. It is stored
// under the definition's name, which holds a dot where no core plural does.
func customKind(d *api.CustomResourceDefinition) kind[api.CustomObject] {
	return kind[api.CustomObject]{d.Metadata.Name, func(o *api.CustomObject) *api.ObjectMeta { return &o.Metadata }, func(o *api.CustomObject) any { return o.Spec },
		api.GroupVersionKind{Group: d.Spec.Group, Version: d.Spec.Versions[0].Name, Kind: d.Spec.Names.Kind}}
}

func pathOf(d *api.CustomResourceDefinition) CustomPath {
	return CustomPath{Group: d.Spec.Group, Version: d.Spec.Versions[0].Name, Plural: d.Spec.Names.Plural}
}

func typeOf(d *api.CustomResourceDefinition) api.CustomResourceType {
	return api.CustomResourceType{Group: d.Spec.Group, Version: d.Spec.Versions[0].Name, Kind: d.Spec.Names.Kind, Plural: d.Spec.Names.Plural}
}

func hasStatusSubresource(d *api.CustomResourceDefinition) bool {
	sub := d.Spec.Versions[0].Subresources
	return sub != nil && sub.Status != nil
}

// servedDefinition returns the definition that serves p. A path is served
// from the moment the definition holds its names until the definition is
// removed, so the objects of a definition being deleted stay reachable.
func (s *service) servedDefinition(ctx context.Context, p CustomPath) (*api.CustomResourceDefinition, error) {
	d, _, err := customResourceDefinitions.get(ctx, s.st, p.Plural+"."+p.Group)
	if err == nil && d.Spec.Versions[0].Name == p.Version && namesAccepted(d) {
		return d, nil
	}
	if err != nil && !isReason(err, api.ReasonNotFound) {
		return nil, err
	}
	return nil, api.NewAPIError(api.ReasonNotFound, "no resource %q is served under %s/%s", p.Plural, p.Group, p.Version)
}

// PutCustomObject creates o, or changes the stored object o was read at. The
// schema prunes and validates what o holds, and with the status subresource
// enabled o's status is not taken. An update that clears the last finalizer of
// an object being deleted removes it.
func (s *service) PutCustomObject(ctx context.Context, p CustomPath, name string, o *api.CustomObject) (*api.CustomObject, error) {
	d, out, removed, err := s.putCustomObject(ctx, p, name, o)
	if err == nil && removed {
		_, err = s.finishDefinition(ctx, d)
	}
	return out, err
}

func (s *service) putCustomObject(ctx context.Context, p CustomPath, name string, o *api.CustomObject) (d *api.CustomResourceDefinition, out *api.CustomObject, removed bool, err error) {
	s.defs.RLock()
	defer s.defs.RUnlock()
	d, err = s.servedDefinition(ctx, p)
	if err != nil {
		return nil, nil, false, err
	}
	if err := checkName(name, o.Metadata.Name); err != nil {
		return nil, nil, false, err
	}
	k := customKind(d)
	if err := mutate(ctx, s, k, "", operationOf(o.Metadata.ResourceVersion), o); err != nil {
		return nil, nil, false, err
	}
	if err := o.Validate(typeOf(d)); err != nil {
		return nil, nil, false, err
	}
	root := d.Spec.Versions[0].Schema.OpenAPIV3Schema
	statusInBody := !hasStatusSubresource(d)
	if o.Spec, err = fitSection(root, "spec", o.Spec); err != nil {
		return nil, nil, false, err
	}
	if statusInBody {
		if o.Status, err = fitSection(root, "status", o.Status); err != nil {
			return nil, nil, false, err
		}
	}
	if o.Metadata.ResourceVersion == "" {
		if d.Metadata.DeletionTimestamp != nil {
			return nil, nil, false, api.NewAPIError(api.ReasonConflict, "the definition %q is being deleted", d.Metadata.Name)
		}
		o.Metadata.Generation, o.Metadata.DeletionTimestamp = 1, nil
		if !statusInBody {
			o.Status = nil
		}
		if err := validate(ctx, s, k, "", api.OperationCreate, o, nil); err != nil {
			return nil, nil, false, err
		}
		return d, o, false, k.create(ctx, s.st, o)
	}
	old, rev, err := k.get(ctx, s.st, name)
	if err != nil {
		return nil, nil, false, err
	}
	if err := checkVersion(k.plural, name, o.Metadata.ResourceVersion, old.Metadata.ResourceVersion); err != nil {
		return nil, nil, false, err
	}
	deleting := old.Metadata.DeletionTimestamp != nil
	if deleting && slices.ContainsFunc(o.Metadata.Finalizers, func(f string) bool { return !slices.Contains(old.Metadata.Finalizers, f) }) {
		return nil, nil, false, api.NewAPIError(api.ReasonInvalid, "a finalizer cannot be added to an object being deleted")
	}
	if deleting && len(o.Metadata.Finalizers) == 0 {
		if err := validate(ctx, s, k, "", api.OperationUpdate, o, old); err != nil {
			return nil, nil, false, err
		}
		old.Metadata.Finalizers = nil
		return d, old, true, k.remove(ctx, s.st, old, rev)
	}
	if !statusInBody {
		o.Status = old.Status
	}
	o.Metadata.DeletionTimestamp = old.Metadata.DeletionTimestamp
	o.Metadata.Generation = nextGeneration(old.Metadata.Generation, old.Spec, o.Spec)
	if err := validate(ctx, s, k, "", api.OperationUpdate, o, old); err != nil {
		return nil, nil, false, err
	}
	return d, o, false, k.update(ctx, s.st, o, rev)
}

// PutCustomObjectStatus replaces the status of the stored object o was read
// at, after the schema has pruned and validated it. The path exists only where
// the definition enables the status subresource.
func (s *service) PutCustomObjectStatus(ctx context.Context, p CustomPath, name string, next *api.CustomObject) (*api.CustomObject, error) {
	s.defs.RLock()
	defer s.defs.RUnlock()
	d, err := s.servedDefinition(ctx, p)
	if err != nil {
		return nil, err
	}
	if !hasStatusSubresource(d) {
		return nil, api.NewAPIError(api.ReasonNotFound, "the definition %q does not enable the status subresource", d.Metadata.Name)
	}
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	k := customKind(d)
	if err := mutate(ctx, s, k, "status", api.OperationUpdate, next); err != nil {
		return nil, err
	}
	if err := next.Validate(typeOf(d)); err != nil {
		return nil, err
	}
	o, rev, err := k.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(k.plural, name, next.Metadata.ResourceVersion, o.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	old := *o
	if o.Status, err = fitSection(d.Spec.Versions[0].Schema.OpenAPIV3Schema, "status", next.Status); err != nil {
		return nil, err
	}
	if err := validate(ctx, s, k, "status", api.OperationUpdate, o, &old); err != nil {
		return nil, err
	}
	return o, k.update(ctx, s.st, o, rev)
}

// DeleteCustomObject sets the deletion time of the object if rv is its current
// version, and removes it when it holds no finalizer. An object already being
// deleted is returned as it is.
func (s *service) DeleteCustomObject(ctx context.Context, p CustomPath, name string, rv api.ResourceVersion) (*api.CustomObject, error) {
	d, o, removed, err := s.deleteCustomObject(ctx, p, name, rv)
	if err == nil && removed {
		_, err = s.finishDefinition(ctx, d)
	}
	return o, err
}

func (s *service) deleteCustomObject(ctx context.Context, p CustomPath, name string, rv api.ResourceVersion) (*api.CustomResourceDefinition, *api.CustomObject, bool, error) {
	s.defs.RLock()
	defer s.defs.RUnlock()
	d, err := s.servedDefinition(ctx, p)
	if err != nil {
		return nil, nil, false, err
	}
	k := customKind(d)
	o, rev, err := k.get(ctx, s.st, name)
	if err != nil {
		return nil, nil, false, err
	}
	if err := checkVersion(k.plural, name, rv, o.Metadata.ResourceVersion); err != nil {
		return nil, nil, false, err
	}
	if o.Metadata.DeletionTimestamp != nil {
		return d, o, false, nil
	}
	if err := admitDelete(ctx, s, k, o); err != nil {
		return nil, nil, false, err
	}
	now := s.now().UTC()
	o.Metadata.DeletionTimestamp = &now
	if len(o.Metadata.Finalizers) > 0 {
		return d, o, false, k.update(ctx, s.st, o, rev)
	}
	return d, o, true, k.remove(ctx, s.st, o, rev)
}

func (s *service) GetCustomObject(ctx context.Context, p CustomPath, name string) (*api.CustomObject, error) {
	d, err := s.servedDefinition(ctx, p)
	if err != nil {
		return nil, err
	}
	o, _, err := customKind(d).get(ctx, s.st, name)
	return o, err
}

func (s *service) ListCustomObjects(ctx context.Context, p CustomPath) (*api.CustomObjectList, error) {
	d, err := s.servedDefinition(ctx, p)
	if err != nil {
		return nil, err
	}
	items, rv, err := customKind(d).list(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return &api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: d.Spec.Group + "/" + d.Spec.Versions[0].Name, Kind: d.Spec.Names.ListKind}, Metadata: api.ListMeta{ResourceVersion: rv}, Items: items}, nil
}

func (s *service) WatchCustomObjects(ctx context.Context, p CustomPath, rv api.ResourceVersion) (<-chan api.WatchEvent, error) {
	d, err := s.servedDefinition(ctx, p)
	if err != nil {
		return nil, err
	}
	return customKind(d).watch(ctx, s.st, rv)
}
