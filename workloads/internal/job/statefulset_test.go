package job_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

// ssOpts is what a test varies in a StatefulSet spec.
type ssOpts struct {
	replicas int
	image    string
	policy   string // podManagementPolicy, "" for none
	update   string // updateStrategy JSON, "" for none
	claims   bool   // one claim template, "data", mounted at /data
}

func (o ssOpts) spec() string {
	if o.image == "" {
		o.image = "alpine"
	}
	volumes, claims := "", ""
	if o.claims {
		volumes = `,"volumes":[{"name":"data","mountPath":"/data"}]`
		claims = `,"volumeClaimTemplates":[{"name":"data","spec":{"storageClass":"local","accessModes":["ReadWriteOnce"],"storage":"1Gi"}}]`
	}
	extra := ""
	if o.policy != "" {
		extra += fmt.Sprintf(`,"podManagementPolicy":%q`, o.policy)
	}
	if o.update != "" {
		extra += `,"updateStrategy":` + o.update
	}
	return fmt.Sprintf(`{"replicas":%d,"selector":{"matchLabels":{"app":"db"}},"template":{"image":%q,"resources":{},"runStrategy":"Never","nodeName":"stale","bindingEpoch":3%s}%s%s}`,
		o.replicas, o.image, volumes, claims, extra)
}

// replicaNames returns the names of the MicroVMs the StatefulSet owns, in ordinal order.
func replicaNames(f *fakeAPI, set string) []string {
	var out []string
	for _, m := range f.replicasOf(set) {
		out = append(out, m.Metadata.Name)
	}
	return out
}

func TestStatefulSet_MakesEachReplicaWithANameAnIdentityAndAVolume(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 2, policy: "Parallel", claims: true}.spec())

	start(t, f)

	ms := f.replicasOf("db")
	require.Len(t, ms, 2)
	for i, m := range ms {
		name := fmt.Sprintf("db-%d", i)
		assert.Equal(t, name, m.Metadata.Name)
		assert.Equal(t, []api.OwnerReference{{APIVersion: job.APIVersion, Kind: job.KindStatefulSet, Name: "db", UID: "uid-db", Controller: true}}, m.Metadata.OwnerReferences)
		assert.Equal(t, "db", m.Metadata.Labels[job.StatefulSetLabel])
		assert.Equal(t, fmt.Sprint(i), m.Metadata.Labels[job.OrdinalLabel])
		assert.Equal(t, name, m.Metadata.Labels[job.ReplicaNameLabel])
		assert.Equal(t, "db", m.Metadata.Labels["app"], "the selector's labels are carried")
		assert.Equal(t, job.Manager, m.Metadata.Labels[job.ManagedByLabel])
		assert.NotEmpty(t, m.Metadata.Labels[job.TemplateHashLabel])
		assert.Equal(t, api.RunAlways, m.Spec.RunStrategy)
		assert.Empty(t, m.Spec.NodeName)
		assert.Zero(t, m.Spec.BindingEpoch)
		assert.Equal(t, []api.MicroVMVolume{{Name: fmt.Sprintf("data-db-%d", i), MountPath: "/data"}}, m.Spec.Volumes, "the mount names this replica's Volume")
	}
	assert.Equal(t, []string{"data-db-0", "data-db-1"}, f.volumeNames())
	v := f.volume("data-db-1")
	assert.Equal(t, "local", v.Spec.StorageClass)
	assert.Equal(t, []string{"ReadWriteOnce"}, v.Spec.AccessModes)
	assert.Equal(t, api.Quantity("1Gi"), v.Spec.Storage)
	assert.Equal(t, api.KindVolume, v.Kind)
	assert.Equal(t, map[string]string{job.ManagedByLabel: job.Manager, job.StatefulSetLabel: "db"}, v.Metadata.Labels)
	assert.Empty(t, v.Metadata.OwnerReferences, "the garbage collector must not remove a Volume with its StatefulSet")
}

func TestStatefulSet_OrderedReadyMakesReplicaIOnlyAfterReplicaIBeforeItIsReady(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 3}.spec())

	clock := start(t, f)
	assert.Equal(t, []string{"db-0"}, replicaNames(f, "db"), "only the first is made")

	clock.tick(t)
	assert.Equal(t, []string{"db-0"}, replicaNames(f, "db"), "db-1 waits while db-0 is not Ready")

	f.makeReady("db-0")
	clock.tick(t)
	assert.Equal(t, []string{"db-0", "db-1"}, replicaNames(f, "db"))
	clock.tick(t)
	assert.Equal(t, []string{"db-0", "db-1"}, replicaNames(f, "db"), "db-2 waits while db-1 is not Ready")

	f.makeReady("db-1")
	clock.tick(t)
	assert.Equal(t, []string{"db-0", "db-1", "db-2"}, replicaNames(f, "db"))
}

