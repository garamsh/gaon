package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags_ReadsTheConfigFileAndRefusesABadOne(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "scheduler.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	const header = "apiVersion: scheduler.gaon.garam.sh/v1alpha1\nkind: SchedulerConfiguration\n"
	tests := []struct {
		name string
		path string
	}{
		{"unknown field", write(header + "bogus: 1\n")},
		{"unknown plugin", write(header + "scorePlugins:\n  - name: Nope\n")},
		{"weight below 1", write(header + "scorePlugins:\n  - name: LeastAllocated\n    weight: 0\n")},
		{"extender that is http to a non-loopback address", write(header + "extenders:\n  - urlPrefix: http://10.0.0.5:1/x\n    filterVerb: f\n")},
		{"file that does not exist", filepath.Join(t.TempDir(), "none.yaml")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-config", tt.path}, io.Discard)

			assert.Error(t, err)
		})
	}

	valid := write(header + "scorePlugins:\n  - name: LeastAllocated\n    weight: 2\n")
	_, err := parseFlags([]string{"-api", "http://127.0.0.1:1", "-config", valid}, io.Discard)
	assert.NoError(t, err, "control: a valid file is accepted")
	_, err = parseFlags([]string{"-api", "http://127.0.0.1:1"}, io.Discard)
	assert.NoError(t, err, "control: no file is accepted")
}
