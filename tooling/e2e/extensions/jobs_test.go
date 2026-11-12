//go:build extensions

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

const (
	workloadsGroup   = "workloads.gaon.garam.sh"
	workloadsVersion = "v1alpha1"
	managedByLabel   = workloadsGroup + "/managed-by"
	// rbacManifest is what an administrator applies to let gaon-workloads work.
	rbacManifest = "../../../workloads/deploy/rbac.yaml"
)

var jobType = api.CustomResourceType{Group: workloadsGroup, Version: workloadsVersion, Kind: "Job", Plural: "jobs"}

// jobStatus is the part of a Job's status the test reads.
type jobStatus struct {
	Active, Succeeded, Failed int32
	Conditions                []api.Condition
}

// jobsRun shows gaon-workloads, a client of the public API with its own
// certificate, is refused until an administrator applies its role, then
// registers the Job kind, runs a Job's MicroVMs to completion, retries failures
// up to the limit, and leaves the MicroVMs to the garbage collector.
func (c *cluster) jobsRun(t *testing.T) {
	ctx, cancel := within(5 * time.Minute)
	defer cancel()
	id := c.ca.client(t, "gaon-workloads")
	defs := c.client(t, id, api.APIExtensionsGroupVersion)

	// The refusal: the certificate has no binding yet.
	_, err := defs.PutCustomResourceDefinition(ctx, probeDefinition(map[string]string{managedByLabel: "gaon-workloads"}))
	require.ErrorIs(t, err, api.ErrForbidden, "gaon-workloads registers a kind before its role is applied")

	c.applyManifest(t, rbacManifest)

	// The control: the same write is allowed by the applied role, for a
	// definition that carries the unit's label, and refused without it.
	_, err = defs.PutCustomResourceDefinition(ctx, probeDefinition(map[string]string{managedByLabel: "gaon-workloads"}))
	require.NoError(t, err, "the applied role lets gaon-workloads register a definition it labels")
	other := probeDefinition(nil)
	other.Metadata.Name = "others." + workloadsGroup
	other.Spec.Names = api.CustomResourceNames{Kind: "Other", ListKind: "OtherList", Plural: "others", Singular: "other"}
	_, err = defs.PutCustomResourceDefinition(ctx, other)
	require.ErrorIs(t, err, api.ErrForbidden, "the role covers only the definitions the unit labels")
	_, err = c.defs.DeleteCustomResourceDefinition(ctx, "probes."+workloadsGroup, mustDefinition(t, ctx, c, "probes."+workloadsGroup).Metadata.ResourceVersion)
	require.NoError(t, err)

	tls := []string{"-tls-ca", c.ca.CertFile, "-tls-cert", id.CertFile, "-tls-key", id.KeyFile}
	workloads := startService(t, c.dir, c.binDir, "workloads", "gaon-workloads",
		append([]string{"-server", c.server, "-interval", "500ms"}, tls...)...)
	require.Eventually(t, func() bool {
		d, err := c.defs.GetCustomResourceDefinition(ctx, "jobs."+workloadsGroup)
		if err != nil {
			return false
		}
		for _, cond := range d.Status.Conditions {
			if cond.Type == api.CRDEstablished && cond.Status == api.ConditionTrue {
				return true
			}
		}
		return false
	}, waitFor, pollEvery, "gaon-workloads registers the Job kind and it is Established")

	jobs, err := api.NewCustomResourceClient(c.server, c.adminHTTP(t), jobType)
	require.NoError(t, err)

	// A Job that needs two successes runs two MicroVMs and completes.
	_, err = jobs.Put(ctx, job("exits", `{"completions":2,"backoffLimit":0,"template":`+template+`}`))
	require.NoError(t, err)
	done := c.waitJob(t, ctx, jobs, "exits", func(s jobStatus) bool { return finished(s) != "" })
	assert.Equal(t, "Complete", finished(done.status))
	assert.Equal(t, int32(2), done.status.Succeeded)
	assert.Zero(t, done.status.Failed)
	for _, name := range []string{"exits-0", "exits-1"} {
		m, err := c.admin.GetMicroVM(ctx, name)
		require.NoError(t, err, name)
		assert.Equal(t, api.MicroVMSucceeded, m.EffectivePhase(), name)
		assert.Equal(t, api.RunOnce, m.Spec.RunStrategy, name)
		require.Len(t, m.Metadata.OwnerReferences, 1, name)
		assert.Equal(t, done.object.Metadata.UID, m.Metadata.OwnerReferences[0].UID, "%s is owned by the Job's uid", name)
		assert.NotEmpty(t, done.object.Metadata.UID, "the API server set the Job's uid")
	}

	// A Job whose MicroVMs fail is retried once, then fails.
	_, err = jobs.Put(ctx, job("fails", `{"completions":1,"backoffLimit":1,"template":`+template+`}`))
	require.NoError(t, err)
	failed := c.waitJob(t, ctx, jobs, "fails", func(s jobStatus) bool { return finished(s) != "" })
	assert.Equal(t, "Failed", finished(failed.status))
	assert.Equal(t, int32(2), failed.status.Failed, "the failed MicroVM was retried once")
	assert.Zero(t, failed.status.Succeeded)
	for _, name := range []string{"fails-0", "fails-1"} {
		m, err := c.admin.GetMicroVM(ctx, name)
		require.NoError(t, err, name)
		assert.Equal(t, api.MicroVMFailed, m.EffectivePhase(), name)
	}
	_, err = c.admin.GetMicroVM(ctx, "fails-2")
	require.ErrorIs(t, err, api.ErrNotFound, "no third MicroVM after the limit")

	// Deleting a Job leaves its MicroVMs to the garbage collector; the other
	// Job's are kept.
	gone := done.object
	_, err = jobs.Delete(ctx, "exits", gone.Metadata.ResourceVersion)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err0 := c.admin.GetMicroVM(ctx, "exits-0")
		_, err1 := c.admin.GetMicroVM(ctx, "exits-1")
		return errors.Is(err0, api.ErrNotFound) && errors.Is(err1, api.ErrNotFound)
	}, waitFor, pollEvery, "the garbage collector deletes the deleted Job's MicroVMs")
	_, err = c.admin.GetMicroVM(ctx, "fails-0")
	require.NoError(t, err, "the other Job's MicroVM is kept")

	// The unit stops with this subtest, so its log is checked here, as
	// noForbidden checks the others.
	assert.False(t, workloads.exited(), "gaon-workloads is still running")
	assert.NotContains(t, strings.ToLower(workloads.output(t)), "forbidden", "gaon-workloads met a refusal")
}

