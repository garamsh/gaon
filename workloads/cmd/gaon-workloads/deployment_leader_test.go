package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

// fleetState is what the server checks and plays for the replicas of its
// Deployment. The bounds hold after every write; the extremes seen are kept so
// that a test shows they were reached.
type fleetState struct {
	checking   bool
	maxPresent int
	minReady   int

	mostPresent int
	fewestReady int
	violations  []string

	// hold is an image whose MicroVMs the agents do not make Ready.
	hold string
}

func depSpecJSON(replicas int, image string) string {
	return fmt.Sprintf(`{"replicas":%d,"selector":{"matchLabels":{"app":"web"}},"template":{"image":%q,"resources":{}},"strategy":{"rollingUpdate":{"maxSurge":"1","maxUnavailable":"0"}}}`, replicas, image)
}

func (a *electionAPI) setDeployment(replicas int, image string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.version++
	rv := api.ResourceVersion(fmt.Sprint("d", a.version))
	if a.dep == nil {
		a.dep = &api.CustomObject{
			TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindDeployment},
			Metadata: api.ObjectMeta{Name: "web", UID: "uid-web", Generation: 1},
		}
	} else {
		a.dep.Metadata.Generation++
	}
	a.dep.Metadata.ResourceVersion = rv
	a.dep.Spec = json.RawMessage(depSpecJSON(replicas, image))
}

// boundReplicas starts checking the Deployment's replicas after every write.
func (a *electionAPI) boundReplicas(maxPresent, minReady int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fleet = fleetState{checking: true, maxPresent: maxPresent, minReady: minReady, fewestReady: math.MaxInt, hold: a.fleet.hold}
	a.checkBounds()
}

func (a *electionAPI) holdImage(image string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fleet.hold = image
}

func isDeploymentReplica(m api.MicroVM) bool {
	return len(m.Metadata.OwnerReferences) > 0 && m.Metadata.OwnerReferences[0].Kind == job.KindDeployment
}

func isReady(m api.MicroVM) bool {
	return slices.ContainsFunc(m.Status.Conditions, func(c api.Condition) bool {
		return c.Type == "Ready" && c.Status == api.ConditionTrue
	})
}

// checkBounds records a bound the stored replicas break. The caller holds mu.
func (a *electionAPI) checkBounds() {
	f := &a.fleet
	if !f.checking {
		return
	}
	present, ready := 0, 0
	for _, m := range a.microvms {
		if !isDeploymentReplica(m) {
			continue
		}
		present++
		if m.Metadata.DeletionTimestamp == nil && isReady(m) {
			ready++
		}
	}
	f.mostPresent = max(f.mostPresent, present)
	f.fewestReady = min(f.fewestReady, ready)
	if present > f.maxPresent {
		f.violations = append(f.violations, fmt.Sprintf("%d MicroVMs exist, above %d", present, f.maxPresent))
	}
	if ready < f.minReady {
		f.violations = append(f.violations, fmt.Sprintf("%d MicroVMs are Ready, below %d", ready, f.minReady))
	}
}

// replicas counts the Deployment's MicroVMs by image: all of them, and the
// ones that are Ready and not being deleted.
func (a *electionAPI) replicas() (present, ready map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	present, ready = map[string]int{}, map[string]int{}
	for _, m := range a.microvms {
		if !isDeploymentReplica(m) {
			continue
		}
		present[m.Spec.Image]++
		if m.Metadata.DeletionTimestamp == nil && isReady(m) {
			ready[m.Spec.Image]++
		}
	}
	return present, ready
}

// replicasAre reports whether the Deployment has exactly n MicroVMs, all of
// the image and Ready.
func (a *electionAPI) replicasAre(n int, image string) bool {
	present, ready := a.replicas()
	return present[image] == n && ready[image] == n && len(present) == 1
}

// playAgents does what the node agents do until the test ends: every replica
// not being deleted becomes Ready, except the held image, and every replica
// being deleted is removed.
func (a *electionAPI) playAgents(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			a.agentRound()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (a *electionAPI) agentRound() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, m := range a.microvms {
		switch {
		case !isDeploymentReplica(m):
		case m.Metadata.DeletionTimestamp != nil:
			delete(a.microvms, name)
		case !isReady(m) && m.Spec.Image != a.fleet.hold:
			now := time.Now()
			m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue, LastTransitionTime: &now}}}
			a.version++
			m.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("m", a.version))
			a.microvms[name] = m
		}
	}
	a.checkBounds()
}

// loseReplica removes one of the Deployment's MicroVMs outright.
func (a *electionAPI) loseReplica(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.microvms, name)
	a.checkBounds()
}

func (a *electionAPI) deletesOf(who string) int { return a.count(a.deletes, who) }

