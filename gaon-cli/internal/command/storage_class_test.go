package command_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/gaon-cli/internal/command"
	"github.com/garamsh/gaon/lib/api"
)

const storageClassYAML = `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: StorageClass
metadata:
  name: fast
provisioner: ceph
allowVolumeExpansion: true
`

func seedStorageClasses(f *fakeServer) {
	f.seed("/storageclasses/local", api.StorageClass{
		TypeMeta:    api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:    api.ObjectMeta{Name: api.StorageClassLocal},
		Provisioner: api.StorageClassLocal,
	})
	f.seed("/storageclasses/fast", api.StorageClass{
		TypeMeta:             api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:             api.ObjectMeta{Name: "fast"},
		Provisioner:          "ceph",
		AllowVolumeExpansion: true,
	})
}

func TestGet_ShowsEachStorageClassWithItsProvisionerAndExpansion(t *testing.T) {
	f := newFakeServer(t)
	seedStorageClasses(f)

	stdout, stderr, code := execute(t, f, "get", "storageclasses")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Contains(t, stdout, "NAME")
	assert.Contains(t, stdout, "PROVISIONER")
	assert.Contains(t, stdout, "ALLOW-VOLUME-EXPANSION")
	assert.Regexp(t, `(?m)^fast\s+ceph\s+true$`, stdout)
	assert.Regexp(t, `(?m)^local\s+local\s+false$`, stdout, "a class that forbids growth reads false, not blank")

	stdout, stderr, code = execute(t, f, "get", "storageclass", "fast")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Regexp(t, `(?m)^fast\s+ceph\s+true$`, stdout)
	assert.NotContains(t, stdout, "local", "one class is read by its own path")
}

func TestDescribe_ShowsAStorageClass(t *testing.T) {
	f := newFakeServer(t)
	seedStorageClasses(f)

	stdout, stderr, code := execute(t, f, "describe", "storageclass", "fast")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Regexp(t, `(?m)^Provisioner:\s+ceph$`, stdout)
	assert.Regexp(t, `(?m)^Allow volume expansion:\s+true$`, stdout)
}

func TestApply_CreatesAStorageClassAndReportsItUnchangedOnRerun(t *testing.T) {
	f := newFakeServer(t)
	file := writeFile(t, storageClassYAML)

	stdout, stderr, code := execute(t, f, "apply", "-f", file)
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "storageclass/fast created\n", stdout)
	stored := load[api.StorageClass](f, "/storageclasses/fast")
	assert.Equal(t, "ceph", stored.Provisioner)
	assert.True(t, stored.AllowVolumeExpansion)

	writesBefore := f.writes()
	stdout, stderr, code = execute(t, f, "apply", "-f", file)
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "storageclass/fast unchanged\n", stdout)
	assert.Equal(t, writesBefore, f.writes(), "an unchanged class is not sent")

	stdout, stderr, code = execute(t, f, "apply", "-f", writeFile(t, `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: StorageClass
metadata:
  name: fast
provisioner: ceph
`))
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "storageclass/fast configured\n", stdout)
	assert.False(t, load[api.StorageClass](f, "/storageclasses/fast").AllowVolumeExpansion)
}

func TestApply_RefusesAStorageClassWhoseProvisionerBreaksTheLocalRule(t *testing.T) {
	f := newFakeServer(t)
	bad := `apiVersion: compute.gaon.garam.sh/v1alpha1
kind: StorageClass
metadata:
  name: local
provisioner: ceph
`
	_, stderr, code := execute(t, f, "apply", "-f", writeFile(t, bad))
	assert.NotEqual(t, command.ExitOK, code)
	assert.Contains(t, stderr, "belongs to the class")
	assert.Zero(t, f.writes(), "a document that fails validation is not sent")
}

func TestDelete_RemovesAStorageClass(t *testing.T) {
	f := newFakeServer(t)
	seedStorageClasses(f)

	stdout, stderr, code := execute(t, f, "delete", "storageclass", "fast")
	require.Equal(t, command.ExitOK, code, stderr)
	assert.Equal(t, "storageclass/fast deleted\n", stdout)
	assert.Contains(t, f.seen(), "DELETE /storageclasses/fast")
}