// template is the MicroVM template of the test's Jobs.
const template = `{"image":"` + microvmImage + `","rootDiskSize":"256Mi","resources":{"requests":{"cpu":"100m","memory":"64Mi"}}}`

func probeDefinition(labels map[string]string) *api.CustomResourceDefinition {
	return &api.CustomResourceDefinition{
		TypeMeta: api.TypeMeta{APIVersion: api.APIExtensionsGroupVersion, Kind: api.KindCustomResourceDefinition},
		Metadata: api.ObjectMeta{Name: "probes." + workloadsGroup, Labels: labels},
		Spec: api.CustomResourceDefinitionSpec{
			Group: workloadsGroup,
			Names: api.CustomResourceNames{Kind: "Probe", ListKind: "ProbeList", Plural: "probes", Singular: "probe"},
			Scope: api.ScopeCluster,
			Versions: []api.CustomResourceVersion{{
				Name: workloadsVersion, Served: true, Storage: true,
				Schema: api.CustomResourceValidation{OpenAPIV3Schema: &api.JSONSchemaProps{Type: api.SchemaObject}},
			}},
		},
	}
}

func mustDefinition(t *testing.T, ctx context.Context, c *cluster, name string) *api.CustomResourceDefinition {
	t.Helper()
	d, err := c.defs.GetCustomResourceDefinition(ctx, name)
	require.NoError(t, err)
	return d
}

func job(name, spec string) *api.CustomObject {
	return &api.CustomObject{
		TypeMeta: api.TypeMeta{APIVersion: workloadsGroup + "/" + workloadsVersion, Kind: "Job"},
		Metadata: api.ObjectMeta{Name: name},
		Spec:     json.RawMessage(spec),
	}
}

// finished returns the type of the Job's True condition, or "".
func finished(s jobStatus) string {
	for _, cond := range s.Conditions {
		if cond.Status == api.ConditionTrue {
			return cond.Type
		}
	}
	return ""
}

type jobState struct {
	object *api.CustomObject
	status jobStatus
}

// waitJob polls the Job until ok accepts its status.
func (c *cluster) waitJob(t *testing.T, ctx context.Context, jobs *api.CustomResourceClient, name string, ok func(jobStatus) bool) jobState {
	t.Helper()
	var got jobState
	require.Eventually(t, func() bool {
		o, err := jobs.Get(ctx, name)
		if err != nil {
			return false
		}
		var s jobStatus
		if len(o.Status) > 0 && json.Unmarshal(o.Status, &s) != nil {
			return false
		}
		got = jobState{object: o, status: s}
		return ok(s)
	}, waitFor, pollEvery, "Job "+name+" reaches the state")
	return got
}

// adminHTTP is the administrator's HTTP client.
func (c *cluster) adminHTTP(t *testing.T) *http.Client {
	t.Helper()
	id := c.ca.client(t, "test-admin", "gaon:admins")
	h, err := api.NewTLSClient(c.ca.CertFile, id.CertFile, id.KeyFile)
	require.NoError(t, err)
	return h
}

// applyManifest applies the YAML file with the gaon CLI, as the administrator.
func (c *cluster) applyManifest(t *testing.T, path string) {
	t.Helper()
	id := c.ca.client(t, "test-admin", "gaon:admins")
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	cmd := exec.Command(filepath.Join(c.binDir, "gaon"), "--server", c.root, "--tls-ca", c.ca.CertFile, "--tls-cert", id.CertFile, "--tls-key", id.KeyFile, "apply", "-f", abs) //nolint:gosec // the CLI make built
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
