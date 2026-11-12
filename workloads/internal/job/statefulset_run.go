package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
)

// sweepStatefulSets reconciles every StatefulSet once. A failure of one is
// logged and does not stop the others.
func (s *service) sweepStatefulSets(ctx context.Context) {
	list, err := s.statefulsets.List(ctx)
	if err != nil {
		s.log.Warn("list statefulsets", "error", err)
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
		if err := s.reconcileStatefulSet(ctx, &list.Items[i], mvms.Items); err != nil {
			s.log.Warn("reconcile statefulset", "statefulset", list.Items[i].Metadata.Name, "error", err)
		}
	}
}

// replicaSet is the MicroVMs a StatefulSet owns, by ordinal.
type replicaSet map[int]api.MicroVM

func (r replicaSet) ordinals() []int {
	return slices.Sorted(maps.Keys(r))
}

func deleting(m api.MicroVM) bool { return m.Metadata.DeletionTimestamp != nil }

func failed(m api.MicroVM) bool {
	return !deleting(m) && m.EffectivePhase() == api.MicroVMFailed
}

// live reports whether the replica is Ready and not being deleted.
func live(m api.MicroVM) bool { return !deleting(m) && ready(m) }

// readyBelow reports whether every replica below ordinal exists and is Ready.
func (r replicaSet) readyBelow(ordinal int) bool {
	for o := range ordinal {
		if m, ok := r[o]; !ok || !live(m) {
			return false
		}
	}
	return true
}

// statefulStep is what one sweep does to a StatefulSet's replicas.
type statefulStep struct {
	remove []api.MicroVM
	create []int
}

// plan decides the sweep's step. It removes the Failed replicas. Then, in the
// first of these that applies, it does nothing more:
//   - replicas above the count exist: it removes them, all at once under
//     Parallel and the highest only under OrderedReady;
//   - replicas below the count are missing: it creates them, all at once
//     under Parallel and the lowest only under OrderedReady, and only while
//     every replica below it is Ready;
//   - with RollingUpdate, a replica from the partition up is of another
//     template: it removes the highest such replica, while every other replica
//     is Ready.
//
// A replica that exists, being deleted included, is not missing: its name is
// held until it is gone.
func (r replicaSet) plan(ro rollout, hash string) statefulStep {
	var p statefulStep
	for _, o := range r.ordinals() {
		if failed(r[o]) {
			p.remove = append(p.remove, r[o])
		}
	}

	if extra := slices.DeleteFunc(r.ordinals(), func(o int) bool { return o < ro.replicas }); len(extra) > 0 {
		if !ro.parallel {
			extra = extra[len(extra)-1:]
		}
		for _, o := range extra {
			if m := r[o]; !deleting(m) && !failed(m) {
				p.remove = append(p.remove, m)
			}
		}
		return p
	}

	var missing []int
	for o := range ro.replicas {
		if _, ok := r[o]; !ok {
			missing = append(missing, o)
		}
	}
	if len(missing) > 0 {
		switch {
		case ro.parallel:
			p.create = missing
		case r.readyBelow(missing[0]):
			p.create = missing[:1]
		}
		return p
	}

	if ro.onDelete || len(p.remove) > 0 {
		return p
	}
	for target := ro.replicas - 1; target >= ro.partition; target-- {
		m := r[target]
		if deleting(m) || m.Metadata.Labels[TemplateHashLabel] == hash {
			continue
		}
		for o := range ro.replicas {
			if o != target && !live(r[o]) {
				return p
			}
		}
		p.remove = append(p.remove, m)
		return p
	}
	return p
}

