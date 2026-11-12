package csrsigner_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/controllers/internal/csrsigner"
)

func TestLoadCA_RefusesFilesThatDoNotLoadOrDoNotMatch(t *testing.T) {
	ca := newTestCA(t, now.Add(time.Hour))
	other := newTestCA(t, now.Add(time.Hour))
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("not pem"), 0o600))
	tests := []struct {
		name      string
		cert, key string
	}{
		{"missing cert", filepath.Join(dir, "none.pem"), ca.keyFile},
		{"missing key", ca.certFile, filepath.Join(dir, "none.pem")},
		{"cert is not PEM", garbage, ca.keyFile},
		{"key is not PEM", ca.certFile, garbage},
		{"key of another CA", ca.certFile, other.keyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := csrsigner.LoadCA(tt.cert, tt.key)

			assert.Error(t, err)
			assert.Nil(t, got)
		})
	}
	got, err := csrsigner.LoadCA(ca.certFile, ca.keyFile)
	assert.NoError(t, err, "control: a certificate with its own key loads")
	assert.NotNil(t, got)
}
