package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
)

// sweep reconciles every Job once. A failure of one Job is logged and does
// not stop the others.
func (s *service) sweep(ctx context.Context) {
	jobs, err := s.jobs.List(ctx)
	if err != nil {
		s.log.Warn("list jobs", "error", err)
		return
	}
	if len(jobs.Items) == 0 {
		return
	}
	mvms, err := s.microvms.ListMicroVMs(ctx)
	if err != nil {
		s.log.Warn("list microvms", "error", err)
		return
	}
	for i := range jobs.Items {
		if err := s.reconcile(ctx, &jobs.Items[i], mvms.Items); err != nil {
			s.log.Warn("reconcile", "job", jobs.Items[i].Metadata.Name, "error", err)
		}
	}
}

// reconcile brings one Job forward by at most one MicroVM. all is every
// MicroVM the unit wrote.
func (s *service) reconcile(ctx context.Context, obj *api.CustomObject, all []api.MicroVM) error {
	var spec Spec
	if err := json.Unmarshal(obj.Spec, &spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}
	var old Status
	if len(obj.Status) > 0 {
		if err := json.Unmarshal(obj.Status, &old); err != nil {
			return fmt.Errorf("decode status: %w", err)
		}
	}
	if old.finished() {
		return nil
	}

	next := Status{Conditions: old.Conditions}
	for _, m := range owned(obj, all) {
		switch m.EffectivePhase() {
		case api.MicroVMSucceeded:
			next.Succeeded++
		case api.MicroVMFailed:
			next.Failed++
		default:
			next.Active++
		}
	}

	var createErr error
	switch {
	case next.Succeeded >= spec.Completions:
		next.Conditions = append(next.Conditions, s.condition(CondComplete, "CompletionsReached", fmt.Sprintf("%d succeeded", next.Succeeded)))
	case next.Failed > spec.BackoffLimit:
		next.Conditions = append(next.Conditions, s.condition(CondFailed, ReasonBackoffLimitExceeded, fmt.Sprintf("%d failed, limit %d", next.Failed, spec.BackoffLimit)))
	case next.Active == 0:
		createErr = s.create(ctx, obj, spec, all)
		switch {
		case createErr == nil:
			next.Active++
		case errors.Is(createErr, api.ErrInvalid):
			next.Conditions = append(next.Conditions, s.condition(CondFailed, ReasonInvalidTemplate, createErr.Error()))
			createErr = nil
		}
	}

	if reflect.DeepEqual(old, next) {
		return createErr
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	_, err = s.jobs.PutStatus(ctx, &api.CustomObject{TypeMeta: obj.TypeMeta, Metadata: obj.Metadata, Status: raw})
	return errors.Join(createErr, err)
}

// owned returns the MicroVMs that name the Job, by uid, as their owner. A
// recreated Job of the same name does not own the MicroVMs of the old one.
func owned(job *api.CustomObject, all []api.MicroVM) []api.MicroVM {
	var out []api.MicroVM
	for _, m := range all {
		for _, ref := range m.Metadata.OwnerReferences {
			if ref.APIVersion == APIVersion && ref.Kind == Kind && ref.Name == job.Metadata.Name && ref.UID == job.Metadata.UID {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// create writes the next MicroVM of the Job, owned by it and named by the
// first index no listed MicroVM uses.
func (s *service) create(ctx context.Context, job *api.CustomObject, spec Spec, all []api.MicroVM) error {
	taken := make(map[string]bool, len(all))
	for _, m := range all {
		taken[m.Metadata.Name] = true
	}
	name := job.Metadata.Name + "-0"
	for i := 1; taken[name]; i++ {
		name = job.Metadata.Name + "-" + strconv.Itoa(i)
	}
	tmpl := spec.Template
	tmpl.RunStrategy = api.RunOnce
	tmpl.NodeName, tmpl.BindingEpoch = "", 0
	_, err := s.microvms.PutMicroVM(ctx, &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ManagedByLabel: Manager, JobLabel: job.Metadata.Name},
			OwnerReferences: []api.OwnerReference{{
				APIVersion: APIVersion, Kind: Kind, Name: job.Metadata.Name, UID: job.Metadata.UID, Controller: true,
			}},
		},
		Spec: tmpl,
	})
	if err != nil {
		return fmt.Errorf("create microvm %s: %w", name, err)
	}
	return nil
}

func (s *service) condition(typ, reason, message string) api.Condition {
	now := s.clock.Now()
	return api.Condition{Type: typ, Status: api.ConditionTrue, Reason: reason, Message: message, LastTransitionTime: &now}
}
