package authorization

import (
	"context"
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// Writer is what Bootstrap creates the policy with. An Ensure creates the
// object where none of its name exists, leaves one that exists, and calls no
// admission webhook.
type Writer interface {
	EnsureClusterRole(ctx context.Context, r *api.ClusterRole) error
	EnsureClusterRoleBinding(ctx context.Context, b *api.ClusterRoleBinding) error
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
	// leaseVerbs are what leader election takes: read the Lease, create it,
	// and renew or take it over by an update.
	leaseVerbs   = []api.Verb{api.VerbGet, api.VerbCreate, api.VerbUpdate}
	coordination = []string{api.CoordinationGroup}
)

// bootstrapRoles are the rules of ADR 0008, Decision 6, as ADR 0021 changes
// them, in group compute.gaon.garam.sh except where a rule names another.
var bootstrapRoles = []struct {
	role, group string
	rules       []api.PolicyRule
}{
	{RoleScheduler, GroupSchedulers, []api.PolicyRule{
		{APIGroups: compute, Resources: []string{"microvms", "nodes", "volumes"}, Verbs: readVerbs},
		{APIGroups: compute, Resources: []string{"microvms/binding"}, Verbs: []api.Verb{api.VerbCreate}},
		{APIGroups: compute, Resources: []string{"microvms/status"}, Verbs: updateVerb},
		{APIGroups: coordination, Resources: []string{"leases"}, Verbs: leaseVerbs},
	}},
	{RoleControllers, GroupControllers, []api.PolicyRule{
		{APIGroups: []string{api.APIGroupAll}, Resources: []string{api.ResourceAll}, Verbs: []api.Verb{api.VerbGet, api.VerbList, api.VerbWatch, api.VerbDelete}},
		{APIGroups: compute, Resources: []string{"microvms"}, Verbs: []api.Verb{api.VerbCreate, api.VerbUpdate}},
		{APIGroups: compute, Resources: []string{"endpoints"}, Verbs: []api.Verb{api.VerbCreate, api.VerbUpdate}},
		{APIGroups: compute, Resources: []string{"microvms/status", "nodes/status", "volumes/status", "certificatesigningrequests/status"}, Verbs: updateVerb},
		{APIGroups: coordination, Resources: []string{"leases"}, Verbs: leaseVerbs},
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
		if err := w.EnsureClusterRole(ctx, role); err != nil {
			return fmt.Errorf("create clusterrole %s: %w", b.role, err)
		}
		binding := &api.ClusterRoleBinding{
			TypeMeta: api.TypeMeta{APIVersion: api.RBACGroupVersion, Kind: api.KindClusterRoleBinding},
			Metadata: api.ObjectMeta{Name: b.role},
			RoleRef:  api.RoleRef{Kind: api.KindClusterRole, Name: b.role},
			Subjects: []api.Subject{{Kind: api.SubjectGroup, Name: b.group}},
		}
		if err := w.EnsureClusterRoleBinding(ctx, binding); err != nil {
			return fmt.Errorf("create clusterrolebinding %s: %w", b.role, err)
		}
	}
	return nil
}
