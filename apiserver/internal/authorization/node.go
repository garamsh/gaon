package authorization

import (
	"context"
	"errors"

	"github.com/garamsh/gaon/lib/api"
)

// node reports whether the caller named user is a Node that may make r under
// ADR 0008, Decision 5. It never refuses: false leaves r to the roles.
func (a *rbac) node(ctx context.Context, user string, r Request) (bool, error) {
	if user == "" || r.Group != api.Group {
		return false, nil
	}
	own := r.Name == user
	switch r.qualified() {
	case "nodes":
		return own && (r.Verb == api.VerbCreate || r.Verb == api.VerbGet), nil
	case "nodes/status":
		return own && r.Verb == api.VerbUpdate, nil
	case "leases":
		return own && (r.Verb == api.VerbGet || r.Verb == api.VerbCreate || r.Verb == api.VerbUpdate), nil
	case "microvms":
		switch r.Verb {
		case api.VerbGet, api.VerbList, api.VerbWatch:
			return a.isNode(ctx, user)
		case api.VerbUpdate:
			return a.bound(ctx, user, r.Name)
		}
	case "microvms/status":
		if r.Verb == api.VerbUpdate {
			return a.bound(ctx, user, r.Name)
		}
	case "volumes":
		switch r.Verb {
		case api.VerbGet, api.VerbList, api.VerbWatch:
			return a.isNode(ctx, user)
		case api.VerbUpdate:
			return a.pinned(ctx, user, r.Name)
		}
	case "volumes/status":
		if r.Verb == api.VerbUpdate {
			if ok, err := a.pinned(ctx, user, r.Name); ok || err != nil {
				return ok, err
			}
			return a.uses(ctx, user, r.Name)
		}
	case "certificatesigningrequests":
		switch r.Verb {
		case api.VerbCreate:
			return a.isNode(ctx, user)
		case api.VerbGet:
			return a.created(ctx, user, r.Name)
		}
	}
	return false, nil
}

// isNode reports whether a Node is named user.
func (a *rbac) isNode(ctx context.Context, user string) (bool, error) {
	_, err := a.src.GetNode(ctx, user)
	return found(err)
}

// bound reports whether the MicroVM name is bound to the Node user.
func (a *rbac) bound(ctx context.Context, user, name string) (bool, error) {
	m, err := a.src.GetMicroVM(ctx, name)
	if ok, err := found(err); !ok || err != nil {
		return false, err
	}
	return m.Spec.NodeName == user, nil
}

// pinned reports whether the Volume name is pinned to the Node user.
func (a *rbac) pinned(ctx context.Context, user, name string) (bool, error) {
	v, err := a.src.GetVolume(ctx, name)
	if ok, err := found(err); !ok || err != nil {
		return false, err
	}
	return v.Spec.NodeName == user, nil
}

// uses reports whether a MicroVM bound to the Node user mounts the Volume
// name.
func (a *rbac) uses(ctx context.Context, user, name string) (bool, error) {
	ms, err := a.src.ListMicroVMs(ctx, nil)
	if err != nil {
		return false, err
	}
	for _, m := range ms.Items {
		if m.Spec.NodeName != user {
			continue
		}
		for _, v := range m.Spec.Volumes {
			if v.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}

// created reports whether the Node user created the request name.
func (a *rbac) created(ctx context.Context, user, name string) (bool, error) {
	r, err := a.src.GetCertificateSigningRequest(ctx, name)
	if ok, err := found(err); !ok || err != nil {
		return false, err
	}
	if ok, err := a.isNode(ctx, user); !ok || err != nil {
		return false, err
	}
	return r.Spec.Username == user, nil
}

// found maps a read that finds nothing to false and keeps any other error.
func found(err error) (bool, error) {
	if errors.Is(err, api.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
