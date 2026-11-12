// Package csrsigner approves the CertificateSigningRequests of Node Agents and
// signs them with the cluster CA.
package csrsigner

import (
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// RetryDelay is how long the signer waits before it lists again after a
// failed list, watch or write.
const RetryDelay = 5 * time.Second

// clockSkew is how far before the signing time a certificate's NotBefore is
// set, for clock skew between Nodes.
const clockSkew = 5 * time.Minute

// usernamePrefix starts the username of a request made with a bootstrap token.
const usernamePrefix = "bootstrap:"

// The reasons of the Denied condition, one for each rule of the approval rule.
const (
	reasonInvalidRequest = "InvalidRequest"
	reasonInvalidSubject = "InvalidSubject"
	reasonDisallowedExt  = "DisallowedExtension"
	reasonUnauthorized   = "UnauthorizedRequester"
)

// reasonApproved is the reason of the Approved condition.
const reasonApproved = "Approved"

// Config holds what the signer is started with.
type Config struct {
	// CA signs the certificates.
	CA *CA
	// Duration is the validity of a signed certificate, before it is capped at
	// the end of the CA certificate's own validity.
	Duration time.Duration
	// Now returns the signing time.
	Now func() time.Time
}

// decided reports whether r carries an Approved or a Denied condition.
func decided(r *api.CertificateSigningRequest) bool {
	for _, c := range r.Status.Conditions {
		if c.Type == api.CSRApproved || c.Type == api.CSRDenied {
			return true
		}
	}
	return false
}
