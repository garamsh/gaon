package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestStorageClassValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *api.StorageClass)
		valid  bool
	}{
		{"a third-party class", func(c *api.StorageClass) {}, true},
		{"the local class", func(c *api.StorageClass) {
			c.Metadata.Name = "local"
			c.Provisioner = "local"
		}, true},
		{"wrong kind", func(c *api.StorageClass) { c.Kind = api.KindVolume }, false},
		{"wrong version", func(c *api.StorageClass) { c.APIVersion = "compute.gaon.garam.sh/v1" }, false},
		{"no name", func(c *api.StorageClass) { c.Metadata.Name = "" }, false},
		{"no provisioner", func(c *api.StorageClass) { c.Provisioner = "" }, false},
		{"provisioner in capitals", func(c *api.StorageClass) { c.Provisioner = "Ceph" }, false},
		{"provisioner with a slash", func(c *api.StorageClass) { c.Provisioner = "vendor/ceph" }, false},
		{"provisioner ending in a dash", func(c *api.StorageClass) { c.Provisioner = "ceph-" }, false},
		{"another class named for the local provisioner", func(c *api.StorageClass) { c.Provisioner = "local" }, false},
		{"the local name with another provisioner", func(c *api.StorageClass) { c.Metadata.Name = "local" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClass("fast", "ceph", true)
			tt.mutate(c)
			err := c.Validate()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, api.ErrInvalid)
			}
		})
	}
}

func TestStorageClassValidateUpdate(t *testing.T) {
	old := newClass("fast", "ceph", true)

	flipped := newClass("fast", "ceph", false)
	assert.NoError(t, flipped.ValidateUpdate(old), "allowVolumeExpansion may change")

	moved := newClass("fast", "rbd", true)
	assert.ErrorIs(t, moved.ValidateUpdate(old), api.ErrInvalid, "the provisioner may not")

	broken := newClass("fast", "", true)
	brokenOld := *broken
	assert.ErrorIs(t, broken.ValidateUpdate(&brokenOld), api.ErrInvalid, "an unchanged class must still validate")
}

func TestStorageClassJSON(t *testing.T) {
	b, err := json.Marshal(newClass("fast", "ceph", true))
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":"compute.gaon.garam.sh/v1alpha1","kind":"StorageClass","metadata":{"name":"fast"},"provisioner":"ceph","allowVolumeExpansion":true}`, string(b))

	b, err = json.Marshal(newClass("slow", "ceph", false))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "allowVolumeExpansion", "a class that forbids expansion omits the field")
}