func TestStatefulSet_ParallelMakesEveryReplicaAtOnce(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 3, policy: "Parallel"}.spec())

	start(t, f)

	assert.Equal(t, []string{"db-0", "db-1", "db-2"}, replicaNames(f, "db"), "no replica waits for another")
}

func TestStatefulSet_AReplicaDeletedComesBackWithTheSameNameAndVolume(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 2, claims: true}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 2, "alpine")
	volume := f.volume("data-db-1").Metadata.ResourceVersion
	writes := f.volumeWrites()

	f.markDeleting("db-1")
	clock.tick(t)
	assert.Equal(t, []string{"db-1"}, f.terminating(), "the replica is not made while the old one is still held by its agent")
	assert.Equal(t, writes, f.volumeWrites(), "no Volume is written while it is released")

	f.removeMicroVM("db-1")
	clock.tick(t)
	back := f.microvm("db-1")
	require.NotNil(t, back, "the replica comes back under its name")
	assert.Equal(t, []api.MicroVMVolume{{Name: "data-db-1", MountPath: "/data"}}, back.Spec.Volumes, "and mounts the Volume it had")
	assert.Equal(t, volume, f.volume("data-db-1").Metadata.ResourceVersion, "the Volume is the one it was")
	assert.Equal(t, writes, f.volumeWrites(), "the Volume is not written again")
	assert.Equal(t, []string{"data-db-0", "data-db-1"}, f.volumeNames())
}

func TestStatefulSet_AFailedReplicaIsMadeAgainUnderItsNameWithItsVolume(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 2, claims: true}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 2, "alpine")
	writes := f.volumeWrites()

	f.failMicroVM("db-1")
	clock.tick(t)
	require.Equal(t, []string{"db-1"}, f.terminating(), "the Failed replica is deleted before any agent heals it")
	settle(t, f, clock, "db", 2, "alpine")

	assert.Equal(t, []string{"db-0", "db-1"}, replicaNames(f, "db"))
	assert.Equal(t, "data-db-1", f.microvm("db-1").Spec.Volumes[0].Name)
	assert.Equal(t, writes, f.volumeWrites())
}

func TestStatefulSet_ScaleDownRemovesTheHighestOrdinalFirstAndKeepsTheVolumes(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 4, claims: true}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 4, "alpine")
	f.setStatefulSetSpec("db", ssOpts{replicas: 1, claims: true}.spec())

	var order []string
	seen := map[string]bool{}
	for i := range 20 {
		f.agentStep(i%2 == 0)
		clock.tick(t)
		term := f.terminating()
		assert.LessOrEqual(t, len(term), 1, "OrderedReady removes one at a time")
		for _, n := range term {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}

	assert.Equal(t, []string{"db-3", "db-2", "db-1"}, order)
	assert.Equal(t, []string{"db-0"}, replicaNames(f, "db"))
	assert.Equal(t, []string{"data-db-0", "data-db-1", "data-db-2", "data-db-3"}, f.volumeNames(), "the Volumes outlive the replicas")
}

func TestStatefulSet_ParallelScaleDownRemovesEveryExtraAtOnce(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 4, policy: "Parallel"}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 4, "alpine")
	f.setStatefulSetSpec("db", ssOpts{replicas: 1, policy: "Parallel"}.spec())

	clock.tick(t)

	assert.Equal(t, []string{"db-1", "db-2", "db-3"}, f.terminating())
}

func TestStatefulSet_ARollingUpdateReplacesFromTheHighestOrdinalDownOneAtATime(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 4, image: "v1", claims: true}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 4, "v1")
	writes := f.volumeWrites()
	f.setStatefulSetSpec("db", ssOpts{replicas: 4, image: "v2", claims: true}.spec())

	var order []string
	seen := map[string]bool{}
	for i := range 40 {
		f.agentStep(i%2 == 0)
		clock.tick(t)
		ms := f.replicasOf("db")
		down := 4 - len(ms)
		for _, m := range ms {
			if m.Metadata.DeletionTimestamp != nil || !isReady(&m) {
				down++
			}
		}
		assert.LessOrEqual(t, down, 1, "at most one replica is down at a time")
		for _, n := range f.terminating() {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}

	assert.Equal(t, []string{"db-3", "db-2", "db-1", "db-0"}, order)
	for _, m := range f.replicasOf("db") {
		assert.Equal(t, "v2", m.Spec.Image, m.Metadata.Name)
	}
	assert.Equal(t, writes, f.volumeWrites(), "the replacements keep their Volumes")
}

