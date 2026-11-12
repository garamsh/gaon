package job_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

func cronSpec(schedule string, historyLimit int) string {
	return fmt.Sprintf(`{"schedule":%q,"jobTemplate":%s,"historyLimit":%d}`, schedule, jobSpec(2, 1), historyLimit)
}

func lastSchedule(ts time.Time) string {
	return fmt.Sprintf(`{"lastScheduleTime":%q}`, ts.Format(time.RFC3339))
}

// minuteOf is the Unix minute a Job of a CronJob is named and labeled for.
func minuteOf(ts time.Time) int { return int(ts.Unix() / 60) }

func jobFor(cron string, ts time.Time) string { return cron + "-" + strconv.Itoa(minuteOf(ts)) }

// clockAt returns a fake clock reading the time given.
func clockAt(s string) *fakeClock {
	c := newFakeClock()
	c.set(utc(s))
	return c
}

func TestRun_CountsANewCronJobsScheduleFromTheSweepThatFirstSeesIt(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), "")
	clock := clockAt("2026-11-13 12:00:30")

	startWith(t, f, clock)

	assert.Empty(t, f.jobNames(), "the minute that began before the CronJob was seen is not run")
	assert.Equal(t, utc("2026-11-13 12:00:30"), *f.cronStatus("snap").LastScheduleTime, "the status records when the unit saw it")

	// The control: the next minute runs.
	clock.set(utc("2026-11-13 12:01:10"))
	clock.tick(t)
	assert.Equal(t, []string{jobFor("snap", utc("2026-11-13 12:01:00"))}, f.jobNames())
}

func TestRun_CreatesTheJobOfAScheduledTimeFromTheTemplateOwnedByTheCronJob(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))
	clock := clockAt("2026-11-13 12:00:59")
	startWith(t, f, clock)
	require.Empty(t, f.jobNames(), "setup: the time of 12:01 has not come")

	clock.set(utc("2026-11-13 12:01:05"))
	clock.tick(t)

	name := jobFor("snap", utc("2026-11-13 12:01:00"))
	require.Equal(t, []string{name}, f.jobNames(), "one Job for the time")
	created := f.storedJob(name)
	assert.Equal(t, []api.OwnerReference{{APIVersion: job.APIVersion, Kind: job.KindCronJob, Name: "snap", UID: "uid-snap", Controller: true}}, created.Metadata.OwnerReferences)
	assert.Equal(t, map[string]string{
		job.ManagedByLabel: job.Manager, job.CronJobLabel: "snap", job.ScheduledLabel: strconv.Itoa(minuteOf(utc("2026-11-13 12:01:00"))),
	}, created.Metadata.Labels)
	var spec job.Spec
	require.NoError(t, json.Unmarshal(created.Spec, &spec))
	assert.Equal(t, int32(2), spec.Completions, "the Job carries the template's spec")
	assert.Equal(t, int32(1), spec.BackoffLimit)
	assert.Equal(t, "alpine", spec.Template.Image)
	assert.Equal(t, utc("2026-11-13 12:01:00"), *f.cronStatus("snap").LastScheduleTime)

	clock.tick(t)
	assert.Equal(t, []string{name}, f.jobNames(), "a time that was run is not run again")
}

func TestRun_RunsOnlyTheLatestOfTheTimesMissedWhileNotLeading(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))

	startWith(t, f, clockAt("2026-11-13 12:05:30"))

	assert.Equal(t, []string{jobFor("snap", utc("2026-11-13 12:05:00"))}, f.jobNames(), "five times were missed and one Job runs")
	assert.Equal(t, utc("2026-11-13 12:05:00"), *f.cronStatus("snap").LastScheduleTime, "the missed ones are not owed")
}

