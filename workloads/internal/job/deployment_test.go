package job_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

// depSpec is a Deployment spec of the replicas and image given, with the
// strategy JSON ("" for none).
func depSpec(replicas int, image, strategy string) string {
	if strategy != "" {
		strategy = `,"strategy":` + strategy
	}
	return fmt.Sprintf(`{"replicas":%d,"selector":{"matchLabels":{"app":"web"}},"template":{"image":%q,"resources":{},"runStrategy":"Never","nodeName":"stale","bindingEpoch":3}%s}`, replicas, image, strategy)
}

func depOwner(name string, uid api.UID) api.OwnerReference {
	return api.OwnerReference{APIVersion: job.APIVersion, Kind: job.KindDeployment, Name: name, UID: uid, Controller: true}
}

// settle runs sweeps, each preceded by an agent step, until the Deployment's
// replicas are the count, all made from the image and Ready. Every other step
// leaves the deleted MicroVMs in place, as a slow agent does.
func settle(t *testing.T, f *fakeAPI, clock *fakeClock, name string, replicas int, image string) {
	t.Helper()
	for i := range 80 {
		f.agentStep(i%2 == 0)
		clock.tick(t)
		f.agentStep(true)
		ms := f.replicasOf(name)
		if len(ms) != replicas {
			continue
		}
		done := true
		for _, m := range ms {
			done = done && m.Spec.Image == image && m.Status.Phase == api.MicroVMRunning
		}
		if done {
			return
		}
	}
	require.FailNow(t, "the rollout did not settle", "replicas: %v", f.replicasOf(name))
}

func TestDeployment_CreatesTheReplicasOwnedAndLabelled(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-web", depSpec(3, "alpine", ""))

	start(t, f)

	ms := f.replicasOf("web")
	require.Len(t, ms, 3, "one MicroVM per replica")
	for i, m := range ms {
		assert.Equal(t, fmt.Sprintf("web-%d", i), m.Metadata.Name)
		assert.Equal(t, []api.OwnerReference{depOwner("web", "uid-web")}, m.Metadata.OwnerReferences, "the reference carries the Deployment's uid")
		assert.Equal(t, "web", m.Metadata.Labels[job.DeploymentLabel])
		assert.Equal(t, "web", m.Metadata.Labels["app"], "the selector's labels are carried")
		assert.Equal(t, job.Manager, m.Metadata.Labels[job.ManagedByLabel])
		assert.NotEmpty(t, m.Metadata.Labels[job.TemplateHashLabel])
		assert.Equal(t, api.RunAlways, m.Spec.RunStrategy, "the template's runStrategy is replaced")
		assert.Equal(t, "alpine", m.Spec.Image, "the rest of the template is kept")
		assert.Empty(t, m.Spec.NodeName, "the template's binding is dropped")
		assert.Zero(t, m.Spec.BindingEpoch)
	}
}

func TestDeployment_ReplacesADeletedAndAFailedReplica(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-web", depSpec(3, "alpine", ""))
	clock := start(t, f)
	settle(t, f, clock, "web", 3, "alpine")

	kept := f.microvm("web-0").Metadata.ResourceVersion
	f.removeMicroVM("web-1")
	f.failMicroVM("web-2")
	settle(t, f, clock, "web", 3, "alpine")

	assert.ElementsMatch(t, []string{"web-0", "web-1", "web-2"}, f.microvmNames(), "the missing and the Failed replica are made again")
	assert.Equal(t, kept, f.microvm("web-0").Metadata.ResourceVersion, "the healthy replica is untouched")
	for _, m := range f.replicasOf("web") {
		assert.NotEqual(t, api.MicroVMFailed, m.Status.Phase, m.Metadata.Name)
	}
}

func TestDeployment_RemovesTheExtrasWhenReplicasDrop(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-web", depSpec(4, "alpine", ""))
	clock := start(t, f)
	settle(t, f, clock, "web", 4, "alpine")
	f.setDeploymentSpec("web", depSpec(2, "alpine", ""))

	settle(t, f, clock, "web", 2, "alpine")

	assert.ElementsMatch(t, []string{"web-0", "web-1"}, f.microvmNames(), "the highest names go first")
}

