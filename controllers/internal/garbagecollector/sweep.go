package garbagecollector

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// object is an object a sweep listed and the kind it belongs to.
type object struct {
	res resource
	obj api.CustomObject
}

// snapshot is what one sweep listed: a client per kind, and the uid of each
// object by kind and name. A kind that is not a key of uids was not served or
// could not be listed, so nothing is known of its owners.
type snapshot struct {
	clients map[kindKey]*api.CustomResourceClient
	uids    map[kindKey]map[string]api.UID
	objects []object
}

// sweep lists every kind and deletes each object whose owners are all gone.
// A failure of one kind or one object is logged and does not stop the rest.
func (s *service) sweep(ctx context.Context) {
	resources, err := s.resources(ctx)
	if err != nil {
		s.log.Warn("sweep sees only the built-in kinds", "operation", "list definitions", "error", err)
	}
	snap := &snapshot{clients: map[kindKey]*api.CustomResourceClient{}, uids: map[kindKey]map[string]api.UID{}}
	for _, r := range resources {
		c, err := s.clientFor(r.typ)
		if err != nil {
			s.log.Warn("kind skipped", "operation", "make client", "kind", r.typ.Kind, "group", r.typ.Group, "error", err)
			continue
		}
		list, err := c.List(ctx)
		if err != nil {
			s.log.Warn("kind skipped", "operation", "list", "kind", r.typ.Kind, "group", r.typ.Group, "error", err)
			continue
		}
		uids := make(map[string]api.UID, len(list.Items))
		for _, o := range list.Items {
			uids[o.Metadata.Name] = o.Metadata.UID
			snap.objects = append(snap.objects, object{res: r, obj: o})
		}
		snap.clients[r.key()] = c
		snap.uids[r.key()] = uids
	}
	for _, o := range snap.objects {
		if ctx.Err() != nil {
			return
		}
		s.collect(ctx, snap, o)
	}
}

// collect deletes o when it has owners and every one is gone. An object that
// is already being deleted, or whose kind has no DELETE, is left alone.
func (s *service) collect(ctx context.Context, snap *snapshot, o object) {
	m := o.obj.Metadata
	if len(m.OwnerReferences) == 0 || m.DeletionTimestamp != nil || !o.res.deletable {
		return
	}
	for _, ref := range m.OwnerReferences {
		if !s.gone(ctx, snap, ref) {
			return
		}
	}
	_, err := snap.clients[o.res.key()].Delete(ctx, m.Name, m.ResourceVersion)
	switch {
	case err == nil:
		s.log.Info("collected", "kind", o.res.typ.Kind, "group", o.res.typ.Group, "name", m.Name, "owners", len(m.OwnerReferences))
	case errors.Is(err, api.ErrConflict), errors.Is(err, api.ErrNotFound):
		// Changed or removed since the list: the next sweep decides again.
	default:
		s.log.Warn("delete failed", "operation", "delete", "kind", o.res.typ.Kind, "group", o.res.typ.Group, "name", m.Name, "error", err)
	}
}

// gone reports whether ref's owner is shown to be gone: its kind was listed,
// the list holds no object of that name with the reference's uid, and a read
// of the name confirms it. The read stops an owner created after the list from
// being taken for a missing one. An owner whose kind is unknown, or whose read
// failed, is not shown gone.
func (s *service) gone(ctx context.Context, snap *snapshot, ref api.OwnerReference) bool {
	key := kindKey{apiVersion: ref.APIVersion, kind: ref.Kind}
	uids, ok := snap.uids[key]
	if !ok {
		return false
	}
	if uid, listed := uids[ref.Name]; listed && sameIncarnation(ref.UID, uid) {
		return false
	}
	cur, err := snap.clients[key].Get(ctx, ref.Name)
	switch {
	case errors.Is(err, api.ErrNotFound):
		return true
	case err != nil:
		s.log.Warn("owner not read", "operation", "get", "kind", ref.Kind, "name", ref.Name, "error", err)
		return false
	}
	return !sameIncarnation(ref.UID, cur.Metadata.UID)
}
