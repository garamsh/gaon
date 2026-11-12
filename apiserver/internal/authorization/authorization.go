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
type Request struct {
	Group       string
	Resource    string
	Subresource string
	Verb        api.Verb
	Name        string
}

// Authorizer allows a request or refuses it with a Forbidden error. Any other
// error is the server's.
type Authorizer interface {
	// Authorize allows r from c, or returns Forbidden.
	Authorize(ctx context.Context, c Caller, r Request) error
	// AuthorizeRole allows c to write a ClusterRole with rules, or returns
	// Forbidden where c does not already hold every permission they grant.
	AuthorizeRole(ctx context.Context, c Caller, rules []api.PolicyRule) error
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
func (alwaysAllow) AuthorizeBinding(context.Context, Caller, api.RoleRef) error   { return nil }

func (r Request) String() string {
	return string(r.Verb) + " " + r.qualified()
}

// qualified is the resource as a rule names it: machines, or machines/status.
func (r Request) qualified() string {
	if r.Subresource == "" {
		return r.Resource
	}
	return r.Resource + "/" + r.Subresource
}