func TestRun_SkipsATimeWhileTheJobOfAnEarlierOneIsUnfinished(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))
	running := f.addScheduledJob("snap", "uid-snap", minuteOf(utc("2026-11-13 12:00:00")), false)
	clock := clockAt("2026-11-13 12:01:10")

	startWith(t, f, clock)

	assert.Equal(t, []string{running}, f.jobNames(), "the previous Job is unfinished, so 12:01 starts none")
	assert.Equal(t, utc("2026-11-13 12:01:00"), *f.cronStatus("snap").LastScheduleTime, "the skipped time is not owed")

	// The control: the same CronJob runs the next time once the Job is finished.
	f.finishJob(running)
	clock.set(utc("2026-11-13 12:02:10"))
	clock.tick(t)
	assert.Contains(t, f.jobNames(), jobFor("snap", utc("2026-11-13 12:02:00")))
}

func TestRun_AJobOfAnotherCronJobDoesNotHoldBackARun(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))
	f.addScheduledJob("snap", "uid-old-snap", minuteOf(utc("2026-11-13 11:00:00")), false)
	f.addScheduledJob("other", "uid-other", minuteOf(utc("2026-11-13 11:30:00")), false)

	startWith(t, f, clockAt("2026-11-13 12:01:10"))

	assert.Contains(t, f.jobNames(), jobFor("snap", utc("2026-11-13 12:01:00")), "only a Job whose owner has the CronJob's uid counts")
}

func TestRun_AnswersAJobThatAlreadyExistsForTheTimeAsDone(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))
	// A creator that stopped before it wrote the status left the Job behind.
	existing := f.addScheduledJob("snap", "uid-snap", minuteOf(utc("2026-11-13 12:01:00")), true)

	startWith(t, f, clockAt("2026-11-13 12:01:10"))

	assert.Equal(t, []string{existing}, f.jobNames(), "the time is not run twice")
	assert.Equal(t, utc("2026-11-13 12:01:00"), *f.cronStatus("snap").LastScheduleTime, "the time is recorded")
}

func TestRun_KeepsTheNewestFinishedJobsUpToTheHistoryLimit(t *testing.T) {
	f := newFakeAPI(t)
	now := utc("2026-11-13 12:00:10")
	f.addCronJob("snap", "uid-snap", cronSpec("0 0 * * *", 2), lastSchedule(utc("2026-11-13 12:00:00")))
	// Created out of order, so that the oldest is not the first stored.
	var done [4]string
	for _, i := range []int{2, 0, 3, 1} {
		done[i] = f.addScheduledJob("snap", "uid-snap", minuteOf(now)-100+i*10, true)
	}
	running := f.addScheduledJob("snap", "uid-snap", minuteOf(now)-500, false)
	others := f.addScheduledJob("other", "uid-other", minuteOf(now)-600, true)

	startWith(t, f, clockAt("2026-11-13 12:00:10"))

	assert.ElementsMatch(t, []string{done[2], done[3], running, others}, f.jobNames(),
		"the two oldest finished Jobs are deleted; the newest two, an unfinished one and another CronJob's are kept")
}

func TestRun_KeepsThreeFinishedJobsWhenTheSpecSetsNoLimit(t *testing.T) {
	f := newFakeAPI(t)
	now := utc("2026-11-13 12:00:10")
	spec := fmt.Sprintf(`{"schedule":"0 0 * * *","jobTemplate":%s}`, jobSpec(1, 0))
	f.addCronJob("snap", "uid-snap", spec, lastSchedule(utc("2026-11-13 12:00:00")))
	for i := 0; i < 5; i++ {
		f.addScheduledJob("snap", "uid-snap", minuteOf(now)-100+i*10, true)
	}

	startWith(t, f, clockAt("2026-11-13 12:00:10"))

	assert.Len(t, f.jobNames(), job.DefaultHistoryLimit)
}

func TestRun_LeavesACronJobWhoseScheduleItCannotReadAlone(t *testing.T) {
	f := newFakeAPI(t)
	f.addCronJob("snap", "uid-snap", cronSpec("every minute", 0), lastSchedule(utc("2026-11-13 12:00:00")))
	f.addCronJob("good", "uid-good", cronSpec("* * * * *", 0), lastSchedule(utc("2026-11-13 12:00:00")))

	startWith(t, f, clockAt("2026-11-13 12:01:10"))

	assert.Equal(t, []string{jobFor("good", utc("2026-11-13 12:01:00"))}, f.jobNames(), "the unreadable schedule runs nothing and does not stop the other")
}