// reconcileStatefulSet brings one StatefulSet's replicas one step toward its
// spec and writes the counts taken before the step. all is every MicroVM the
// unit wrote.
func (s *service) reconcileStatefulSet(ctx context.Context, obj *api.CustomObject, all []api.MicroVM) error {
	var spec StatefulSetSpec
	if err := json.Unmarshal(obj.Spec, &spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}
	var old StatefulSetStatus
	if len(obj.Status) > 0 {
		if err := json.Unmarshal(obj.Status, &old); err != nil {
			return fmt.Errorf("decode status: %w", err)
		}
	}
	if len(spec.Selector.MatchLabels) == 0 {
		return errors.New("spec.selector.matchLabels is empty")
	}
	ro, err := spec.rollout()
	if err != nil {
		return err
	}
	tmpl := replicaTemplate(spec.Template)
	hash, err := templateHash(tmpl)
	if err != nil {
		return err
	}

	name := obj.Metadata.Name
	replicas := replicaSet{}
	for _, m := range owned(KindStatefulSet, obj, all) {
		if o, ok := ordinalOf(name, m.Metadata.Name); ok {
			replicas[o] = m
		}
	}
	next := StatefulSetStatus{ObservedGeneration: obj.Metadata.Generation}
	for _, m := range replicas {
		if deleting(m) || failed(m) {
			continue
		}
		next.Replicas++
		if ready(m) {
			next.ReadyReplicas++
		}
		if m.Metadata.Labels[TemplateHashLabel] == hash {
			next.UpdatedReplicas++
		}
	}

	p := replicas.plan(ro, hash)
	errs := []error{s.removeReplicas(ctx, p.remove)}
	errs = append(errs, s.createStatefulReplicas(ctx, obj, spec, tmpl, hash, p.create))

	if !reflect.DeepEqual(old, next) {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = s.statefulsets.PutStatus(ctx, &api.CustomObject{TypeMeta: obj.TypeMeta, Metadata: obj.Metadata, Status: raw})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// createStatefulReplicas writes the replicas of the ordinals, each after its
// Volumes. A Volume is created only when none of its name is listed, and never
// deleted, so a replica made again finds its Volume as it left it.
func (s *service) createStatefulReplicas(ctx context.Context, set *api.CustomObject, spec StatefulSetSpec, tmpl api.MicroVMSpec, hash string, ordinals []int) error {
	if len(ordinals) == 0 {
		return nil
	}
	have := map[string]bool{}
	if len(spec.VolumeClaimTemplates) > 0 {
		list, err := s.microvms.ListVolumes(ctx)
		if err != nil {
			return fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range list.Items {
			have[v.Metadata.Name] = true
		}
	}
	name := set.Metadata.Name
	var errs []error
	for _, o := range ordinals {
		if err := s.createVolumes(ctx, name, spec, o, have); err != nil {
			errs = append(errs, err)
			continue
		}
		replica := name + "-" + strconv.Itoa(o)
		labels := maps.Clone(spec.Selector.MatchLabels)
		labels[ManagedByLabel], labels[StatefulSetLabel], labels[TemplateHashLabel] = Manager, name, hash
		labels[OrdinalLabel], labels[ReplicaNameLabel] = strconv.Itoa(o), replica
		_, err := s.microvms.PutMicroVM(ctx, &api.MicroVM{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
			Metadata: api.ObjectMeta{
				Name:   replica,
				Labels: labels,
				OwnerReferences: []api.OwnerReference{{
					APIVersion: APIVersion, Kind: KindStatefulSet, Name: name, UID: set.Metadata.UID, Controller: true,
				}},
			},
			Spec: replicaSpec(name, spec, tmpl, o),
		})
		switch {
		case errors.Is(err, api.ErrAlreadyExists):
			s.log.Info("microvm exists", "microvm", replica)
		case err != nil:
			errs = append(errs, fmt.Errorf("create microvm %s: %w", replica, err))
		}
	}
	return errors.Join(errs...)
}

// createVolumes writes the Volumes of replica ordinal that are not in have. A
// Volume is not owned by the StatefulSet: the garbage collector would remove it
// with the set, and the data is kept.
func (s *service) createVolumes(ctx context.Context, set string, spec StatefulSetSpec, ordinal int, have map[string]bool) error {
	for _, c := range spec.VolumeClaimTemplates {
		vol := volumeName(c.Name, set, ordinal)
		if have[vol] {
			continue
		}
		vs := c.Spec
		vs.NodeName = ""
		_, err := s.microvms.PutVolume(ctx, &api.Volume{
			TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindVolume},
			Metadata: api.ObjectMeta{Name: vol, Labels: map[string]string{ManagedByLabel: Manager, StatefulSetLabel: set}},
			Spec:     vs,
		})
		switch {
		case errors.Is(err, api.ErrAlreadyExists):
			s.log.Info("volume exists", "volume", vol)
		case err != nil:
			return fmt.Errorf("create volume %s: %w", vol, err)
		}
	}
	return nil
}
