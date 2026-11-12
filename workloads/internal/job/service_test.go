package job_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

// start runs the service against f until the test ends and returns once its
// first sweep has finished, with the clock that starts the next.
func start(t *testing.T, f *fakeAPI) *fakeClock {
	t.Helper()
	clock := newFakeClock()
	svc := job.NewService(f.definitionsClient(), f.jobsClient(), f.microvmsClient(), clock, job.Config{Interval: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
	})
	clock.sweepsAbove(t, 0)
	return clock
}

func jobSpec(completions, backoffLimit int) string {
	return fmt.Sprintf(`{"completions":%d,"backoffLimit":%d,"template":{"image":"alpine","resources":{},"runStrategy":"Always","nodeName":"stale","bindingEpoch":3}}`, completions, backoffLimit)
}

func ownerOf(name string, uid api.UID) api.OwnerReference {
	return api.OwnerReference{APIVersion: job.APIVersion, Kind: job.Kind, Name: name, UID: uid, Controller: true}
}

// condition returns the True condition of the status, or nil.
func condition(s job.Status) *api.Condition {
	for _, c := range s.Conditions {
		if c.Status == api.ConditionTrue {
			return &c
		}
	}
	return nil
}

func TestRun_RegistersTheDefinitionOnlyWhenItDiffers(t *testing.T) {
	f := newFakeAPI(t)
	require.Nil(t, f.definition("jobs."+job.Group), "setup: no definition is stored")

	start(t, f)

	d := f.definition("jobs." + job.Group)
	require.NotNil(t, d, "the service registers the Job kind")
	assert.Equal(t, job.Group, d.Spec.Group)
	assert.Equal(t, job.Kind, d.Spec.Names.Kind)
	assert.Equal(t, 1, f.definitionWrites)
	require.NoError(t, d.Validate(), "the registered definition is one the API server accepts")

	// The control: a second start finds the stored definition equal and writes nothing.
	start(t, f)
	assert.Equal(t, 1, f.definitionWrites, "a matching definition is not rewritten")
}

func TestRun_UpdatesAStoredDefinitionThatDiffers(t *testing.T) {
	f := newFakeAPI(t)
	stale := job.Definition()
	stale.Spec.Versions[0].Schema.OpenAPIV3Schema.Description = "an older schema"
	stale.Metadata.ResourceVersion = "7"
	f.definitions[stale.Metadata.Name] = stale
	require.Equal(t, 0, f.definitionWrites, "setup: nothing written yet")

	start(t, f)

	assert.Equal(t, 1, f.definitionWrites)
	assert.Empty(t, f.definition(stale.Metadata.Name).Spec.Versions[0].Schema.OpenAPIV3Schema.Description)
}

func TestRun_CreatesAMicroVMOwnedByTheJobThatRunsOnce(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-build", jobSpec(1, 0))

	start(t, f)

	m := f.microvm("build-0")
	require.NotNil(t, m, "the Job's first MicroVM is created")
	assert.Equal(t, []api.OwnerReference{ownerOf("build", "uid-build")}, m.Metadata.OwnerReferences, "the reference carries the Job's uid")
	assert.Equal(t, api.RunOnce, m.Spec.RunStrategy, "the template's Always is replaced")
	assert.Equal(t, "alpine", m.Spec.Image, "the rest of the template is kept")
	assert.Empty(t, m.Spec.NodeName, "the template's binding is dropped")
	assert.Zero(t, m.Spec.BindingEpoch)
	assert.Equal(t, map[string]string{job.ManagedByLabel: job.Manager, job.JobLabel: "build"}, m.Metadata.Labels)
	assert.Equal(t, int32(1), f.status("build").Active)
}

func TestRun_CreatesNoSecondMicroVMWhileOneIsRunning(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-build", jobSpec(3, 0))
	clock := start(t, f)
	require.Equal(t, []string{"build-0"}, f.microvmNames(), "setup: the first MicroVM exists")

	clock.tick(t)
	f.setPhase("build-0", api.MicroVMRunning)
	clock.tick(t)
	assert.Equal(t, []string{"build-0"}, f.microvmNames(), "a running MicroVM keeps the Job from starting another")

	// The control: once it has succeeded the next is created.
	f.setPhase("build-0", api.MicroVMSucceeded)
	clock.tick(t)
	assert.ElementsMatch(t, []string{"build-0", "build-1"}, f.microvmNames())
}

