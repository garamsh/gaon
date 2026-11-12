package api

import (
	"log/slog"
	"time"
)

// The lifetime of a service account token, in seconds.
const (
	DefaultTokenSeconds int64 = 3600
	MinTokenSeconds     int64 = 600
	MaxTokenSeconds     int64 = 86400
)

// ServiceAccountSubjectPrefix starts the subject of a service account's
// token, and the identity RBAC binds.
const ServiceAccountSubjectPrefix = "gaon:serviceaccount:"

// ServiceAccountSubject is the subject of a token of the ServiceAccount name.
func ServiceAccountSubject(name string) string { return ServiceAccountSubjectPrefix + name }

// BoundObject names the object a token is bound to by name and uid. A token
// of a MicroVM that was deleted and created again under the same name is
// refused, because its uid differs.
type BoundObject struct {
	Name string `json:"name"`
	UID  UID    `json:"uid"`
}

// TokenRequestSpec is what a caller asks of
// POST /serviceaccounts/{name}/token. gaon binds a token to a MicroVM only,
// where Kubernetes takes any object.
type TokenRequestSpec struct {
	Audiences         []string    `json:"audiences"`
	ExpirationSeconds int64       `json:"expirationSeconds,omitempty"`
	BoundMicroVM      BoundObject `json:"boundMicroVM"`
}

// TokenRequestStatus is the answer: the signed token and when it expires. A
// token is never logged and never put in an Event.
type TokenRequestStatus struct {
	Token               string     `json:"token,omitempty"`
	ExpirationTimestamp *time.Time `json:"expirationTimestamp,omitempty"`
}

// String formats s without its token.
func (s TokenRequestStatus) String() string { return "TokenRequestStatus{token redacted}" }

// GoString formats s without its token.
func (s TokenRequestStatus) GoString() string { return s.String() }

// LogValue logs s without its token.
func (s TokenRequestStatus) LogValue() slog.Value { return slog.StringValue(s.String()) }

// TokenRequest is the body and the answer of
// POST /serviceaccounts/{name}/token. It is not stored.
type TokenRequest struct {
	TypeMeta
	Spec   TokenRequestSpec   `json:"spec"`
	Status TokenRequestStatus `json:"status"`
}

// Lifetime is the lifetime r asks for, DefaultTokenSeconds when it asks for
// none.
func (s TokenRequestSpec) Lifetime() time.Duration {
	if s.ExpirationSeconds == 0 {
		return time.Duration(DefaultTokenSeconds) * time.Second
	}
	return time.Duration(s.ExpirationSeconds) * time.Second
}

// Validate checks the identity and the spec of r.
func (r *TokenRequest) Validate() error {
	if r.APIVersion != GroupVersion || r.Kind != KindTokenRequest {
		return invalid("apiVersion and kind are %q and %q, want %q and %q", r.APIVersion, r.Kind, GroupVersion, KindTokenRequest)
	}
	if len(r.Spec.Audiences) == 0 {
		return invalid("spec.audiences is empty, want at least one")
	}
	for i, a := range r.Spec.Audiences {
		if a == "" {
			return invalid("spec.audiences[%d] is empty", i)
		}
	}
	if e := r.Spec.ExpirationSeconds; e != 0 && (e < MinTokenSeconds || e > MaxTokenSeconds) {
		return invalid("spec.expirationSeconds is %d, want 0 or %d to %d", e, MinTokenSeconds, MaxTokenSeconds)
	}
	if err := validateObjectName("spec.boundMicroVM.name", r.Spec.BoundMicroVM.Name); err != nil {
		return err
	}
	if r.Spec.BoundMicroVM.UID == "" {
		return invalid("spec.boundMicroVM.uid is empty")
	}
	return nil
}

// TokenClaimsExtension is the claim "gaon.garam.sh": what the token is bound to.
type TokenClaimsExtension struct {
	ServiceAccount BoundObject `json:"serviceaccount"`
	MicroVM        BoundObject `json:"microvm"`
}