func TestDeployment_RemovesAReplicaThatIsNotReadyBeforeOnesThatAre(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-web", depSpec(1, "alpine", ""))
	hash := templateHashOf(t, f, depSpec(1, "alpine", ""))
	f.addReplica("web-0", "web", "uid-web", hash, false)
	f.addReplica("web-1", "web", "uid-web", hash, true)

	start(t, f)

	assert.Equal(t, []string{"web-0"}, f.terminating(), "the replica not Ready goes, though web-1 has the higher name")
}

// templateHashOf is the hash the service gives the replicas of a spec: the one
// it labels a first sweep's MicroVMs with.
func templateHashOf(t *testing.T, f *fakeAPI, spec string) string {
	t.Helper()
	g := newFakeAPI(t)
	g.addDeployment("probe", "uid-probe", spec)
	start(t, g)
	ms := g.replicasOf("probe")
	require.NotEmpty(t, ms)
	return ms[0].Metadata.Labels[job.TemplateHashLabel]
}

func TestDeployment_ARolloutKeepsToTheSurgeAndUnavailableBounds(t *testing.T) {
	tests := []struct {
		name     string
		replicas int
		strategy string
		surge    int
		unavail  int
	}{
		{"one surge none unavailable", 4, `{"type":"RollingUpdate","rollingUpdate":{"maxSurge":"1","maxUnavailable":"0"}}`, 1, 0},
		{"none surge one unavailable", 4, `{"type":"RollingUpdate","rollingUpdate":{"maxSurge":"0","maxUnavailable":"1"}}`, 0, 1},
		{"two surge two unavailable", 5, `{"rollingUpdate":{"maxSurge":"2","maxUnavailable":"2"}}`, 2, 2},
		{"percentages round surge up and unavailable down", 5, `{"rollingUpdate":{"maxSurge":"50%","maxUnavailable":"30%"}}`, 3, 1},
		{"default of a quarter", 8, "", 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addDeployment("web", "uid-web", depSpec(tt.replicas, "alpine:1", tt.strategy))
			clock := start(t, f)
			settle(t, f, clock, "web", tt.replicas, "alpine:1")
			bounds := f.boundReplicas("web", tt.replicas+tt.surge, tt.replicas-tt.unavail)

			f.setDeploymentSpec("web", depSpec(tt.replicas, "alpine:2", tt.strategy))
			settle(t, f, clock, "web", tt.replicas, "alpine:2")

			f.mu.Lock()
			defer f.mu.Unlock()
			assert.Equal(t, tt.replicas+tt.surge, bounds.mostPresent, "the rollout used the whole surge")
			assert.Equal(t, tt.replicas-tt.unavail, bounds.fewestReady, "the rollout used the whole unavailability")
		})
	}
}

func TestDeployment_RecreateRemovesEveryReplicaBeforeCreatingAny(t *testing.T) {
	f := newFakeAPI(t)
	spec := func(image string) string { return depSpec(3, image, `{"type":"Recreate"}`) }
	f.addDeployment("web", "uid-web", spec("alpine:1"))
	clock := start(t, f)
	settle(t, f, clock, "web", 3, "alpine:1")
	f.boundReplicas("web", 3, 0)

	f.setDeploymentSpec("web", spec("alpine:2"))
	clock.tick(t)

	assert.Len(t, f.terminating(), 3, "every old replica is being deleted")
	for _, m := range f.replicasOf("web") {
		assert.Equal(t, "alpine:1", m.Spec.Image, "no new replica is created while an old one exists")
	}

	f.agentStep(false)
	clock.tick(t)
	for _, m := range f.replicasOf("web") {
		assert.Equal(t, "alpine:1", m.Spec.Image, "still none while the deleted replicas are present")
	}

	settle(t, f, clock, "web", 3, "alpine:2")
}

