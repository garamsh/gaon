package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/tooling/e2e"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const goodFile = `{
  "version": 1,
  "apiServer": {"address": "127.0.0.1:41233"},
  "pki": {"ca": "/d/ca.pem", "clientCert": "/d/client.pem", "clientKey": "/d/client.key"},
  "nodes": [
    {"name": "node-1", "pid": 4242, "serialLog": "/d/node-1/console.log", "controlPlane": true},
    {"name": "node-2", "pid": 4243, "serialLog": "/d/node-2/console.log", "controlPlane": false}
  ],
  "images": [{"reference": "127.0.0.1:5000/test/entry@` + digest + `"}],
  "futureField": 7
}`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cluster.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestReadClusterFile_ParsesTheRolesTheRootAndTheImage(t *testing.T) {
	c, err := e2e.ReadClusterFile(write(t, goodFile))

	require.NoError(t, err)
	assert.Equal(t, "https://127.0.0.1:41233/apis/compute.gaon.garam.sh/v1alpha1", c.APIRoot)
	assert.Equal(t, e2e.Node{Name: "node-1", PID: 4242, SerialLog: "/d/node-1/console.log", ControlPlane: true}, c.ControlPlane)
	require.Len(t, c.Workers, 1)
	assert.Equal(t, "node-2", c.Workers[0].Name)
	assert.Equal(t, "/d/client.key", c.Key)
	assert.Equal(t, "127.0.0.1:5000/test/entry@"+digest, c.Image)
}

func TestReadClusterFile_TellsTheRolesByTheFlagAndNotByPosition(t *testing.T) {
	swapped := strings.NewReplacer(`"controlPlane": true`, `"controlPlane": false`, `"controlPlane": false`, `"controlPlane": true`).Replace(goodFile)

	c, err := e2e.ReadClusterFile(write(t, swapped))

	require.NoError(t, err)
	assert.Equal(t, "node-2", c.ControlPlane.Name)
	assert.Equal(t, "node-1", c.Workers[0].Name)
}

func TestReadClusterFile_RefusesWhatItCannotRelyOn(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"another version", strings.Replace(goodFile, `"version": 1`, `"version": 2`, 1), "version 2"},
		{"no address", strings.Replace(goodFile, `127.0.0.1:41233`, ``, 1), "no API server address"},
		{"no worker", strings.Replace(goodFile, `,
    {"name": "node-2", "pid": 4243, "serialLog": "/d/node-2/console.log", "controlPlane": false}`, ``, 1), "1 control-plane Nodes and 0 others"},
		{"two control planes", strings.Replace(goodFile, `"controlPlane": false`, `"controlPlane": true`, 1), "2 control-plane Nodes and 0 others"},
		{"an image without a digest", strings.Replace(goodFile, `@`+digest, `:latest`, 1), "no image by digest"},
		{"not JSON", `nodes`, "parse cluster file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e2e.ReadClusterFile(write(t, tt.body))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestReadClusterFile_ReportsAMissingFile(t *testing.T) {
	_, err := e2e.ReadClusterFile(filepath.Join(t.TempDir(), "absent.json"))

	require.ErrorIs(t, err, os.ErrNotExist)
}
