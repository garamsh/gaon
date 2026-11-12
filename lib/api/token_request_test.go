package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

var tokenNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func newTokenRequest() *api.TokenRequest {
	return &api.TokenRequest{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindTokenRequest},
		Spec: api.TokenRequestSpec{
			Audiences:    []string{"openbao"},
			BoundMicroVM: api.BoundObject{Name: "m1", UID: "mvm-uid"},
		},
	}
}

func tokenFixtures() (*api.ServiceAccount, *api.MicroVM) {
	sa := newServiceAccount("web")
	sa.Metadata.UID = "sa-uid"
	m := newMicroVM("m1")
	m.Metadata.UID = "mvm-uid"
	m.Spec.ServiceAccountName = "web"
	return sa, m
}

func TestTokenRequestValidate(t *testing.T) {
	tests := map[string]struct {
		mutate func(r *api.TokenRequest)
		valid  bool
	}{
		"complete":                   {func(r *api.TokenRequest) {}, true},
		"wrong kind":                 {func(r *api.TokenRequest) { r.Kind = api.KindSecret }, false},
		"wrong group version":        {func(r *api.TokenRequest) { r.APIVersion = "v1" }, false},
		"no audience":                {func(r *api.TokenRequest) { r.Spec.Audiences = nil }, false},
		"empty audience":             {func(r *api.TokenRequest) { r.Spec.Audiences = []string{"a", ""} }, false},
		"expiration below the floor": {func(r *api.TokenRequest) { r.Spec.ExpirationSeconds = api.MinTokenSeconds - 1 }, false},
		"expiration at the floor":    {func(r *api.TokenRequest) { r.Spec.ExpirationSeconds = api.MinTokenSeconds }, true},
		"expiration above the cap":   {func(r *api.TokenRequest) { r.Spec.ExpirationSeconds = api.MaxTokenSeconds + 1 }, false},
		"expiration at the cap":      {func(r *api.TokenRequest) { r.Spec.ExpirationSeconds = api.MaxTokenSeconds }, true},
		"expiration negative":        {func(r *api.TokenRequest) { r.Spec.ExpirationSeconds = -1 }, false},
		"bound MicroVM with no name": {func(r *api.TokenRequest) { r.Spec.BoundMicroVM.Name = "" }, false},
		"bound MicroVM with no uid":  {func(r *api.TokenRequest) { r.Spec.BoundMicroVM.UID = "" }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r := newTokenRequest()
			tt.mutate(r)
			err := r.Validate()
			if tt.valid {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, api.ErrInvalid)
		})
	}
}

func TestNewTokenClaims(t *testing.T) {
	sa, m := tokenFixtures()
	c, err := api.NewTokenClaims("https://gaon.example", sa, m, newTokenRequest(), tokenNow)
	require.NoError(t, err)
	assert.Equal(t, "https://gaon.example", c.Issuer)
	assert.Equal(t, "gaon:serviceaccount:web", c.Subject)
	assert.Equal(t, []string{"openbao"}, c.Audience)
	assert.Equal(t, tokenNow.Unix(), c.IssuedAt)
	assert.Equal(t, tokenNow.Add(time.Hour).Unix(), c.Expiry, "no expirationSeconds is an hour")
	assert.Equal(t, api.BoundObject{Name: "web", UID: "sa-uid"}, c.Extension.ServiceAccount)
	assert.Equal(t, api.BoundObject{Name: "m1", UID: "mvm-uid"}, c.Extension.MicroVM)

	b, err := json.Marshal(c)
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`{"iss":"https://gaon.example","sub":"gaon:serviceaccount:web","aud":["openbao"],"exp":%d,"iat":%d,
		"gaon.garam.sh":{"serviceaccount":{"name":"web","uid":"sa-uid"},"microvm":{"name":"m1","uid":"mvm-uid"}}}`, c.Expiry, c.IssuedAt), string(b))

	req := newTokenRequest()
	req.Spec.ExpirationSeconds = 900
	c, err = api.NewTokenClaims("i", sa, m, req, tokenNow)
	require.NoError(t, err)
	assert.Equal(t, tokenNow.Add(15*time.Minute).Unix(), c.Expiry)
}