func TestDeployment_WritesTheStatusAndTheGenerationItObserved(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-web", depSpec(2, "alpine:1", ""))
	clock := start(t, f)

	s := f.deploymentStatus("web")
	assert.Equal(t, job.DeploymentStatus{ObservedGeneration: 1, Replicas: 0}, s, "the counts are of the replicas before the sweep's step")

	settle(t, f, clock, "web", 2, "alpine:1")
	clock.tick(t)
	assert.Equal(t, job.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}, f.deploymentStatus("web"))

	f.setDeploymentSpec("web", depSpec(2, "alpine:2", `{"rollingUpdate":{"maxSurge":"1","maxUnavailable":"0"}}`))
	clock.tick(t)
	s = f.deploymentStatus("web")
	assert.Equal(t, int64(2), s.ObservedGeneration, "the generation of the spec the sweep read")
	assert.Equal(t, int32(2), s.Replicas)
	assert.Equal(t, int32(0), s.UpdatedReplicas, "no replica is of the new template yet")

	settle(t, f, clock, "web", 2, "alpine:2")
	clock.tick(t)
	assert.Equal(t, job.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}, f.deploymentStatus("web"))
}

func TestDeployment_LeavesAnUnusableSpecAloneAndServesTheOthers(t *testing.T) {
	tests := map[string]string{
		"empty selector":  `{"replicas":2,"selector":{"matchLabels":{}},"template":{"image":"alpine","resources":{}}}`,
		"bad bound":       depSpec(2, "alpine", `{"rollingUpdate":{"maxSurge":"lots"}}`),
		"unknown type":    depSpec(2, "alpine", `{"type":"BlueGreen"}`),
		"undecodable":     `{"replicas":"two"}`,
		"percent too big": depSpec(2, "alpine", `{"rollingUpdate":{"maxSurge":"1000000000000%"}}`),
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addDeployment("bad", "uid-bad", spec)
			f.addDeployment("web", "uid-web", depSpec(1, "alpine", ""))

			start(t, f)

			assert.Empty(t, f.replicasOf("bad"), "no MicroVM is created for it")
			assert.Len(t, f.replicasOf("web"), 1, "the other Deployment is served")
		})
	}
}

func TestDeployment_CountsOnlyTheMicroVMsItOwnsByUid(t *testing.T) {
	f := newFakeAPI(t)
	f.addDeployment("web", "uid-new", depSpec(2, "alpine", ""))
	hash := templateHashOf(t, f, depSpec(2, "alpine", ""))
	f.addReplica("web-0", "web", "uid-old", hash, true)

	start(t, f)

	owned := 0
	for _, m := range f.replicasOf("web") {
		if m.Metadata.OwnerReferences[0].UID == "uid-new" {
			owned++
		}
	}
	assert.Equal(t, 2, owned, "a MicroVM of a Deployment of the same name and another uid is not a replica")
	assert.NotNil(t, f.microvm("web-0"), "and it is left in place")
	assert.False(t, f.microvm("web-0").Metadata.DeletionTimestamp != nil)
}

func TestDeployment_KeepsASurgeReplicaThatIsNotYetReady(t *testing.T) {
	f := newFakeAPI(t)
	spec := func(image string) string {
		return depSpec(3, image, `{"rollingUpdate":{"maxSurge":"1","maxUnavailable":"0"}}`)
	}
	f.addDeployment("web", "uid-web", spec("alpine:1"))
	clock := start(t, f)
	settle(t, f, clock, "web", 3, "alpine:1")

	f.setDeploymentSpec("web", spec("alpine:2"))
	clock.tick(t)
	clock.tick(t)
	clock.tick(t)

	assert.Len(t, f.replicasOf("web"), 4, "the surge replica and the three old ones")
	assert.Empty(t, f.terminating(), "nothing is removed while the new replica is not Ready: that would leave fewer than replicas Ready")
	assert.Equal(t, "alpine:2", f.microvm("web-3").Spec.Image)
}

func TestBoundPattern_AcceptsANumberOrAPercentage(t *testing.T) {
	re := regexp.MustCompile(job.BoundPattern)
	for _, ok := range []string{"0", "1", "25", "100%", "0%", "999999999"} {
		assert.True(t, re.MatchString(ok), ok)
	}
	for _, bad := range []string{"", "-1", "01", "1.5", "5%%", "%", "x", "1234567890", " 1"} {
		assert.False(t, re.MatchString(bad), bad)
	}
}
