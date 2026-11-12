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
	allowed, err := a.allowed(ctx, rules, r)
	if err != nil {
		return err
	}
	if !allowed {
		return api.NewAPIError(api.ReasonForbidden, "%q may not %s in %q", c.User, r, r.Group)
	}
	return nil
}

// allowed reports whether one rule lists the group, the resource and the verb
// of r and passes r through its label selector (ADR 0018, item 8). Rules are
// judged one at a time, never combined. The stored labels are read once, and
// only when a rule that has a selector needs them.
func (a *rbac) allowed(ctx context.Context, rules []api.PolicyRule, r Request) (bool, error) {
	var stored map[string]string
	read := false
	for _, rule := range rules {
		if !listed(rule.APIGroups, r.Group) || !listed(rule.Resources, r.qualified()) || !listed(rule.Verbs, string(r.Verb)) {
			continue
		}
		if rule.LabelSelector == "" {
			return true, nil
		}
		switch {
		case r.Verb == api.VerbList || r.Verb == api.VerbWatch:
			if rule.AllowsSelector(r.Selector) {
				return true, nil
			}
		case r.Verb == api.VerbCreate && r.Subresource == "":
			if rule.AllowsLabels(r.Labels) {
				return true, nil
			}
		default:
			if !read {
				var err error
				if stored, err = a.src.Labels(ctx, r.Group, r.Resource, r.Name); err != nil {
					return false, err
				}
				read = true
			}
			// An update of the object itself is judged on the labels it has
			// and on the labels it would have.
			if rule.AllowsLabels(stored) && (r.Verb != api.VerbUpdate || r.Subresource != "" || rule.AllowsLabels(r.Labels)) {
				return true, nil
			}
		}
	}
	return false, nil
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
					if !holds(held, g, res, string(v), rule) {
						return api.NewAPIError(api.ReasonForbidden, "%q does not hold %s on %s in %q%s, which the role grants", c.User, v, res, g, selectorNote(rule))
					}
				}
			}
		}
	}
	return nil
}

func selectorNote(r api.PolicyRule) string {
	if r.LabelSelector == "" {
		return ""
	}
	return " for " + r.LabelSelector
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
	bindings, err := a.src.ListClusterRoleBindings(ctx, nil)
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

// holds reports whether a held rule lists the group, the resource and the
// verb, or holds the wildcard for each it does not list, and is no narrower
// than want: it has no label selector, or want's selector selects nothing it
// does not. A rule of want that has no selector is held only through a rule
// that has none.
func holds(held []api.PolicyRule, group, resource, verb string, want api.PolicyRule) bool {
	wantSel, err := want.Selector()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(held, func(r api.PolicyRule) bool {
		return listed(r.APIGroups, group) && listed(r.Resources, resource) && listed(r.Verbs, verb) && r.AllowsSelector(wantSel)
	})
}

// listed reports whether names holds name or the wildcard. A name holding a
// wildcard among other characters is a plain name.
func listed[S ~string](names []S, name string) bool {
	return slices.Contains(names, S(name)) || slices.Contains(names, S("*"))
}
