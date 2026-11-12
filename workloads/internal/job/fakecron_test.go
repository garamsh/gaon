package job_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/gaon/workloads/internal/job"
)

const completeStatus = `{"succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}`

func (f *fakeAPI) cronJobsClient() *api.CustomResourceClient {
	c, err := api.NewCustomResourceClient(f.server.URL, nil, job.CronJobType)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// addCronJob stores a CronJob with the uid, the spec JSON and the status JSON
// given; status may be empty.
func (f *fakeAPI) addCronJob(name string, uid api.UID, spec, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cronjobs[name] = &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.KindCronJob},
		Metadata: api.ObjectMeta{Name: name, UID: uid, ResourceVersion: f.rv()},
		Spec:     json.RawMessage(spec),
	}
	if status != "" {
		f.cronjobs[name].Status = json.RawMessage(status)
	}
}

// cronStatus returns the status the service wrote to the CronJob.
func (f *fakeAPI) cronStatus(name string) job.CronJobStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s job.CronJobStatus
	if raw := f.cronjobs[name].Status; len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			f.t.Fatal(err)
		}
	}
	return s
}

// addScheduledJob stores a Job owned by the CronJob, scheduled for the Unix
// minute, finished when done, and returns its name.
func (f *fakeAPI) addScheduledJob(cron string, uid api.UID, minute int, done bool) string {
	name := fmt.Sprintf("%s-%d", cron, minute)
	f.mu.Lock()
	defer f.mu.Unlock()
	o := &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: job.APIVersion, Kind: job.Kind},
		Metadata: api.ObjectMeta{
			Name: name, UID: api.UID("uid-" + name), ResourceVersion: f.rv(),
			Labels:          map[string]string{job.ScheduledLabel: strconv.Itoa(minute)},
			OwnerReferences: []api.OwnerReference{{APIVersion: job.APIVersion, Kind: job.KindCronJob, Name: cron, UID: uid, Controller: true}},
		},
		Spec: json.RawMessage(jobSpec(1, 0)),
	}
	if done {
		o.Status = json.RawMessage(completeStatus)
	}
	f.jobs[name] = o
	return name
}

// finishJob marks a stored Job Complete.
func (f *fakeAPI) finishJob(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[name].Status = json.RawMessage(completeStatus)
}

// jobNames returns the names of the stored Jobs, sorted.
func (f *fakeAPI) jobNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for n := range f.jobs {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (f *fakeAPI) storedJob(name string) *api.CustomObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[name]
}

func (f *fakeAPI) listCronJobs(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := api.CustomObjectList{Metadata: api.ListMeta{ResourceVersion: "1"}, Items: []api.CustomObject{}}
	for _, o := range f.cronjobs {
		list.Items = append(list.Items, *o)
	}
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAPI) putCronJobStatus(w http.ResponseWriter, r *http.Request) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.cronjobs[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "cronjob"))
		return
	}
	if o.Metadata.ResourceVersion != in.Metadata.ResourceVersion {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "cronjob changed"))
		return
	}
	o.Status = in.Status
	o.Metadata.ResourceVersion = f.rv()
	writeJSON(w, http.StatusOK, o)
}

// putJob creates a Job: a PUT of one that exists answers 409.
func (f *fakeAPI) putJob(w http.ResponseWriter, r *http.Request) {
	var in api.CustomObject
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobs[in.Metadata.Name] != nil {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonAlreadyExists, "job"))
		return
	}
	in.Metadata.UID = api.UID("uid-" + in.Metadata.Name)
	in.Metadata.ResourceVersion = f.rv()
	f.jobs[in.Metadata.Name] = &in
	writeJSON(w, http.StatusCreated, &in)
}

func (f *fakeAPI) deleteJob(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.jobs[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.NewAPIError(api.ReasonNotFound, "job"))
		return
	}
	if string(o.Metadata.ResourceVersion) != r.URL.Query().Get("resourceVersion") {
		writeJSON(w, http.StatusConflict, api.NewAPIError(api.ReasonConflict, "job changed"))
		return
	}
	delete(f.jobs, o.Metadata.Name)
	writeJSON(w, http.StatusOK, o)
}