func TestNewTokenClaimsRefusals(t *testing.T) {
	tests := map[string]struct {
		mutate func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest)
		want   error
	}{
		"MicroVM recreated under the same name": {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { m.Metadata.UID = "new-uid" }, api.ErrConflict},
		"request binds another name":            {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { r.Spec.BoundMicroVM.Name = "m2" }, api.ErrConflict},
		"MicroVM being deleted": {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) {
			now := tokenNow
			m.Metadata.DeletionTimestamp = &now
		}, api.ErrConflict},
		"MicroVM names another service account": {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { m.Spec.ServiceAccountName = "other" }, api.ErrInvalid},
		"MicroVM names no service account":      {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { m.Spec.ServiceAccountName = "" }, api.ErrInvalid},
		"service account has no uid":            {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { sa.Metadata.UID = "" }, api.ErrInvalid},
		"MicroVM has no uid": {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) {
			m.Metadata.UID = ""
			r.Spec.BoundMicroVM.UID = "x"
		}, api.ErrInvalid},
		"invalid request": {func(sa *api.ServiceAccount, m *api.MicroVM, r *api.TokenRequest) { r.Spec.Audiences = nil }, api.ErrInvalid},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sa, m := tokenFixtures()
			r := newTokenRequest()
			tt.mutate(sa, m, r)
			c, err := api.NewTokenClaims("i", sa, m, r, tokenNow)
			assert.ErrorIs(t, err, tt.want)
			assert.Nil(t, c)
		})
	}
}

func TestTokenClaimsValidate(t *testing.T) {
	good := func() *api.TokenClaims {
		sa, m := tokenFixtures()
		c, err := api.NewTokenClaims("i", sa, m, newTokenRequest(), tokenNow)
		require.NoError(t, err)
		return c
	}
	tests := map[string]struct {
		mutate func(c *api.TokenClaims)
		valid  bool
	}{
		"as built":                   {func(c *api.TokenClaims) {}, true},
		"no issuer":                  {func(c *api.TokenClaims) { c.Issuer = "" }, false},
		"subject not the account's":  {func(c *api.TokenClaims) { c.Subject = "gaon:serviceaccount:other" }, false},
		"subject without the prefix": {func(c *api.TokenClaims) { c.Subject = "web" }, false},
		"no audience":                {func(c *api.TokenClaims) { c.Audience = nil }, false},
		"empty audience":             {func(c *api.TokenClaims) { c.Audience = []string{""} }, false},
		"no issued-at":               {func(c *api.TokenClaims) { c.IssuedAt, c.Expiry = 0, 100 }, false},
		"expiry at issued-at":        {func(c *api.TokenClaims) { c.Expiry = c.IssuedAt }, false},
		"lifetime above the cap":     {func(c *api.TokenClaims) { c.Expiry = c.IssuedAt + api.MaxTokenSeconds + 1 }, false},
		"lifetime at the cap":        {func(c *api.TokenClaims) { c.Expiry = c.IssuedAt + api.MaxTokenSeconds }, true},
		"account with no name": {func(c *api.TokenClaims) {
			c.Extension.ServiceAccount.Name = ""
			c.Subject = api.ServiceAccountSubjectPrefix
		}, false},
		"account with no uid":  {func(c *api.TokenClaims) { c.Extension.ServiceAccount.UID = "" }, false},
		"MicroVM with no name": {func(c *api.TokenClaims) { c.Extension.MicroVM.Name = "" }, false},
		"MicroVM with no uid":  {func(c *api.TokenClaims) { c.Extension.MicroVM.UID = "" }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := good()
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

func TestTokenIsNeverFormattedOrLogged(t *testing.T) {
	const token = "eyJhbGciOi.payload.signature"
	r := newTokenRequest()
	r.Status.Token = token
	for name, got := range map[string]string{
		"%v":                fmt.Sprintf("%v", r.Status),
		"%+v":               fmt.Sprintf("%+v", r.Status),
		"%#v":               fmt.Sprintf("%#v", r.Status),
		"%v of the request": fmt.Sprintf("%v", r),
	} {
		if name == "%v of the request" {
			assert.NotContains(t, got, token, name)
			continue
		}
		assert.Equal(t, "TokenRequestStatus{token redacted}", got, name)
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("issued", "status", r.Status)
	assert.NotContains(t, logged.String(), token)
	assert.Contains(t, logged.String(), "token redacted")

	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(b), token), "control: the wire form carries the token")
}
