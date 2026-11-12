package resourceapi

import (
	"context"
	"errors"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// TokenSigner signs the claims of a service account token.
type TokenSigner interface {
	// Issuer is the "iss" of the tokens it signs.
	Issuer() string
	Sign(c *api.TokenClaims) (string, error)
}

func (s *service) CreateServiceAccountToken(ctx context.Context, name string, req *api.TokenRequest) (*api.TokenRequest, error) {
	if s.signer == nil {
		return nil, errors.New("no token signing key is configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	sa, _, err := serviceAccounts.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	m, _, err := microvms.get(ctx, s.st, req.Spec.BoundMicroVM.Name)
	if err != nil {
		return nil, err
	}
	claims, err := api.NewTokenClaims(s.signer.Issuer(), sa, m, req, s.now())
	if err != nil {
		return nil, err
	}
	token, err := s.signer.Sign(claims)
	if err != nil {
		return nil, err
	}
	expires := time.Unix(claims.Expiry, 0).UTC()
	out := *req
	out.Status = api.TokenRequestStatus{Token: token, ExpirationTimestamp: &expires}
	return &out, nil
}