func TestStatefulSet_APartitionKeepsTheLowerReplicasOutOfARollout(t *testing.T) {
	f := newFakeAPI(t)
	update := `{"type":"RollingUpdate","rollingUpdate":{"partition":2}}`
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 4, image: "v1", update: update}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 4, "v1")
	f.setStatefulSetSpec("db", ssOpts{replicas: 4, image: "v2", update: update}.spec())

	for i := range 40 {
		f.agentStep(i%2 == 0)
		clock.tick(t)
	}

	images := map[string]string{}
	for _, m := range f.replicasOf("db") {
		images[m.Metadata.Name] = m.Spec.Image
	}
	assert.Equal(t, map[string]string{"db-0": "v1", "db-1": "v1", "db-2": "v2", "db-3": "v2"}, images)
}

func TestStatefulSet_OnDeleteReplacesOnlyAReplicaTheUserDeletes(t *testing.T) {
	f := newFakeAPI(t)
	update := `{"type":"OnDelete"}`
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 3, image: "v1", update: update}.spec())
	clock := start(t, f)
	settle(t, f, clock, "db", 3, "v1")
	f.setStatefulSetSpec("db", ssOpts{replicas: 3, image: "v2", update: update}.spec())

	for i := range 10 {
		f.agentStep(i%2 == 0)
		clock.tick(t)
	}
	assert.Empty(t, f.terminating(), "nothing is replaced by itself")
	for _, m := range f.replicasOf("db") {
		assert.Equal(t, "v1", m.Spec.Image, m.Metadata.Name)
	}

	f.removeMicroVM("db-1")
	clock.tick(t)

	assert.Equal(t, "v2", f.microvm("db-1").Spec.Image, "a replica made again is made from the new template")
	assert.Equal(t, "v1", f.microvm("db-0").Spec.Image)
	assert.Equal(t, "v1", f.microvm("db-2").Spec.Image)
}

func TestStatefulSet_WritesTheCountsAndTheGenerationItObserved(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 2, image: "v1", policy: "Parallel"}.spec())
	clock := start(t, f)
	f.makeReady("db-0")
	clock.tick(t)
	assert.Equal(t, job.StatefulSetStatus{ObservedGeneration: 1, Replicas: 2, ReadyReplicas: 1, UpdatedReplicas: 2}, f.statefulSetStatus("db"))

	f.setStatefulSetSpec("db", ssOpts{replicas: 2, image: "v2", policy: "Parallel", update: `{"type":"OnDelete"}`}.spec())
	clock.tick(t)

	assert.Equal(t, job.StatefulSetStatus{ObservedGeneration: 2, Replicas: 2, ReadyReplicas: 1, UpdatedReplicas: 0}, f.statefulSetStatus("db"), "no replica is of the new template")
}

func TestStatefulSet_ASpecItCannotUseMakesNothingAndLeavesOthersServed(t *testing.T) {
	good := ssOpts{replicas: 1}.spec()
	tests := []struct{ name, spec string }{
		{"empty selector", `{"replicas":1,"selector":{"matchLabels":{}},"template":{"image":"a","resources":{}}}`},
		{"unknown policy", ssOpts{replicas: 1, policy: "Burst"}.spec()},
		{"unknown update type", ssOpts{replicas: 1, update: `{"type":"Recreate"}`}.spec()},
		{"negative partition", ssOpts{replicas: 1, update: `{"rollingUpdate":{"partition":-1}}`}.spec()},
		{"claim mounted by nothing", `{"replicas":1,"selector":{"matchLabels":{"app":"x"}},"template":{"image":"a","resources":{}},"volumeClaimTemplates":[{"name":"data","spec":{"storageClass":"local"}}]}`},
		{"duplicate claim", `{"replicas":1,"selector":{"matchLabels":{"app":"x"}},"template":{"image":"a","resources":{},"volumes":[{"name":"data","mountPath":"/d"}]},"volumeClaimTemplates":[{"name":"data","spec":{"storageClass":"local"}},{"name":"data","spec":{"storageClass":"local"}}]}`},
		{"undecodable", `{"replicas":"many"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.addStatefulSet("bad", "uid-bad", tt.spec)
			f.addStatefulSet("ok", "uid-ok", good)

			start(t, f)

			assert.Empty(t, replicaNames(f, "bad"), "nothing is made for the spec")
			assert.Empty(t, f.volumeNames())
			assert.Equal(t, []string{"ok-0"}, replicaNames(f, "ok"), "control: another StatefulSet is served")
		})
	}
}

func TestStatefulSet_OnlyMicroVMsItOwnsByUidAndOrdinalAreItsReplicas(t *testing.T) {
	f := newFakeAPI(t)
	f.addStatefulSet("db", "uid-db", ssOpts{replicas: 1}.spec())
	f.addSetReplica("db-x", "db", "uid-db", true)
	f.addSetReplica("db-01", "db", "uid-db", true)
	f.addSetReplica("db-5", "db", "uid-old", true)

	clock := start(t, f)
	clock.tick(t)

	assert.Empty(t, f.terminating(), "names that are not ordinals, and a replica of another uid, are left alone")
	assert.NotNil(t, f.microvm("db-0"), "the replica it should own is made")
}
