// Package authorization decides whether a caller identified by a client
// certificate may make a request, and whether it may grant the permissions of
// a ClusterRole.
package authorization

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Caller is who made a request: the CN of its client certificate, and the
// Organization values of its subject.
type Caller struct {
	User   string
	Groups []string
}

// Request is a verb on a resource of an API group. Subresource is empty
// except for the status and binding paths, and Name for a collection.
// Labels are the labels of the object in the body of a create or an update,
// and Selector is the label selector of a list or a watch: the two things a
// rule with a label selector judges that are not stored.
type Request struct {
	Group       string
	Resource    string
	Subresource string
	Verb        api.Verb
	Name        string
	Labels      map[string]string
	Selector    api.Selector
	// BoundMicroVM is the MicroVM the body of a token request binds, set for
	// the create of serviceaccounts/token and for no other request.
	BoundMicroVM api.BoundObject
}

// Authorizer allows a request or refuses it with a Forbidden error. Any other
// error is the server's.
type Authorizer interface {
	// Authorize allows r from c, or returns Forbidden.
	Authorize(ctx context.Context, c Caller, r Request) error
	// AuthorizeRole allows c to write a ClusterRole with rules, or returns
	// Forbidden where c does not already hold every permission they grant.
	AuthorizeRole(ctx context.Context, c Caller, rules []api.PolicyRule) error
	// AuthorizeMicroVMUpdate allows c to replace the stored MicroVM name
	// with next where a role grants it update on microvms, and otherwise,
	// where the Node authorizer's grant is all it has, only where next
	// differs from the stored MicroVM in the Node Agent's finalizer alone.
	// It returns Forbidden for any other change.
	AuthorizeMicroVMUpdate(ctx context.Context, c Caller, name string, next *api.MicroVM) error
	// AuthorizeVolumeUpdate allows c to replace the stored Volume name with
	// next where a role grants it update on volumes, and otherwise, where the
	// Node authorizer's grant is all it has, only where next differs from
	// the stored Volume in the Node Agent's finalizer alone. It returns
	// Forbidden for any other change.
	AuthorizeVolumeUpdate(ctx context.Context, c Caller, name string, next *api.Volume) error
	// AuthorizeVolumeSnapshotUpdate allows c to replace the stored
	// VolumeSnapshot name with next where a role grants it update on
	// volumesnapshots, and otherwise, where the Node authorizer's grant is all
	// it has, only where next differs from the stored VolumeSnapshot in the
	// Node Agent's finalizer alone. It returns Forbidden for any other change.
	AuthorizeVolumeSnapshotUpdate(ctx context.Context, c Caller, name string, next *api.VolumeSnapshot) error
	// AuthorizeBinding allows c to write a ClusterRoleBinding of ref, or
	// returns Forbidden where c does not hold every permission of the role,
	// and Invalid where the role does not exist.
	AuthorizeBinding(ctx context.Context, c Caller, ref api.RoleRef) error
}

type alwaysAllow struct{}

// AlwaysAllow returns the Authorizer that allows everything.
func AlwaysAllow() Authorizer { return alwaysAllow{} }

func (alwaysAllow) Authorize(context.Context, Caller, Request) error              { return nil }
func (alwaysAllow) AuthorizeRole(context.Context, Caller, []api.PolicyRule) error { return nil }
func (alwaysAllow) AuthorizeMicroVMUpdate(context.Context, Caller, string, *api.MicroVM) error {
	return nil
}
func (alwaysAllow) AuthorizeVolumeUpdate(context.Context, Caller, string, *api.Volume) error {
	return nil
}
func (alwaysAllow) AuthorizeVolumeSnapshotUpdate(context.Context, Caller, string, *api.VolumeSnapshot) error {
	return nil
}
func (alwaysAllow) AuthorizeBinding(context.Context, Caller, api.RoleRef) error { return nil }

func (r Request) String() string {
	return string(r.Verb) + " " + r.qualified()
}

// qualified is the resource as a rule names it: microvms, or microvms/status.
func (r Request) qualified() string {
	if r.Subresource == "" {
		return r.Resource
	}
	return r.Resource + "/" + r.Subresource
}
