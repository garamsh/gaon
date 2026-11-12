package authorization

import (
	"context"
	"errors"
	"slices"

	"github.com/garamsh/gaon/lib/api"
)

type rbac struct {
	src Source
}

// NewRBAC returns the Authorizer of the RBAC mode: the Node authorizer, then
// the ClusterRoles bound to the caller. It reads src for every decision, so an
// edit of a role or a binding takes effect on the next request.
func NewRBAC(src Source) Authorizer {
	return &rbac{src: src}
}

func (a *rbac) Authorize(ctx context.Context, c Caller, r Request) error {
	if ok, err := a.node(ctx, c.User, r); err != nil || ok {
		return err
	}
	rules, err := a.rules(ctx, c)
	if err != nil {
		return err
	}
	if !covers(rules, r.Group, r.qualified(), string(r.Verb)) {
		return api.NewAPIError(api.ReasonForbidden, "%q may not %s in %q", c.User, r, r.Group)
	}
	return nil
}

func (a *rbac) AuthorizeRole(ctx context.Context, c Caller, wanted []api.PolicyRule) error {
	held, err := a.rules(ctx, c)
	if err != nil {
		return err
	}
	for _, rule := range wanted {
		for _, g := range rule.APIGroups {
			for _, res := range rule.Resources {
				for _, v := range rule.Verbs {
					if !covers(held, g, res, string(v)) {
						return api.NewAPIError(api.ReasonForbidden, "%q does not hold %s on %s in %q, which the role grants", c.User, v, res, g)
					}
				}
			}
		}
	}
	return nil
}

func (a *rbac) AuthorizeBinding(ctx context.Context, c Caller, ref api.RoleRef) error {
	role, err := a.src.GetClusterRole(ctx, ref.Name)
	if errors.Is(err, api.ErrNotFound) {
		return api.NewAPIError(api.ReasonInvalid, "roleRef names clusterrole %q, which does not exist", ref.Name)
	}
	if err != nil {
		return err
	}
	return a.AuthorizeRole(ctx, c, role.Rules)
}

// rules returns the rules of every ClusterRole a binding gives to c's user or
// to one of its groups.
func (a *rbac) rules(ctx context.Context, c Caller) ([]api.PolicyRule, error) {
	bindings, err := a.src.ListClusterRoleBindings(ctx)
	if err != nil {
		return nil, err
	}
	var rules []api.PolicyRule
	for _, b := range bindings.Items {
		if !slices.ContainsFunc(b.Subjects, c.isSubject) {
			continue
		}
		role, err := a.src.GetClusterRole(ctx, b.RoleRef.Name)
		if errors.Is(err, api.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		rules = append(rules, role.Rules...)
	}
	return rules, nil
}

func (c Caller) isSubject(s api.Subject) bool {
	switch s.Kind {
	case api.SubjectUser:
		return s.Name == c.User
	case api.SubjectGroup:
		return slices.Contains(c.Groups, s.Name)
	}
	return false
}

// covers reports whether a rule lists the group, the resource and the verb, or
// holds the wildcard for each it does not list.
func covers(rules []api.PolicyRule, group, resource, verb string) bool {
	return slices.ContainsFunc(rules, func(r api.PolicyRule) bool {
		return listed(r.APIGroups, group) && listed(r.Resources, resource) && listed(r.Verbs, verb)
	})
}

// listed reports whether names holds name or the wildcard. A name holding a
// wildcard among other characters is a plain name.
func listed[S ~string](names []S, name string) bool {
	return slices.Contains(names, S(name)) || slices.Contains(names, S("*"))
}
