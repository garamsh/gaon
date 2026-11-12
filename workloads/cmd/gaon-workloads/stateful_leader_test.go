package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

const statefulSpecJSON = `{"replicas":3,"selector":{"matchLabels":{"app":"db"}},` +
	`"template":{"image":"alpine","resources":{},"volumes":[{"name":"data","mountPath":"/data"}]},` +
	`"volumeClaimTemplates":[{"name":"data","spec":{"storageClass":"local","accessModes":["ReadWriteOnce"],"storage":"1Gi"}}]}`

func (a *electionAPI) setStatefulSet() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sts = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindStatefulSet},
		Metadata: api.ObjectMeta{Name: "db", UID: "uid-db", Generation: 1, ResourceVersion: "s1"},
		Spec:     json.RawMessage(statefulSpecJSON),
	}
}

func (a *electionAPI) listStatefulSets(w http.ResponseWriter) {
	list := api.CustomObjectList{TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: "StatefulSetList"}, Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	if a.sts != nil {
		list.Items = append(list.Items, *a.sts)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (a *electionAPI) putStatefulSetStatus(w http.ResponseWriter, r *http.Request, fail func(api.Reason)) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || a.sts == nil {
		fail(api.ReasonInvalid)
		return
	}
	if in.Metadata.ResourceVersion != a.sts.Metadata.ResourceVersion {
		fail(api.ReasonConflict)
		return
	}
	a.version++
	a.sts.Status = in.Status
	a.sts.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("s", a.version))
	_ = json.NewEncoder(w).Encode(a.sts)
}

func (a *electionAPI) listVolumes(w http.ResponseWriter) {
	list := api.VolumeList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.Volume{}}
	for _, v := range a.volumes {
		list.Items = append(list.Items, v)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (a *electionAPI) putVolume(w http.ResponseWriter, r *http.Request, fail func(api.Reason)) {
	var v api.Volume
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		fail(api.ReasonInvalid)
		return
	}
	a.volumePuts++
	if _, ok := a.volumes[v.Metadata.Name]; ok {
		fail(api.ReasonAlreadyExists)
		return
	}
	a.version++
	v.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("v", a.version))
	a.volumes[v.Metadata.Name] = v
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(v)
}

// makeReady marks a MicroVM Ready, as its node agent does.
func (a *electionAPI) makeReady(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.microvms[name]
	now := time.Now()
	m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue, LastTransitionTime: &now}}}
	a.version++
	m.Metadata.ResourceVersion = api.ResourceVersion(fmt.Sprint("m", a.version))
	a.microvms[name] = m
}

// microvmsNamed returns the names of the StatefulSet's MicroVMs, not the Job's.
func (a *electionAPI) microvmsNamed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var names []string
	for n, m := range a.microvms {
		if m.Metadata.Labels[job.StatefulSetLabel] == "db" {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

func (a *electionAPI) volumeCount() (stored, puts int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.volumes), a.volumePuts
}

func TestRun_AnOrderedStatefulSetContinuesWhereTheFirstLeaderLeftOff(t *testing.T) {
	a := newElectionAPI(t)
	a.setStatefulSet()

	stopA, doneA := startInstance(t, a, "a")
	require.Eventually(t, func() bool { return slices.Equal(a.microvmsNamed(), []string{"db-0"}) }, 5*time.Second, 10*time.Millisecond, "the first instance makes the first replica")
	startInstance(t, a, "b")
	stopA()
	require.NoError(t, <-doneA)
	require.Eventually(t, func() bool { return a.holder() == "b" }, 5*time.Second, 10*time.Millisecond, "the second instance takes the Lease")
	require.Never(t, func() bool { return len(a.microvmsNamed()) > 1 }, 400*time.Millisecond, 20*time.Millisecond, "the second replica waits for the first to be Ready")
	assert.Zero(t, a.createsOf("b"), "setup: the second instance made nothing yet")

	a.makeReady("db-0")
	require.Eventually(t, func() bool { return slices.Equal(a.microvmsNamed(), []string{"db-0", "db-1"}) }, 5*time.Second, 10*time.Millisecond, "the second instance goes on from the stored state")
	a.makeReady("db-1")
	require.Eventually(t, func() bool { return len(a.microvmsNamed()) == 3 }, 5*time.Second, 10*time.Millisecond)

	stored, puts := a.volumeCount()
	assert.Equal(t, 3, stored, "a Volume per replica")
	assert.Equal(t, 3, puts, "no Volume was written twice across the handover")
	assert.Equal(t, 2, a.createsOf("b"), "the second instance made the replicas after the first")
}