func (a *electionAPI) listDeployments(w http.ResponseWriter) {
	list := api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: "DeploymentList"}, Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	if a.dep != nil {
		list.Items = append(list.Items, *a.dep)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (a *electionAPI) putDeploymentStatus(w http.ResponseWriter, r *http.Request, fail func(api.Reason)) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || a.dep == nil {
		fail(api.ReasonInvalid)
		return
	}
	if in.Metadata.ResourceVersion != a.dep.Metadata.ResourceVersion {
		fail(api.ReasonConflict)
		return
	}
	a.version++
	a.dep.Status = in.Status
	a.dep.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("d", a.version))
	_ = json.NewEncoder(w).Encode(a.dep)
}

func (a *electionAPI) deleteMicroVM(w http.ResponseWriter, r *http.Request, fail func(api.Reason)) {
	m, ok := a.microvms[path.Base(r.URL.Path)]
	if !ok {
		fail(api.ReasonNotFound)
		return
	}
	if string(m.Metadata.ResourceVersion) != r.URL.Query().Get("resourceVersion") {
		fail(api.ReasonConflict)
		return
	}
	now := time.Now()
	m.Metadata.DeletionTimestamp = &now
	a.version++
	m.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("m", a.version))
	a.microvms[m.Metadata.Name] = m
	a.checkBounds()
	_ = json.NewEncoder(w).Encode(m)
}

func TestRun_ARolloutKeepsItsBoundsAcrossALeaderChange(t *testing.T) {
	a := newElectionAPI(t)
	a.setDeployment(3, "v1")
	a.playAgents(t)

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.replicasAre(3, "v1") }, 5*time.Second, 10*time.Millisecond, "the first instance creates the replicas")
	a.boundReplicas(4, 3)

	// The agents do not make a v2 replica Ready, so the rollout stops after its
	// surge: the first instance has created one and may remove none.
	a.holdImage("v2")
	a.setDeployment(3, "v2")
	require.Eventually(t, func() bool { present, _ := a.replicas(); return present["v2"] == 1 }, 5*time.Second, 10*time.Millisecond, "the first instance starts the rollout")

	startInstance(t, a, "b")
	stopA()
	require.NoError(t, <-doneA)
	require.Eventually(t, func() bool { return a.holder() == "b" }, 5*time.Second, 10*time.Millisecond, "the second instance takes the Lease")
	present, ready := a.replicas()
	assert.Equal(t, map[string]int{"v1": 3, "v2": 1}, present, "the rollout is stopped half way at the handover")
	assert.Equal(t, map[string]int{"v1": 3}, ready)
	assert.Zero(t, a.deletesOf("a"), "setup: the first instance removed no replica")

	a.holdImage("")
	require.Eventually(t, func() bool { return a.replicasAre(3, "v2") }, 5*time.Second, 10*time.Millisecond, "the second instance finishes the rollout")

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Empty(t, a.fleet.violations, "the bounds held at every write, across the handover")
	assert.Equal(t, 4, a.fleet.mostPresent, "the rollout used its surge")
	assert.Equal(t, 3, a.fleet.fewestReady, "and never had fewer than replicas Ready")
	assert.Equal(t, 3, a.deletes["b"], "the second instance removed the three replicas of v1")
	assert.Equal(t, int32(3), deploymentStatus(t, a.dep).UpdatedReplicas)
}

func deploymentStatus(t *testing.T, d *api.CustomObject) job.DeploymentStatus {
	t.Helper()
	var s job.DeploymentStatus
	require.NoError(t, json.Unmarshal(d.Status, &s))
	return s
}

func TestRun_TheInstanceThatTakesOverReplacesAndScalesTheReplicas(t *testing.T) {
	a := newElectionAPI(t)
	a.setDeployment(3, "v1")
	a.playAgents(t)

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return a.replicasAre(3, "v1") }, 5*time.Second, 10*time.Millisecond)
	startInstance(t, a, "b")
	stopA()
	require.NoError(t, <-doneA)
	require.Eventually(t, func() bool { return a.holder() == "b" }, 5*time.Second, 10*time.Millisecond, "the second instance takes the Lease")

	a.loseReplica("web-1")
	require.Eventually(t, func() bool { return a.replicasAre(3, "v1") }, 5*time.Second, 10*time.Millisecond, "the second instance replaces a lost replica")

	a.setDeployment(5, "v1")
	require.Eventually(t, func() bool { return a.replicasAre(5, "v1") }, 5*time.Second, 10*time.Millisecond, "the second instance scales up")

	a.setDeployment(2, "v1")
	require.Eventually(t, func() bool { return a.replicasAre(2, "v1") }, 5*time.Second, 10*time.Millisecond, "the second instance scales down")
	assert.Positive(t, a.createsOf("b"), "the second instance created replicas")
	assert.Positive(t, a.deletesOf("b"))
}
