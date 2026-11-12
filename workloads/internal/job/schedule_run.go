package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// sweepCronJobs reconciles every CronJob once. jobs is every Job the sweep
// listed. A failure of one CronJob is logged and does not stop the others.
func (s *service) sweepCronJobs(ctx context.Context, jobs []api.CustomObject) {
	list, err := s.cronjobs.List(ctx)
	if err != nil {
		s.log.Warn("list cronjobs", "error", err)
		return
	}
	for i := range list.Items {
		if err := s.runCronJob(ctx, &list.Items[i], jobs); err != nil {
			s.log.Warn("reconcile cronjob", "cronjob", list.Items[i].Metadata.Name, "error", err)
		}
	}
}

// runCronJob creates the Job of the latest scheduled time that has passed since
// the last one dealt with, then trims the finished Jobs to the history limit.
// A time that passes while the unit does not lead is not caught up one by one:
// only the latest is run. A time at which a Job of the CronJob is unfinished is
// skipped. A CronJob seen for the first time counts from now.
func (s *service) runCronJob(ctx context.Context, obj *api.CustomObject, jobs []api.CustomObject) error {
	var spec CronJobSpec
	if err := json.Unmarshal(obj.Spec, &spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}
	var status CronJobStatus
	if len(obj.Status) > 0 {
		if err := json.Unmarshal(obj.Status, &status); err != nil {
			return fmt.Errorf("decode status: %w", err)
		}
	}
	schedule, err := ParseSchedule(spec.Schedule)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	mine := cronJobsOwned(obj, jobs)

	if status.LastScheduleTime == nil {
		return s.writeScheduled(ctx, obj, now)
	}
	due, ok := schedule.LatestDue(*status.LastScheduleTime, now)
	if !ok {
		return s.trim(ctx, mine, spec.HistoryLimit)
	}
	if slices.ContainsFunc(mine, unfinished) {
		s.log.Info("skipped a run: the previous Job is unfinished", "cronjob", obj.Metadata.Name, "scheduled", due)
	} else if err := s.createScheduled(ctx, obj, spec, due); err != nil {
		return err
	}
	return errors.Join(s.writeScheduled(ctx, obj, due), s.trim(ctx, mine, spec.HistoryLimit))
}

// cronJobsOwned returns the Jobs that name the CronJob, by uid, as their owner,
// leaving out those being deleted.
func cronJobsOwned(cron *api.CustomObject, all []api.CustomObject) []api.CustomObject {
	var out []api.CustomObject
	for _, j := range all {
		if j.Metadata.DeletionTimestamp != nil {
			continue
		}
		for _, ref := range j.Metadata.OwnerReferences {
			if ref.APIVersion == APIVersion && ref.Kind == KindCronJob && ref.Name == cron.Metadata.Name && ref.UID == cron.Metadata.UID {
				out = append(out, j)
				break
			}
		}
	}
	return out
}

// unfinished reports whether the Job has no True condition yet.
func unfinished(j api.CustomObject) bool {
	var st Status
	if len(j.Status) > 0 && json.Unmarshal(j.Status, &st) != nil {
		return true
	}
	return !st.finished()
}

// createScheduled creates the Job for the scheduled time, named for the
// minute, so that a second creator of the same time is answered AlreadyExists.
func (s *service) createScheduled(ctx context.Context, cron *api.CustomObject, spec CronJobSpec, at time.Time) error {
	raw, err := json.Marshal(spec.JobTemplate)
	if err != nil {
		return err
	}
	minute := strconv.FormatInt(at.Unix()/60, 10)
	name := cron.Metadata.Name + "-" + minute
	_, err = s.jobs.Put(ctx, &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: APIVersion, Kind: Kind},
		Metadata: api.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ManagedByLabel: Manager, CronJobLabel: cron.Metadata.Name, ScheduledLabel: minute},
			OwnerReferences: []api.OwnerReference{{
				APIVersion: APIVersion, Kind: KindCronJob, Name: cron.Metadata.Name, UID: cron.Metadata.UID, Controller: true,
			}},
		},
		Spec: raw,
	})
	if errors.Is(err, api.ErrAlreadyExists) {
		s.log.Info("job exists", "job", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("create job %s: %w", name, err)
	}
	return nil
}

// writeScheduled records the scheduled time dealt with.
func (s *service) writeScheduled(ctx context.Context, cron *api.CustomObject, at time.Time) error {
	raw, err := json.Marshal(CronJobStatus{LastScheduleTime: &at})
	if err != nil {
		return err
	}
	_, err = s.cronjobs.PutStatus(ctx, &api.CustomObject{TypeMeta: cron.TypeMeta, Metadata: cron.Metadata, Status: raw})
	return err
}

// trim deletes the oldest finished Jobs beyond limit, oldest scheduled first.
func (s *service) trim(ctx context.Context, mine []api.CustomObject, limit int32) error {
	if limit == 0 {
		limit = DefaultHistoryLimit
	}
	var done []api.CustomObject
	for _, j := range mine {
		if !unfinished(j) {
			done = append(done, j)
		}
	}
	slices.SortFunc(done, func(a, b api.CustomObject) int {
		return scheduledMinute(a) - scheduledMinute(b)
	})
	var errs []error
	for _, j := range done[:max(0, len(done)-int(limit))] {
		_, err := s.jobs.Delete(ctx, j.Metadata.Name, j.Metadata.ResourceVersion)
		if err != nil && !errors.Is(err, api.ErrNotFound) {
			errs = append(errs, fmt.Errorf("delete job %s: %w", j.Metadata.Name, err))
		}
	}
	return errors.Join(errs...)
}

// scheduledMinute returns the minute the Job was scheduled for, or 0.
func scheduledMinute(j api.CustomObject) int {
	n, _ := strconv.Atoi(j.Metadata.Labels[ScheduledLabel])
	return n
}
