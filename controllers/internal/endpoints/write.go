package endpoints

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/garamsh/gaon/lib/api"
)

// write makes the stored Endpoints of svc list what subsetsFor gives, and name
// svc as its owner. It writes nothing when the stored object already does.
func (s *service) write(ctx context.Context, svc *api.Service) error {
	want := subsetsFor(svc, s.microvms)
	cur, err := s.client.GetEndpoints(ctx, svc.Metadata.Name)
	if errors.Is(err, api.ErrNotFound) {
		cur = &api.Endpoints{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindEndpoints},
			Metadata: api.ObjectMeta{Name: svc.Metadata.Name},
		}
	} else if err != nil {
		return fmt.Errorf("get endpoints %s: %w", svc.Metadata.Name, err)
	}
	next := *cur
	next.Subsets = want
	next.Metadata.OwnerReferences = withOwner(cur.Metadata.OwnerReferences, svc)
	if cur.Metadata.ResourceVersion != "" && reflect.DeepEqual(next, *cur) {
		return nil
	}
	if _, err := s.client.PutEndpoints(ctx, &next); err != nil {
		return fmt.Errorf("put endpoints %s: %w", svc.Metadata.Name, err)
	}
	return nil
}

// withOwner returns refs with the reference to svc, as the controller of the
// object, in place of any other reference to the same Service.
func withOwner(refs []api.OwnerReference, svc *api.Service) []api.OwnerReference {
	own := api.OwnerReference{APIVersion: api.GroupVersion, Kind: api.KindService, Name: svc.Metadata.Name, UID: svc.Metadata.UID, Controller: true}
	out := make([]api.OwnerReference, 0, len(refs)+1)
	for _, r := range refs {
		if r.APIVersion != own.APIVersion || r.Kind != own.Kind || r.Name != own.Name {
			out = append(out, r)
		}
	}
	return append(out, own)
}
