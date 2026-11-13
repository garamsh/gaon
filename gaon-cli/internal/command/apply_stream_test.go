package command_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

// helmStream is the shape of `helm template` output: a comment naming each
// template's source, a document of only comments, and a registered kind.
const helmStream = `---
# Source: web/templates/role.yaml
` + clusterRoleYAML + `---
# Source: web/templates/empty.yaml
---
# Source: web/templates/widget.yaml
` + widgetYAML + `---
# Source: web/templates/microvm.yaml
` + microvmYAML

// executeStdin runs one gaon command whose standard input is stdin.
func executeStdin(t *testing.T, f *fakeServer, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = command.Run(context.Background(), f.url()+computePrefix, nil, args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func newStreamServer(t *testing.T) *fakeServer {
	f := newFakeServer(t)
	seedDefinition(f, newDefinition("example.com", "widgets", "widget", "Widget", true))
	return f
}

func TestApply_ReadsAHelmStreamFromStandardInput(t *testing.T) {
	f := newStreamServer(t)

	stdout, stderr, code := executeStdin(t, f, helmStream, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader created\nwidget/w2 created\nmicrovm/web created\n", stdout)
	assert.Equal(t, "reader", load[api.ClusterRole](f, rbacRoot+"/clusterroles/reader").Metadata.Name)
	assert.JSONEq(t, `{"size":3}`, string(load[api.CustomObject](f, widgetRoot+"/widgets/w2").Spec))
	assert.Equal(t, api.RunAlways, load[api.MicroVM](f, "/microvms/web").Spec.RunStrategy)
}

func TestApply_ReportsNoStandardInputAsAFailure(t *testing.T) {
	f := newStreamServer(t)

	_, stderr, code := execute(t, f, "apply", "-f", "-")
	assert.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, "no standard input")
	assert.Zero(t, f.writes())
}

func TestApply_ASecondRunOfTheSameStreamWritesNothing(t *testing.T) {
	f := newStreamServer(t)
	_, stderr, code := executeStdin(t, f, helmStream, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	writes := f.writes()

	stdout, stderr, code := executeStdin(t, f, helmStream, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader unchanged\nwidget/w2 unchanged\nmicrovm/web unchanged\n", stdout)
	assert.Equal(t, writes, f.writes(), "no PUT is sent for an object that already holds the document")
}

func TestApply_AChangedStreamUpdatesOnlyTheObjectsItChanged(t *testing.T) {
	f := newStreamServer(t)
	_, stderr, code := executeStdin(t, f, helmStream, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	before := load[api.CustomObject](f, widgetRoot+"/widgets/w2").Metadata.ResourceVersion

	changed := strings.Replace(helmStream, "size: 3", "size: 4", 1)
	stdout, stderr, code := executeStdin(t, f, changed, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader unchanged\nwidget/w2 configured\nmicrovm/web unchanged\n", stdout)
	assert.JSONEq(t, `{"size":4}`, string(load[api.CustomObject](f, widgetRoot+"/widgets/w2").Spec))
	assert.NotEqual(t, before, load[api.CustomObject](f, widgetRoot+"/widgets/w2").Metadata.ResourceVersion)
}

func TestApply_ALabelOrAnAnnotationOnlyChangeIsNotUnchanged(t *testing.T) {
	f := newStreamServer(t)
	_, stderr, code := executeStdin(t, f, helmStream, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)

	labelled := strings.Replace(microvmYAML, "  name: web\n", "  name: web\n  labels:\n    app.kubernetes.io/instance: rel\n", 1)
	stdout, stderr, code := executeStdin(t, f, labelled, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "microvm/web configured\n", stdout)
	assert.Equal(t, "rel", load[api.MicroVM](f, "/microvms/web").Metadata.Labels["app.kubernetes.io/instance"])
}

func TestApply_AnUnchangedDocumentStillNeverOverwritesAConcurrentChange(t *testing.T) {
	f := newStreamServer(t)
	_, stderr, code := executeStdin(t, f, microvmYAML, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	f.beforeWrite = func(method, path string) {
		theirs := newMicroVM("web", api.RunOnce)
		theirs.Metadata.ResourceVersion = api.ResourceVersion(versionOf(f.objects[path]))
		f.store(path, toMap(t, theirs))
		f.beforeWrite = nil
	}

	stdout, stderr, code := executeStdin(t, f, strings.Replace(microvmYAML, "Always", "Halted", 1), "apply", "-f", "-")
	assert.Equal(t, command.ExitFailure, code, stdout)
	assert.Contains(t, stderr, "changed by someone else")
	assert.Equal(t, api.RunOnce, load[api.MicroVM](f, "/microvms/web").Spec.RunStrategy)
}

func TestApply_AStreamStopsAtItsFirstFailureAndARerunContinuesFromThere(t *testing.T) {
	f := newFakeServer(t) // no definition: the widget cannot be found
	_, stderr, code := executeStdin(t, f, clusterRoleYAML+"---\n"+widgetYAML+"---\n"+microvmYAML, "apply", "-f", "-")
	require.Equal(t, command.ExitFailure, code)
	assert.Contains(t, stderr, `unknown kind "Widget"`)
	assert.Zero(t, f.writes(), "a document that cannot be resolved stops the apply before any write")

	seedDefinition(f, newDefinition("example.com", "widgets", "widget", "Widget", true))
	stdout, stderr, code := executeStdin(t, f, clusterRoleYAML+"---\n"+widgetYAML+"---\n"+microvmYAML, "apply", "-f", "-")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "clusterrole/reader created\nwidget/w2 created\nmicrovm/web created\n", stdout)
}
