package resourceapi

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// PutCertificateSigningRequest creates r with username as spec.username and an
// empty status, whatever r carries. A request carrying a version is Invalid,
// because the spec is immutable and there is no other write to it.
func (s *service) PutCertificateSigningRequest(ctx context.Context, name, username string, r *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error) {
	if err := checkName(name, r.Metadata.Name); err != nil {
		return nil, err
	}
	if r.Metadata.ResourceVersion != "" {
		// Refused first because no stored request is read for a write that
		// is never accepted; a patch cannot change the version.
		return nil, api.NewAPIError(api.ReasonInvalid, "the spec of certificatesigningrequest %q is immutable", name)
	}
	if err := mutate(ctx, s, certificateSigningRequests, "", api.OperationCreate, r); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return admittedPut(ctx, s, certificateSigningRequests, r, func() error {
		r.Spec.Username = username
		r.Status = api.CertificateSigningRequestStatus{}
		return nil
	}, func(*api.CertificateSigningRequest) error { return nil })
}

// PutCertificateSigningRequestStatus replaces the status of the stored request
// next was read at.
func (s *service) PutCertificateSigningRequestStatus(ctx context.Context, name string, next *api.CertificateSigningRequest) (*api.CertificateSigningRequest, error) {
	if err := checkName(name, next.Metadata.Name); err != nil {
		return nil, err
	}
	if err := mutate(ctx, s, certificateSigningRequests, "status", api.OperationUpdate, next); err != nil {
		return nil, err
	}
	r, rev, err := certificateSigningRequests.get(ctx, s.st, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(certificateSigningRequests.plural, name, next.Metadata.ResourceVersion, r.Metadata.ResourceVersion); err != nil {
		return nil, err
	}
	if err := next.ValidateStatus(); err != nil {
		return nil, err
	}
	old := *r
	r.Status = next.Status
	if err := validate(ctx, s, certificateSigningRequests, "status", api.OperationUpdate, r, &old); err != nil {
		return nil, err
	}
	return r, certificateSigningRequests.update(ctx, s.st, r, rev)
}
