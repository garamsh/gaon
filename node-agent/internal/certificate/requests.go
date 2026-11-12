package certificate

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// Requests is the part of the resource API the certificate flow uses.
type Requests interface {
	PutCertificateSigningRequest(ctx context.Context, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error)
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, error)
}