// TokenClaims are the claims of a service account token. Signing and keys are
// the API server's; this fixes what the claims say.
type TokenClaims struct {
	Issuer    string               `json:"iss"`
	Subject   string               `json:"sub"`
	Audience  []string             `json:"aud"`
	Expiry    int64                `json:"exp"`
	IssuedAt  int64                `json:"iat"`
	Extension TokenClaimsExtension `json:"gaon.garam.sh"`
}

// NewTokenClaims builds the claims for req of the ServiceAccount sa bound to
// m, issued by issuer at now. It refuses, as Invalid, a request that does not
// validate, a MicroVM that does not name sa and an object with no uid, and as
// Conflict a MicroVM that is not the one bound by name and uid, or that is
// being deleted.
func NewTokenClaims(issuer string, sa *ServiceAccount, m *MicroVM, req *TokenRequest, now time.Time) (*TokenClaims, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if sa.Metadata.UID == "" || m.Metadata.UID == "" {
		return nil, invalid("the service account or the MicroVM has no uid")
	}
	if m.Metadata.Name != req.Spec.BoundMicroVM.Name || m.Metadata.UID != req.Spec.BoundMicroVM.UID {
		return nil, NewAPIError(ReasonConflict, "MicroVM %q is not the one the request binds, its uid differs", m.Metadata.Name)
	}
	if m.Metadata.DeletionTimestamp != nil {
		return nil, NewAPIError(ReasonConflict, "MicroVM %q is being deleted", m.Metadata.Name)
	}
	if m.Spec.ServiceAccountName != sa.Metadata.Name {
		return nil, invalid("MicroVM %q names service account %q, not %q", m.Metadata.Name, m.Spec.ServiceAccountName, sa.Metadata.Name)
	}
	c := &TokenClaims{
		Issuer:   issuer,
		Subject:  ServiceAccountSubject(sa.Metadata.Name),
		Audience: append([]string(nil), req.Spec.Audiences...),
		IssuedAt: now.Unix(),
		Expiry:   now.Add(req.Spec.Lifetime()).Unix(),
		Extension: TokenClaimsExtension{
			ServiceAccount: BoundObject{Name: sa.Metadata.Name, UID: sa.Metadata.UID},
			MicroVM:        BoundObject{Name: m.Metadata.Name, UID: m.Metadata.UID},
		},
	}
	return c, c.Validate()
}

// Validate checks the shape of c: an issuer, the subject of its service
// account, an audience, and a lifetime of at most MaxTokenSeconds. Whether the
// time is now is the verifier's to check.
func (c *TokenClaims) Validate() error {
	if c.Issuer == "" {
		return invalid("iss is empty")
	}
	if c.Extension.ServiceAccount.Name == "" || c.Extension.ServiceAccount.UID == "" {
		return invalid("gaon.garam.sh.serviceaccount names no name or uid")
	}
	if c.Subject != ServiceAccountSubject(c.Extension.ServiceAccount.Name) {
		return invalid("sub is %q, want %q", c.Subject, ServiceAccountSubject(c.Extension.ServiceAccount.Name))
	}
	if len(c.Audience) == 0 {
		return invalid("aud is empty, want at least one")
	}
	for i, a := range c.Audience {
		if a == "" {
			return invalid("aud[%d] is empty", i)
		}
	}
	if c.Extension.MicroVM.Name == "" || c.Extension.MicroVM.UID == "" {
		return invalid("gaon.garam.sh.microvm names no name or uid")
	}
	if c.IssuedAt <= 0 || c.Expiry <= c.IssuedAt || c.Expiry-c.IssuedAt > MaxTokenSeconds {
		return invalid("iat and exp are %d and %d, want exp after iat by at most %d seconds", c.IssuedAt, c.Expiry, MaxTokenSeconds)
	}
	return nil
}
