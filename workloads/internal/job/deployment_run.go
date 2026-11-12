package job

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// sweepDeployments reconciles every Deployment once. A failure of one is
// logged and does not stop the others.
func (s *service) sweepDeployments(ctx context.Context) {
	list, err := s.deployments.List(ctx)
	if err != nil {
		s.log.Warn("list deployments", "error", err)
		return
	}
	if len(list.Items) == 0 {
		return
	}
	mvms, err := s.microvms.ListMicroVMs(ctx)
	if err != nil {
		s.log.Warn("list microvms", "error", err)
		return
	}
	for i := range list.Items {
		if err := s.reconcileDeployment(ctx, &list.Items[i], mvms.Items); err != nil {
			s.log.Warn("reconcile deployment", "deployment", list.Items[i].Metadata.Name, "error", err)
		}
	}
}

// fleet is the MicroVMs a Deployment owns, sorted by what the rollout does
// with them.
type fleet struct {
	// fresh and stale are the replicas that are not being deleted and have not
	// failed, made from the current template and from another.
	fresh, stale []api.MicroVM
	// failed are Failed replicas not yet being deleted.
	failed []api.MicroVM
	// present counts every owned MicroVM, those being deleted and failed
	// included: each still holds a place until it is gone.
	present int
}

func classify(mine []api.MicroVM, hash string) fleet {
	f := fleet{present: len(mine)}
	for _, m := range mine {
		current := m.Metadata.Labels[TemplateHashLabel] == hash
		switch {
		case m.Metadata.DeletionTimestamp != nil:
		case m.EffectivePhase() == api.MicroVMFailed:
			f.failed = append(f.failed, m)
		case current:
			f.fresh = append(f.fresh, m)
		default:
			f.stale = append(f.stale, m)
		}
	}
	return f
}

// ready reports whether the MicroVM's Ready condition is true, in a status of
// the spec's epoch.
func ready(m api.MicroVM) bool {
	if m.Status.BindingEpoch != m.Spec.BindingEpoch {
		return false
	}
	return slices.ContainsFunc(m.Status.Conditions, func(c api.Condition) bool {
		return c.Type == "Ready" && c.Status == api.ConditionTrue
	})
}

func countReady(ms []api.MicroVM) int32 {
	var n int32
	for _, m := range ms {
		if ready(m) {
			n++
		}
	}
	return n
}

// step is what one sweep does to a Deployment's replicas.
type step struct {
	remove []api.MicroVM
	create int
}

// plan decides the sweep's step. It removes the Failed replicas, then the
// replicas above the count, or above the count plus the surge while a replica of
// another template remains (those not Ready first, then those of another
// template, then the highest names), then the replicas of another template: the
// ones not Ready at once, the ready ones only while that leaves at least
// replicas - unavailable Ready. It creates replicas of the current template up
// to the count while no more than replicas + surge MicroVMs, those being
// deleted included, would exist. Recreate has no surge, so it creates none until
// every MicroVM of the old template is gone.
func (f fleet) plan(replicas, surge, unavailable int) step {
	p := step{remove: slices.Clone(f.failed)}
	fresh, stale := slices.Clone(f.fresh), slices.Clone(f.stale)

	limit := replicas
	if len(stale) > 0 {
		limit += surge
	}
	if excess := len(fresh) + len(stale) - limit; excess > 0 {
		type candidate struct {
			m     api.MicroVM
			stale bool
		}
		var all []candidate
		for _, m := range fresh {
			all = append(all, candidate{m, false})
		}
		for _, m := range stale {
			all = append(all, candidate{m, true})
		}
		// A lower rank goes first: not Ready before Ready, then stale before fresh.
		rank := func(c candidate) int {
			r := 0
			if ready(c.m) {
				r += 2
			}
			if !c.stale {
				r++
			}
			return r
		}
		slices.SortFunc(all, func(a, b candidate) int {
			return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(b.m.Metadata.Name, a.m.Metadata.Name))
		})
		gone := map[string]bool{}
		for _, c := range all[:excess] {
			p.remove = append(p.remove, c.m)
			gone[c.m.Metadata.Name] = true
		}
		isGone := func(m api.MicroVM) bool { return gone[m.Metadata.Name] }
		fresh, stale = slices.DeleteFunc(fresh, isGone), slices.DeleteFunc(stale, isGone)
	}

	available := int(countReady(fresh) + countReady(stale))
	canDrop := max(0, available-max(0, replicas-unavailable))
	slices.SortFunc(stale, func(a, b api.MicroVM) int { return strings.Compare(b.Metadata.Name, a.Metadata.Name) })
	for _, m := range stale {
		switch {
		case !ready(m):
			p.remove = append(p.remove, m)
		case canDrop > 0:
			p.remove = append(p.remove, m)
			canDrop--
		}
	}

	p.create = max(0, min(replicas-len(fresh), replicas+surge-f.present))
	return p
}

