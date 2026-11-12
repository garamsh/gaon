package api

import "encoding/pem"

// pemCertificateRequest is the PEM block type of a PKCS#10 request.
const pemCertificateRequest = "CERTIFICATE REQUEST"

// The condition types of a CertificateSigningRequest, written by the signer.
// CSR is the short form of the resource's name.
const (
	CSRApproved = "Approved"
	CSRDenied   = "Denied"
)

// CertificateSigningRequest asks the cluster CA for a client certificate. Its
// requester creates it; the signer approves or denies it and records the
// certificate it issues in Status.
type CertificateSigningRequest struct {
	TypeMeta
	Metadata ObjectMeta                      `json:"metadata"`
	Spec     CertificateSigningRequestSpec   `json:"spec"`
	Status   CertificateSigningRequestStatus `json:"status"`
}

// CertificateSigningRequestList is the answer to listing
// CertificateSigningRequests.
type CertificateSigningRequestList struct {
	TypeMeta
	Metadata ListMeta                    `json:"metadata"`
	Items    []CertificateSigningRequest `json:"items"`
}

// CertificateSigningRequestSpec is what the requester asks for. Request is a
// PEM PKCS#10 request. Username is the requester's identity, which the API
// server sets from authentication, replacing whatever the requester sent.
type CertificateSigningRequestSpec struct {
	Request  string `json:"request"`
	Username string `json:"username,omitempty"`
}

// CertificateSigningRequestStatus is the signer's answer: the conditions
// Approved and Denied, and the issued PEM certificate once approved.
type CertificateSigningRequestStatus struct {
	Conditions  []Condition `json:"conditions,omitempty"`
	Certificate string      `json:"certificate,omitempty"`
}

// Validate checks the identity of r and that its request is a PEM block of
// type CERTIFICATE REQUEST.
func (r *CertificateSigningRequest) Validate() error {
	if err := validateIdentity(r.TypeMeta, KindCertificateSigningRequest, r.Metadata); err != nil {
		return err
	}
	block, _ := pem.Decode([]byte(r.Spec.Request))
	if block == nil || block.Type != pemCertificateRequest {
		return invalid("spec.request is not a PEM %s", pemCertificateRequest)
	}
	return nil
}

// ValidateStatus checks that each condition of r's status has a known type
// and a known status.
func (r *CertificateSigningRequest) ValidateStatus() error {
	for _, c := range r.Status.Conditions {
		switch c.Type {
		case CSRApproved, CSRDenied:
		default:
			return invalid("condition type is %q, want Approved or Denied", c.Type)
		}
		if err := c.validate(); err != nil {
			return err
		}
	}
	return nil
}
