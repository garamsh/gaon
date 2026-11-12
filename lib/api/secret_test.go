package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func newSecret(name string) *api.Secret {
	return &api.Secret{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindSecret},
		Metadata: api.ObjectMeta{Name: name},
		Data:     map[string][]byte{"token": []byte("s3cr3t-value")},
	}
}

func newConfigMap(name string) *api.ConfigMap {
	return &api.ConfigMap{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindConfigMap},
		Metadata: api.ObjectMeta{Name: name},
		Data:     map[string]string{"app.conf": "x=1"},
	}
}

func newServiceAccount(name string) *api.ServiceAccount {
	return &api.ServiceAccount{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindServiceAccount},
		Metadata: api.ObjectMeta{Name: name},
	}
}

func TestSecretValidate(t *testing.T) {
	tests := map[string]struct {
		mutate func(s *api.Secret)
		valid  bool
	}{
		"complete":                    {func(s *api.Secret) {}, true},
		"no data":                     {func(s *api.Secret) { s.Data = nil }, true},
		"wrong kind":                  {func(s *api.Secret) { s.Kind = api.KindConfigMap }, false},
		"wrong group version":         {func(s *api.Secret) { s.APIVersion = "v1" }, false},
		"empty name":                  {func(s *api.Secret) { s.Metadata.Name = "" }, false},
		"name not a subdomain":        {func(s *api.Secret) { s.Metadata.Name = "Bad_Name" }, false},
		"name of 254 characters":      {func(s *api.Secret) { s.Metadata.Name = strings.Repeat("a", 254) }, false},
		"name of 253 characters":      {func(s *api.Secret) { s.Metadata.Name = strings.Repeat("a", 253) }, true},
		"invalid metadata":            {func(s *api.Secret) { s.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},
		"key with a slash":            {func(s *api.Secret) { s.Data = map[string][]byte{"a/b": nil} }, false},
		"empty key":                   {func(s *api.Secret) { s.Data = map[string][]byte{"": nil} }, false},
		"key is a dot":                {func(s *api.Secret) { s.Data = map[string][]byte{".": nil} }, false},
		"key is two dots":             {func(s *api.Secret) { s.Data = map[string][]byte{"..": nil} }, false},
		"key of 254 characters":       {func(s *api.Secret) { s.Data = map[string][]byte{strings.Repeat("a", 254): nil} }, false},
		"key with dot dash underline": {func(s *api.Secret) { s.Data = map[string][]byte{".a-b_c": nil} }, true},
		"data above the limit":        {func(s *api.Secret) { s.Data = map[string][]byte{"k": bytes.Repeat([]byte("x"), api.MaxDataBytes)} }, false},
		"data at the limit":           {func(s *api.Secret) { s.Data = map[string][]byte{"k": bytes.Repeat([]byte("x"), api.MaxDataBytes-1)} }, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newSecret("creds")
			tt.mutate(s)
			err := s.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestConfigMapValidate(t *testing.T) {
	tests := map[string]struct {
		mutate func(c *api.ConfigMap)
		valid  bool
	}{
		"complete":             {func(c *api.ConfigMap) {}, true},
		"wrong kind":           {func(c *api.ConfigMap) { c.Kind = api.KindSecret }, false},
		"name not a subdomain": {func(c *api.ConfigMap) { c.Metadata.Name = "Bad_Name" }, false},
		"invalid metadata":     {func(c *api.ConfigMap) { c.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},
		"key with a slash":     {func(c *api.ConfigMap) { c.Data = map[string]string{"a/b": ""} }, false},
		"data above the limit": {func(c *api.ConfigMap) { c.Data = map[string]string{"k": strings.Repeat("x", api.MaxDataBytes)} }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := newConfigMap("conf")
			tt.mutate(c)
			err := c.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestServiceAccountValidate(t *testing.T) {
	tests := map[string]struct {
		mutate func(a *api.ServiceAccount)
		valid  bool
	}{
		"complete":                {func(a *api.ServiceAccount) {}, true},
		"wrong kind":              {func(a *api.ServiceAccount) { a.Kind = api.KindSecret }, false},
		"name with a colon":       {func(a *api.ServiceAccount) { a.Metadata.Name = "a:b" }, false},
		"name with an upper-case": {func(a *api.ServiceAccount) { a.Metadata.Name = "Web" }, false},
		"name with a dot":         {func(a *api.ServiceAccount) { a.Metadata.Name = "web.prod" }, true},
		"invalid metadata":        {func(a *api.ServiceAccount) { a.Metadata.Labels = map[string]string{"bad key!": "v"} }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			a := newServiceAccount("web")
			tt.mutate(a)
			err := a.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestSecretValuesAreNeverFormattedLoggedOrNamedInAnError(t *testing.T) {
	s := newSecret("creds")
	const value = "s3cr3t-value"
	encoded := base64.StdEncoding.EncodeToString([]byte(value))

	for name, got := range map[string]string{
		"%v":            fmt.Sprintf("%v", s),
		"%+v":           fmt.Sprintf("%+v", s),
		"%#v":           fmt.Sprintf("%#v", s),
		"%s":            fmt.Sprintf("%s", s),
		"%v of a copy":  fmt.Sprintf("%v", *s),
		"%#v of a copy": fmt.Sprintf("%#v", *s),
		"%v of a list":  fmt.Sprintf("%v", api.SecretList{Items: []api.Secret{*s}}),
	} {
		if name == "%v of a list" {
			assert.NotContains(t, got, value, name)
			assert.NotContains(t, got, "0x73", name)
			continue
		}
		assert.Equal(t, `Secret "creds"`, got, name)
	}

	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).LogAttrs(context.Background(), slog.LevelInfo, "read", slog.Any("secret", *s))
	assert.NotContains(t, logged.String(), encoded, "slog JSON")
	assert.Contains(t, logged.String(), `"secret":"Secret \"creds\""`)

	logged.Reset()
	slog.New(slog.NewTextHandler(&logged, nil)).Info("read", "secret", *s)
	assert.NotContains(t, logged.String(), value, "slog text")
	assert.Contains(t, logged.String(), "creds")

	s.Data["bad key"] = []byte(value)
	err := s.Validate()
	require.ErrorIs(t, err, api.ErrInvalid)
	assert.NotContains(t, err.Error(), value, "an error names the key and not the value")

	s.Data = map[string][]byte{"k": bytes.Repeat([]byte(value), api.MaxDataBytes)}
	err = s.Validate()
	require.ErrorIs(t, err, api.ErrInvalid)
	assert.NotContains(t, err.Error(), value)

	b, err := json.Marshal(newSecret("creds"))
	require.NoError(t, err)
	assert.Contains(t, string(b), encoded, "control: the wire form carries the values, base64")
}

func TestSecretAndConfigMapOmitEmptyData(t *testing.T) {
	s := newSecret("creds")
	s.Data = nil
	b, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"data"`)
}