// reconcileDeployment brings one Deployment's replicas one step toward its
// spec and writes the counts taken before the step. all is every MicroVM the
// unit wrote.
func (s *service) reconcileDeployment(ctx context.Context, obj *api.CustomObject, all []api.MicroVM) error {
	var spec DeploymentSpec
	if err := json.Unmarshal(obj.Spec, &spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}
	var old DeploymentStatus
	if len(obj.Status) > 0 {
		if err := json.Unmarshal(obj.Status, &old); err != nil {
			return fmt.Errorf("decode status: %w", err)
		}
	}
	if len(spec.Selector.MatchLabels) == 0 {
		return errors.New("spec.selector.matchLabels is empty")
	}
	replicas := int(spec.Replicas)
	surge, unavailable, err := spec.Strategy.bounds(replicas)
	if err != nil {
		return err
	}
	tmpl := replicaTemplate(spec.Template)
	hash, err := templateHash(tmpl)
	if err != nil {
		return err
	}

	f := classify(owned(KindDeployment, obj, all), hash)
	next := DeploymentStatus{
		ObservedGeneration: obj.Metadata.Generation,
		Replicas:           int32(len(f.fresh) + len(f.stale)),
		ReadyReplicas:      countReady(f.fresh) + countReady(f.stale),
		UpdatedReplicas:    int32(len(f.fresh)),
	}
	next.AvailableReplicas = next.ReadyReplicas

	p := f.plan(replicas, surge, unavailable)
	errs := []error{s.removeReplicas(ctx, p.remove)}
	errs = append(errs, s.createReplicas(ctx, obj, spec, tmpl, hash, p.create, all))

	if !reflect.DeepEqual(old, next) {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = s.deployments.PutStatus(ctx, &api.CustomObject{TypeMeta: obj.TypeMeta, Metadata: obj.Metadata, Status: raw})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// removeReplicas deletes the MicroVMs, tolerating one already gone or changed:
// the next sweep sees it as it is.
func (s *service) removeReplicas(ctx context.Context, remove []api.MicroVM) error {
	var errs []error
	for _, m := range remove {
		_, err := s.microvms.DeleteMicroVM(ctx, m.Metadata.Name, m.Metadata.ResourceVersion)
		if err != nil && !errors.Is(err, api.ErrNotFound) && !errors.Is(err, api.ErrConflict) {
			errs = append(errs, fmt.Errorf("delete microvm %s: %w", m.Metadata.Name, err))
		}
	}
	return errors.Join(errs...)
}

// createReplicas writes n MicroVMs of the current template, each named by the
// first index no listed MicroVM uses, so that a second creator of the same
// replica is answered AlreadyExists.
func (s *service) createReplicas(ctx context.Context, dep *api.CustomObject, spec DeploymentSpec, tmpl api.MicroVMSpec, hash string, n int, all []api.MicroVM) error {
	taken := make(map[string]bool, len(all))
	for _, m := range all {
		taken[m.Metadata.Name] = true
	}
	labels := maps.Clone(spec.Selector.MatchLabels)
	labels[ManagedByLabel], labels[DeploymentLabel], labels[TemplateHashLabel] = Manager, dep.Metadata.Name, hash
	var errs []error
	for range n {
		name := nextName(dep.Metadata.Name, taken)
		_, err := s.microvms.PutMicroVM(ctx, &api.MicroVM{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
			Metadata: api.ObjectMeta{
				Name:   name,
				Labels: maps.Clone(labels),
				OwnerReferences: []api.OwnerReference{{
					APIVersion: APIVersion, Kind: KindDeployment, Name: dep.Metadata.Name, UID: dep.Metadata.UID, Controller: true,
				}},
			},
			Spec: tmpl,
		})
		switch {
		case errors.Is(err, api.ErrAlreadyExists):
			s.log.Info("microvm exists", "microvm", name)
		case err != nil:
			errs = append(errs, fmt.Errorf("create microvm %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