func TestRun_CompletesOnlyWhenTheCompletionsHaveSucceeded(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-build", jobSpec(2, 0))
	clock := start(t, f)

	f.setPhase("build-0", api.MicroVMSucceeded)
	clock.tick(t)
	s := f.status("build")
	assert.Equal(t, int32(1), s.Succeeded)
	assert.Nil(t, condition(s), "one of two succeeded: the Job is not complete")
	require.ElementsMatch(t, []string{"build-0", "build-1"}, f.microvmNames(), "the second MicroVM is created")

	f.setPhase("build-1", api.MicroVMRunning)
	clock.tick(t)
	assert.Nil(t, condition(f.status("build")), "a running MicroVM does not complete the Job")

	f.setPhase("build-1", api.MicroVMSucceeded)
	clock.tick(t)
	s = f.status("build")
	assert.Equal(t, int32(2), s.Succeeded)
	assert.Zero(t, s.Active)
	c := condition(s)
	require.NotNil(t, c, "two of two succeeded: the Job is complete")
	assert.Equal(t, job.CondComplete, c.Type)
	assert.Len(t, f.microvmNames(), 2, "a complete Job creates no more")
}

func TestRun_RetriesAFailureUpToTheLimitThenFails(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("flaky", "uid-flaky", jobSpec(1, 1))
	clock := start(t, f)

	f.setPhase("flaky-0", api.MicroVMFailed)
	clock.tick(t)
	s := f.status("flaky")
	assert.Equal(t, int32(1), s.Failed)
	assert.Nil(t, condition(s), "one failure is within a limit of one")
	require.ElementsMatch(t, []string{"flaky-0", "flaky-1"}, f.microvmNames(), "the failed run is retried")

	f.setPhase("flaky-1", api.MicroVMFailed)
	clock.tick(t)
	s = f.status("flaky")
	assert.Equal(t, int32(2), s.Failed)
	c := condition(s)
	require.NotNil(t, c, "a second failure passes the limit")
	assert.Equal(t, job.CondFailed, c.Type)
	assert.Equal(t, job.ReasonBackoffLimitExceeded, c.Reason)
	assert.Len(t, f.microvmNames(), 2, "a failed Job starts no more")
}

func TestRun_CountsOnlyTheMicroVMsTheJobOwnsByUid(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-new", jobSpec(1, 0))
	f.addJob("other", "uid-other", jobSpec(1, 0))
	f.addMicroVM("build-old", ownerOf("build", "uid-old"), api.MicroVMSucceeded)
	f.addMicroVM("other-0", ownerOf("other", "uid-other"), api.MicroVMSucceeded)
	require.Equal(t, 2, len(f.microvmNames()), "setup: two succeeded MicroVMs belong to other owners")

	start(t, f)

	s := f.status("build")
	assert.Zero(t, s.Succeeded, "neither the old Job's MicroVM nor another Job's counts")
	assert.Nil(t, condition(s))
	assert.NotNil(t, f.microvm("build-0"), "the Job runs its own MicroVM")

	// The control: the MicroVM that does carry the Job's uid completes it.
	c := condition(f.status("other"))
	require.NotNil(t, c, "other-0 completes the Job that owns it")
	assert.Equal(t, job.CondComplete, c.Type)
}

func TestRun_ReadsAStatusOfAnotherEpochAsNotEnded(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-build", jobSpec(1, 0))
	f.addMicroVM("build-0", ownerOf("build", "uid-build"), api.MicroVMSucceeded)
	f.microvms["build-0"].Spec.BindingEpoch = 2
	require.Equal(t, api.MicroVMPending, f.microvms["build-0"].EffectivePhase(), "setup: the status is void")

	start(t, f)

	s := f.status("build")
	assert.Zero(t, s.Succeeded, "a void status does not count")
	assert.Equal(t, int32(1), s.Active)
}

func TestRun_FailsAJobWhoseTemplateTheAPIRefuses(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("bad", "uid-bad", jobSpec(1, 5))
	f.invalidMicroVM = true

	start(t, f)

	c := condition(f.status("bad"))
	require.NotNil(t, c)
	assert.Equal(t, job.ReasonInvalidTemplate, c.Reason, "a refused template is not retried within the limit")
}

func TestRun_LeavesAFinishedJobAlone(t *testing.T) {
	f := newFakeAPI(t)
	f.addJob("build", "uid-build", jobSpec(1, 0))
	clock := start(t, f)
	f.setPhase("build-0", api.MicroVMSucceeded)
	clock.tick(t)
	require.NotNil(t, condition(f.status("build")), "setup: the Job is complete")
	writes := f.statusWrites

	clock.tick(t)
	clock.tick(t)

	assert.Equal(t, writes, f.statusWrites, "a finished Job's status is not rewritten")
	assert.Len(t, f.microvmNames(), 1)
}
