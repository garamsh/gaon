package apiserver_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func secret(name string, data map[string][]byte) *api.Secret {
	return &api.Secret{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecret},
		Metadata: api.ObjectMeta{Name: name},
		Data:     data,
	}
}

func configMap(name string, data map[string]string) *api.ConfigMap {
	return &api.ConfigMap{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindConfigMap},
		Metadata: api.ObjectMeta{Name: name},
		Data:     data,
	}
}

func serviceAccount(name string) *api.ServiceAccount {
	return &api.ServiceAccount{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceAccount},
		Metadata: api.ObjectMeta{Name: name},
	}
}

// randomValue is a value no source file holds, so that finding it in a log
// or an answer proves it came from the request.
func randomValue(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "v-" + hex.EncodeToString(b)
}

// syncBuffer is a log destination that the server's goroutines and the test
// both reach.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSecret_CreateReadListWatchAndDelete(t *testing.T) {
	e := newEnv(t)
	value := []byte(randomValue(t))

	created, err := e.api.PutSecret(ctx(t), secret("db", map[string][]byte{"password": value}))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.ResourceVersion)
	assert.NotEmpty(t, created.Metadata.UID)

	got, err := e.api.GetSecret(ctx(t), "db")
	require.NoError(t, err)
	assert.Equal(t, value, got.Data["password"])
	list, err := e.api.ListSecrets(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindSecretList, list.Kind)

	w, err := e.api.WatchSecrets(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	update := *got
	update.Data = map[string][]byte{"password": []byte("changed")}
	updated, err := e.api.PutSecret(ctx(t), &update)
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventModified, ev.Type)
	assert.Equal(t, []byte("changed"), ev.Object.Data["password"])

	_, err = e.api.DeleteSecret(ctx(t), "db", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.DeleteSecret(ctx(t), "db", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetSecret(ctx(t), "db")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestSecret_IsCheckedWithItsValidate(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutSecret(ctx(t), secret("bad", map[string][]byte{"bad key!": []byte("x")}))
	assert.ErrorIs(t, err, api.ErrInvalid, "a key that is not a data key")
	over := map[string][]byte{"big": make([]byte, api.MaxDataBytes+1)}
	_, err = e.api.PutSecret(ctx(t), secret("big", over))
	assert.ErrorIs(t, err, api.ErrInvalid, "data over the limit")
	_, err = e.api.PutSecret(ctx(t), secret("fine", map[string][]byte{"key": []byte("x")}))
	assert.NoError(t, err, "control: a Secret that validates")
}

func TestSecret_NoValueReachesTheLogOrAnError(t *testing.T) {
	log := &syncBuffer{}
	e := newEnvWith(t, envOptions{log: log})
	value := randomValue(t)

	encoded := base64.StdEncoding.EncodeToString([]byte(value))
	_, err := e.api.PutSecret(ctx(t), secret("db", map[string][]byte{"password": []byte(value)}))
	require.NoError(t, err)
	assert.NotContains(t, log.String(), value, "the log of a create")
	assert.NotContains(t, log.String(), encoded, "the log of a create, as the JSON carries it")

	var failures []string
	_, err = e.api.PutSecret(ctx(t), secret("db", map[string][]byte{"password": []byte(value)}))
	require.ErrorIs(t, err, api.ErrAlreadyExists)
	failures = append(failures, err.Error())
	_, err = e.api.PutSecret(ctx(t), secret("bad", map[string][]byte{"bad key!": []byte(value)}))
	require.ErrorIs(t, err, api.ErrInvalid)
	failures = append(failures, err.Error())
	status, body := e.send(t, "PUT", "/secrets/raw", `{"apiVersion":"`+api.GroupVersion+`","kind":"Secret","metadata":{"name":"raw"},"data":{"k":"`+value+`"},"extra":1}`)
	require.Equal(t, 422, status, "a body with an unknown field is refused")
	failures = append(failures, body)

	for _, f := range failures {
		assert.NotContains(t, f, value, "an error of a failed create")
		assert.NotContains(t, f, encoded, "an error of a failed create, as the JSON carries it")
	}
	assert.NotContains(t, log.String(), value, "the log of a failed create")
	assert.NotContains(t, log.String(), encoded, "the log of a failed create, as the JSON carries it")
}

func TestConfigMap_CreateReadListWatchAndDelete(t *testing.T) {
	e := newEnv(t)

	created, err := e.api.PutConfigMap(ctx(t), configMap("app", map[string]string{"mode": "fast"}))
	require.NoError(t, err)
	got, err := e.api.GetConfigMap(ctx(t), "app")
	require.NoError(t, err)
	assert.Equal(t, "fast", got.Data["mode"])
	list, err := e.api.ListConfigMaps(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindConfigMapList, list.Kind)

	w, err := e.api.WatchConfigMaps(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	update := *got
	update.Data = map[string]string{"mode": "slow"}
	updated, err := e.api.PutConfigMap(ctx(t), &update)
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, "slow", ev.Object.Data["mode"])

	_, err = e.api.DeleteConfigMap(ctx(t), "app", created.Metadata.ResourceVersion)
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.DeleteConfigMap(ctx(t), "app", updated.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetConfigMap(ctx(t), "app")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestConfigMap_IsCheckedWithItsValidate(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutConfigMap(ctx(t), configMap("bad", map[string]string{"bad key!": "x"}))
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = e.api.PutConfigMap(ctx(t), configMap("fine", map[string]string{"key": "x"}))
	assert.NoError(t, err, "control: a ConfigMap that validates")
}

func TestServiceAccount_CreateReadListWatchAndDelete(t *testing.T) {
	e := newEnv(t)

	created, err := e.api.PutServiceAccount(ctx(t), serviceAccount("web"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Metadata.UID)
	got, err := e.api.GetServiceAccount(ctx(t), "web")
	require.NoError(t, err)
	assert.Equal(t, created, got)
	list, err := e.api.ListServiceAccounts(ctx(t))
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, api.KindServiceAccountList, list.Kind)

	w, err := e.api.WatchServiceAccounts(ctx(t), list.Metadata.ResourceVersion)
	require.NoError(t, err)
	defer w.Close()
	_, err = e.api.PutServiceAccount(ctx(t), serviceAccount("db"))
	require.NoError(t, err)
	ev, err := w.Next()
	require.NoError(t, err)
	assert.Equal(t, api.EventAdded, ev.Type)
	assert.Equal(t, "db", ev.Object.Metadata.Name)

	_, err = e.api.DeleteServiceAccount(ctx(t), "web", "1")
	assert.ErrorIs(t, err, api.ErrConflict, "a stale version")
	_, err = e.api.DeleteServiceAccount(ctx(t), "web", created.Metadata.ResourceVersion)
	require.NoError(t, err)
	_, err = e.api.GetServiceAccount(ctx(t), "web")
	assert.ErrorIs(t, err, api.ErrNotFound)
}

func TestServiceAccount_IsCheckedWithItsValidate(t *testing.T) {
	e := newEnv(t)

	_, err := e.api.PutServiceAccount(ctx(t), serviceAccount("Not A Name"))
	assert.ErrorIs(t, err, api.ErrInvalid)
	_, err = e.api.PutServiceAccount(ctx(t), serviceAccount("fine"))
	assert.NoError(t, err, "control: a name that validates")
}
