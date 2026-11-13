//go:build extensions

package extensions

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// helmChart is the example chart, whose templates emit gaon kinds.
const helmChart = "../testdata/helm/web"

var deploymentType = api.CustomResourceType{Group: workloadsGroup, Version: workloadsVersion, Kind: "Deployment", Plural: "deployments"}

// helmApplies shows a stream rendered by `helm template` with a named release
// applies through `gaon apply -f -`: the objects are created, a second run
// reports each unchanged and writes nothing, and a changed value updates only
// the object it changes. The Deployment kind is registered by the earlier
// workloads subtest; the unit has stopped, so only the stored objects are
// checked.
func (c *cluster) helmApplies(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skipf("helm is not on PATH, so the chart %s is not rendered: %v", helmChart, err)
	}
	ctx, cancel := within(2 * waitFor)
	defer cancel()
	require.Eventually(t, func() bool {
		d, err := c.defs.GetCustomResourceDefinition(ctx, "deployments."+workloadsGroup)
		if err != nil {
			return false
		}
		for _, cond := range d.Status.Conditions {
			if cond.Type == api.CRDEstablished && cond.Status == api.ConditionTrue {
				return true
			}
		}
		return false
	}, waitFor, pollEvery, "the Deployment kind is Established")
	deployments, err := api.NewCustomResourceClient(c.server, c.adminHTTP(t), deploymentType)
	require.NoError(t, err)
	role := func() api.ResourceVersion {
		r, err := c.rbac.GetClusterRole(ctx, "rel-web-reader")
		require.NoError(t, err)
		return r.Metadata.ResourceVersion
	}
	deployment := func() *api.CustomObject {
		d, err := deployments.Get(ctx, "rel-web")
		require.NoError(t, err)
		return d
	}
	render := func(args ...string) []byte {
		out, err := exec.Command(helm, append([]string{"template", "rel", helmChart}, args...)...).Output() //nolint:gosec // the test's own chart
		require.NoError(t, err)
		return out
	}

	assert.Equal(t, "clusterrole/rel-web-reader created\ndeployment/rel-web created\n", c.applyStream(t, ctx, render()))
	firstRole, firstDeployment := role(), deployment()
	assert.JSONEq(t, `{"replicas":2,"selector":{"matchLabels":{"app.kubernetes.io/instance":"rel"}},"template":{"image":"`+microvmImage+`","rootDiskSize":"256Mi","resources":{"requests":{"cpu":"100m","memory":"64Mi"}}}}`, string(firstDeployment.Spec))

	assert.Equal(t, "clusterrole/rel-web-reader unchanged\ndeployment/rel-web unchanged\n", c.applyStream(t, ctx, render()), "the second run changes nothing")
	assert.Equal(t, firstRole, role(), "the ClusterRole was not written")
	assert.Equal(t, firstDeployment.Metadata.ResourceVersion, deployment().Metadata.ResourceVersion, "the Deployment was not written")

	assert.Equal(t, "clusterrole/rel-web-reader unchanged\ndeployment/rel-web configured\n", c.applyStream(t, ctx, render("--set", "replicas=3")), "a changed value updates only its object")
	assert.Equal(t, firstRole, role())
	assert.Contains(t, string(deployment().Spec), `"replicas":3`)
}

// applyStream applies stdin with `gaon apply -f -` as the administrator and
// returns what the command printed.
func (c *cluster) applyStream(t *testing.T, ctx context.Context, stdin []byte) string {
	t.Helper()
	id := c.ca.client(t, "test-admin", "gaon:admins")
	cmd := exec.CommandContext(ctx, filepath.Join(c.binDir, "gaon"), "--server", c.root, "--tls-ca", c.ca.CertFile, "--tls-cert", id.CertFile, "--tls-key", id.KeyFile, "apply", "-f", "-") //nolint:gosec // the CLI make built
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	return stdout.String()
}
