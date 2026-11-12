package api_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func ownedBy(uid api.UID) api.OwnerReference {
	return api.OwnerReference{APIVersion: api.GroupVersion, Kind: api.KindMicroVM, Name: "vm", UID: uid}
}

func TestNewUIDIsAVersion4UUIDAndUnique(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[api.UID]bool{}
	for range 100 {
		uid := api.NewUID()
		assert.Regexp(t, pattern, string(uid))
		assert.False(t, seen[uid], "uid %s repeated", uid)
		seen[uid] = true
	}
}

func TestObjectMetaValidateRefusesBadLabelsAndAnnotations(t *testing.T) {
	require.NoError(t, api.ObjectMeta{Name: "x", Labels: map[string]string{"app": "web"}, Annotations: map[string]string{"n": "any text"}}.Validate(), "control")

	assert.ErrorIs(t, api.ObjectMeta{Name: "x", Labels: map[string]string{"bad key": "v"}}.Validate(), api.ErrInvalid)
	assert.ErrorIs(t, api.ObjectMeta{Name: "x", Annotations: map[string]string{"bad key": "v"}}.Validate(), api.ErrInvalid)
}

func TestObjectMetaValidateChecksOwnerReferences(t *testing.T) {
	require.NoError(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{ownedBy("u1")}}.Validate(), "control")

	for name, edit := range map[string]func(*api.OwnerReference){
		"no apiVersion": func(r *api.OwnerReference) { r.APIVersion = "" },
		"no kind":       func(r *api.OwnerReference) { r.Kind = "" },
		"no name":       func(r *api.OwnerReference) { r.Name = "" },
	} {
		ref := ownedBy("u1")
		edit(&ref)
		assert.ErrorIs(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{ref}}.Validate(), api.ErrInvalid, name)
	}
}

func TestObjectMetaValidateAllowsOneControllerOnly(t *testing.T) {
	one := ownedBy("u1")
	one.Controller = true
	other := ownedBy("u2")
	other.Name = "other"
	require.NoError(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{one, other}}.Validate(),
		"control: one controller beside a plain owner")

	other.Controller = true
	assert.ErrorIs(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{one, other}}.Validate(), api.ErrInvalid)
}

func TestObjectMetaValidateCreateRequiresTheOwnerUID(t *testing.T) {
	require.NoError(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{ownedBy("u1")}}.ValidateCreate(), "control")
	require.NoError(t, api.ObjectMeta{Name: "x"}.ValidateCreate(), "control: no owner needs no uid")

	assert.ErrorIs(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{ownedBy("")}}.ValidateCreate(), api.ErrInvalid)
}

func TestObjectMetaValidateUpdateKeepsTheUID(t *testing.T) {
	old := api.ObjectMeta{Name: "x", UID: "u1"}

	require.NoError(t, api.ObjectMeta{Name: "x", UID: "u1"}.ValidateUpdate(old), "control: the same uid")
	require.NoError(t, api.ObjectMeta{Name: "x"}.ValidateUpdate(old), "an update that leaves the uid out keeps the stored one")
	require.NoError(t, api.ObjectMeta{Name: "x", UID: "u1"}.ValidateUpdate(api.ObjectMeta{Name: "x"}),
		"an object stored without a uid takes one")

	assert.ErrorIs(t, api.ObjectMeta{Name: "x", UID: "u2"}.ValidateUpdate(old), api.ErrInvalid)
}

func TestObjectMetaValidateUpdateRequiresTheUIDOfANewOwnerOnly(t *testing.T) {
	kept := ownedBy("")
	old := api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{kept}}

	require.NoError(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{kept}}.ValidateUpdate(old),
		"a reference the object held before uids existed may stay without one")
	require.NoError(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{ownedBy("u9")}}.ValidateUpdate(old),
		"control: a new reference with a uid")

	added := ownedBy("")
	added.Name = "added"
	assert.ErrorIs(t, api.ObjectMeta{Name: "x", OwnerReferences: []api.OwnerReference{kept, added}}.ValidateUpdate(old), api.ErrInvalid)
}

func TestObjectMetaMetadataFieldsAreOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(api.ObjectMeta{Name: "x"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"x"}`, string(b), "an object without the new fields keeps its encoding")

	var m api.ObjectMeta
	require.NoError(t, json.Unmarshal([]byte(`{"name":"x","uid":"u","labels":{"a":"b"},"ownerReferences":[{"apiVersion":"v","kind":"K","name":"n","uid":"o","controller":true,"blockOwnerDeletion":true}]}`), &m))
	assert.Equal(t, api.UID("u"), m.UID)
	assert.Equal(t, map[string]string{"a": "b"}, m.Labels)
	assert.True(t, m.OwnerReferences[0].Controller)
	assert.True(t, m.OwnerReferences[0].BlockOwnerDeletion)
}
