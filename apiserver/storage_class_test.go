package apiserver_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/apiserver"
	"github.com/garamsh/gaon/lib/api"
)

func storageClassJSON(t *testing.T, name, provisioner string, allowExpansion bool, resourceVersion string) string {
	t.Helper()
	return mustJSON(t, api.StorageClass{
		TypeMeta:             api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindStorageClass},
		Metadata:             api.ObjectMeta{Name: name, ResourceVersion: api.ResourceVersion(resourceVersion)},
		Provisioner:          provisioner,
		AllowVolumeExpansion: allowExpansion,
	})
}

func decodeClass(t *testing.T, body string) api.StorageClass {
	t.Helper()
	var c api.StorageClass
	require.NoError(t, json.Unmarshal([]byte(body), &c), body)
	return c
}

func TestStorageClass_IsCreatedReadListedChangedAndDeleted(t *testing.T) {
	e := newEnv(t)

	code, body := e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", false, ""))
	require.Equal(t, http.StatusOK, code, body)
	created := decodeClass(t, body)
	assert.Equal(t, "ceph", created.Provisioner)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)

	code, body = e.send(t, http.MethodGet, "/storageclasses/ceph", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, created, decodeClass(t, body))

	code, body = e.send(t, http.MethodGet, "/storageclasses", "")
	require.Equal(t, http.StatusOK, code, body)
	var list api.StorageClassList
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	assert.Equal(t, api.KindStorageClassList, list.Kind)
	names := []string{}
	for _, c := range list.Items {
		names = append(names, c.Metadata.Name)
	}
	assert.ElementsMatch(t, []string{"ceph", api.StorageClassLocal}, names)

	code, body = e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", true, string(created.Metadata.ResourceVersion)))
	require.Equal(t, http.StatusOK, code, body)
	updated := decodeClass(t, body)
	assert.True(t, updated.AllowVolumeExpansion, "allowVolumeExpansion changes")

	code, body = e.send(t, http.MethodDelete, "/storageclasses/ceph?resourceVersion="+string(updated.Metadata.ResourceVersion), "")
	require.Equal(t, http.StatusOK, code, body)
	code, _ = e.send(t, http.MethodGet, "/storageclasses/ceph", "")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestStorageClass_ProvisionerIsImmutableAndTheLocalClassKeepsItsProvisioner(t *testing.T) {
	e := newEnv(t)
	code, body := e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", false, ""))
	require.Equal(t, http.StatusOK, code, body)
	created := decodeClass(t, body)

	code, body = e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "rbd", false, string(created.Metadata.ResourceVersion)))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a provisioner change: %s", body)
	code, body = e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", true, string(created.Metadata.ResourceVersion)))
	assert.Equal(t, http.StatusOK, code, "control: allowVolumeExpansion alone: %s", body)

	code, _ = e.send(t, http.MethodPut, "/storageclasses/other", storageClassJSON(t, "other", api.StorageClassLocal, false, ""))
	assert.Equal(t, http.StatusUnprocessableEntity, code, "another class with provisioner local")
	code, _ = e.send(t, http.MethodPut, "/storageclasses/zfs", storageClassJSON(t, "zfs", "zfs", false, ""))
	assert.Equal(t, http.StatusOK, code, "control: another class with another provisioner")
}

func TestStorageClass_WatchStreamsAClassWhenItIsCreated(t *testing.T) {
	e := newEnv(t)
	code, body := e.send(t, http.MethodGet, "/storageclasses", "")
	require.Equal(t, http.StatusOK, code, body)
	var list api.StorageClassList
	require.NoError(t, json.Unmarshal([]byte(body), &list))

	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, e.base+apiRoot+"/storageclasses?watch=true&resourceVersion="+string(list.Metadata.ResourceVersion), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	code, body = e.send(t, http.MethodPut, "/storageclasses/ceph", storageClassJSON(t, "ceph", "ceph", false, ""))
	require.Equal(t, http.StatusOK, code, body)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, `"ceph"`)
}

func TestStorageClass_LocalExistsAtStartAndIsLeftAsItIs(t *testing.T) {
	endpoint := startEtcd(t)
	first, err := startServer(t, []string{endpoint})
	require.NoError(t, err)

	code, body := first.send(t, http.MethodGet, "/storageclasses/local", "")
	require.Equal(t, http.StatusOK, code, body)
	local := decodeClass(t, body)
	assert.Equal(t, api.StorageClassLocal, local.Provisioner)
	assert.True(t, local.AllowVolumeExpansion)

	code, body = first.send(t, http.MethodPut, "/storageclasses/local", storageClassJSON(t, "local", "local", false, string(local.Metadata.ResourceVersion)))
	require.Equal(t, http.StatusOK, code, body)
	changed := decodeClass(t, body)
	require.NoError(t, first.stop())

	second, err := startServer(t, []string{endpoint})
	require.NoError(t, err)
	code, body = second.send(t, http.MethodGet, "/storageclasses/local", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, changed, decodeClass(t, body), "a restart leaves the stored class unchanged")

	code, _ = second.send(t, http.MethodDelete, "/storageclasses/local?resourceVersion="+string(changed.Metadata.ResourceVersion), "")
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, second.stop())

	third, err := startServer(t, []string{endpoint})
	require.NoError(t, err)
	code, body = third.send(t, http.MethodGet, "/storageclasses/local", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.True(t, decodeClass(t, body).AllowVolumeExpansion, "control: a missing class is created again")
}

func TestStorageClass_OnlyAdministratorsWriteAndNoNodeOrSchedulerReads(t *testing.T) {
	c := newCluster(t)
	addr, _ := c.start(t, apiserver.ModeRBAC)
	url := "https://" + addr + apiRoot + "/storageclasses"
	do := func(w who, method, path, body string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx(t), method, url+path, strings.NewReader(body))
		require.NoError(t, err)
		resp, err := adminHTTP(t, c, w).Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, w := range []who{nodeID("node-1"), schedulerID, strangerID} {
		assert.Equal(t, http.StatusForbidden, do(w, http.MethodGet, "", ""), "list by %s", w.cn)
		assert.Equal(t, http.StatusForbidden, do(w, http.MethodGet, "/local", ""), "get by %s", w.cn)
		assert.Equal(t, http.StatusForbidden, do(w, http.MethodPut, "/x", storageClassJSON(t, "x", "x", false, "")), "put by %s", w.cn)
	}
	assert.Equal(t, http.StatusForbidden, do(controllersID, http.MethodPut, "/x", storageClassJSON(t, "x", "x", false, "")), "controllers do not write")

	assert.Equal(t, http.StatusOK, do(controllersID, http.MethodGet, "/local", ""), "control: controllers read")
	assert.Equal(t, http.StatusOK, do(adminID, http.MethodPut, "/x", storageClassJSON(t, "x", "x", false, "")), "control: admin writes")
}
