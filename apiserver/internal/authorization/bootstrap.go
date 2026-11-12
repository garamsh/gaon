package authorization

import (
	"context"
	"errors"
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// Writer is what Bootstrap creates the policy with. A Put without a
// resourceVersion creates, and fails with AlreadyExists where the object is
// there.
type Writer interface {
	PutClusterRole(ctx context.Context, name string, r *api.ClusterRole) (*api.ClusterRole, error)
	PutClusterRoleBinding(ctx context.Context, name string, b *api.ClusterRoleBinding) (*api.ClusterRoleBinding, error)
}

// The roles and groups that gaon creates (ADR 0008, Decision 6).
const (
	RoleScheduler   = "gaon:scheduler"
	RoleControllers = "gaon:controllers"
	RoleAdmin       = "gaon:admin"

	GroupSchedulers  = "gaon:schedulers"
	GroupControllers = "gaon:controllers"
	GroupAdmins      = "gaon:admins"
)

var (
	readVerbs  = []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch}
	updateVerb = []api.Verb{api.VerbUpdate}
	compute    = []string{api.Group}
)

// bootstrapRoles are the rules of ADR 0008, Decision 6, in group
// compute.gaon.garam.sh except gaon:admin's.
var bootstrapRoles = []struct {
	role, group string
	rules       []api.PolicyRule
}{
	{RoleScheduler, GroupSchedulers, []api.PolicyRule{
		{APIGroups: compute, Resources: []string{"machines", "nodes"}, Verbs: readVerbs},
		{APIGroups: compute, Resources: []string{"machines/binding"}, Verbs: []api.Verb{api.VerbCreate}},
		{APIGroups: compute, Resources: []string{"machines/status"}, Verbs: updateVerb},
	}},
	{RoleControllers, GroupControllers, []api.PolicyRule{
		{APIGroups: compute, Resources: []string{"machines", "nodes", "leases", "certificatesigningrequests"}, Verbs: readVerbs},
		{APIGroups: compute, Resources: []string{"machines"}, Verbs: []api.Verb{api.VerbCreate, api.VerbUpdate}},
		{APIGroups: compute, Resources: []string{"machines/status", "nodes/status", "volumes/status", "certificatesigningrequests/status"}, Verbs: updateVerb},
	}},
	{RoleAdmin, GroupAdmins, []api.PolicyRule{
		{APIGroups: []string{api.APIGroupAll}, Resources: []string{api.ResourceAll}, Verbs: []api.Verb{api.VerbAll}},
	}},
}

// Bootstrap creates each bootstrap ClusterRole and its ClusterRoleBinding that
// is missing from w. It never overwrites one that exists, so an operator's
// edit survives a restart.
func Bootstrap(ctx context.Context, w Writer) error {
	for _, b := range bootstrapRoles {
		role := &api.ClusterRole{
			TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRole},
			Metadata: api.ObjectMeta{Name: b.role},
			Rules:    b.rules,
		}
		if _, err := w.PutClusterRole(ctx, b.role, role); err != nil && !errors.Is(err, api.ErrAlreadyExists) {
			return fmt.Errorf("create clusterrole %s: %w", b.role, err)
		}
		binding := &api.ClusterRoleBinding{
			TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
			Metadata: api.ObjectMeta{Name: b.role},
			RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: b.role},
			Subjects: []api.Subject{{Kind: api.SubjectGroup, Name: b.group}},
		}
		if _, err := w.PutClusterRoleBinding(ctx, b.role, binding); err != nil && !errors.Is(err, api.ErrAlreadyExists) {
			return fmt.Errorf("create clusterrolebinding %s: %w", b.role, err)
		}
	}
	return nil
}
