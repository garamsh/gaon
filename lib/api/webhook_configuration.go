package api

import (
	"crypto/x509"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Operation is a kind of write the API server may send to a webhook.
type Operation string

// The operations. OperationAll matches every operation in a rule.
const (
	OperationCreate Operation = "CREATE"
	OperationUpdate Operation = "UPDATE"
	OperationDelete Operation = "DELETE"
	OperationAll    Operation = "*"
)

// FailurePolicy says what the API server does with a write when a call to a
// webhook fails. The empty value means FailurePolicyFail.
type FailurePolicy string

// The failure policies.
const (
	FailurePolicyFail   FailurePolicy = "Fail"
	FailurePolicyIgnore FailurePolicy = "Ignore"
)

// The bounds of a webhook's timeout, and its default.
const (
	minWebhookTimeoutSeconds     = 1
	maxWebhookTimeoutSeconds     = 30
	defaultWebhookTimeoutSeconds = 10
)

var (
	// webhookName is a domain of at least three lowercase labels.
	webhookName  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?){2,}$`)
	resourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// MutatingWebhookConfiguration registers webhooks that may change a write
// before it is stored.
type MutatingWebhookConfiguration struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Webhooks []Webhook  `json:"webhooks"`
}

// MutatingWebhookConfigurationList is the answer to listing
// MutatingWebhookConfigurations.
type MutatingWebhookConfigurationList struct {
	TypeMeta
	Metadata ListMeta                       `json:"metadata"`
	Items    []MutatingWebhookConfiguration `json:"items"`
}

// ValidatingWebhookConfiguration registers webhooks that may refuse a write
// before it is stored.
type ValidatingWebhookConfiguration struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Webhooks []Webhook  `json:"webhooks"`
}

// ValidatingWebhookConfigurationList is the answer to listing
// ValidatingWebhookConfigurations.
type ValidatingWebhookConfigurationList struct {
	TypeMeta
	Metadata ListMeta                         `json:"metadata"`
	Items    []ValidatingWebhookConfiguration `json:"items"`
}

// Webhook is one out-of-process admission webhook: where it is, which writes
// it is sent and what happens when the call fails. TimeoutSeconds nil means
// the default, 10.
type Webhook struct {
	Name           string              `json:"name"`
	ClientConfig   WebhookClientConfig `json:"clientConfig"`
	Rules          []WebhookRule       `json:"rules"`
	FailurePolicy  FailurePolicy       `json:"failurePolicy,omitempty"`
	TimeoutSeconds *int32              `json:"timeoutSeconds,omitempty"`
}

// WebhookClientConfig says how to reach a webhook: its https URL, and the PEM
// CA bundle that is the only trust the API server places in it. CABundle is
// base64 in JSON, as in Kubernetes.
type WebhookClientConfig struct {
	URL      string `json:"url"`
	CABundle []byte `json:"caBundle"`
}

// WebhookRule selects writes by operation, API group, API version and
// resource. The wildcard "*" matches any value of its field, except in
// Resources: there "*" matches every resource but no subresource, "*/*"
// matches every resource and subresource, and "machines/status",
// "machines/*" and "*/status" match subresources.
type WebhookRule struct {
	Operations  []Operation `json:"operations"`
	APIGroups   []string    `json:"apiGroups"`
	APIVersions []string    `json:"apiVersions"`
	Resources   []string    `json:"resources"`
}

// FailurePolicyOrDefault returns the webhook's failure policy, Fail when unset.
func (w Webhook) FailurePolicyOrDefault() FailurePolicy {
	if w.FailurePolicy == "" {
		return FailurePolicyFail
	}
	return w.FailurePolicy
}

// Timeout returns how long the API server waits for the webhook, 10 seconds
// when unset.
func (w Webhook) Timeout() time.Duration {
	if w.TimeoutSeconds == nil {
		return defaultWebhookTimeoutSeconds * time.Second
	}
	return time.Duration(*w.TimeoutSeconds) * time.Second
}

// Validate checks the identity of c and each of its webhooks.
func (c *MutatingWebhookConfiguration) Validate() error {
	if err := validateIdentity(c.TypeMeta, AdmissionRegistrationGroupVersion, KindMutatingWebhookConfiguration, c.Metadata); err != nil {
		return err
	}
	return validateWebhooks(c.Webhooks)
}

// Validate checks the identity of c and each of its webhooks.
func (c *ValidatingWebhookConfiguration) Validate() error {
	if err := validateIdentity(c.TypeMeta, AdmissionRegistrationGroupVersion, KindValidatingWebhookConfiguration, c.Metadata); err != nil {
		return err
	}
	return validateWebhooks(c.Webhooks)
}

func validateWebhooks(webhooks []Webhook) error {
	seen := map[string]bool{}
	for i, w := range webhooks {
		if err := w.validate(); err != nil {
			return invalid("webhooks[%d]: %v", i, err)
		}
		if seen[w.Name] {
			return invalid("webhooks[%d]: name %q is used by an earlier webhook", i, w.Name)
		}
		seen[w.Name] = true
	}
	return nil
}

func (w Webhook) validate() error {
	if len(w.Name) > maxDefinitionLen || !webhookName.MatchString(w.Name) {
		return invalid("name %q is not a domain of at least three lowercase labels", w.Name)
	}
	if err := w.ClientConfig.validate(); err != nil {
		return invalid("clientConfig: %v", err)
	}
	if len(w.Rules) == 0 {
		return invalid("rules is empty")
	}
	for i, r := range w.Rules {
		if err := r.validate(); err != nil {
			return invalid("rules[%d]: %v", i, err)
		}
	}
	switch w.FailurePolicy {
	case "", FailurePolicyFail, FailurePolicyIgnore:
	default:
		return invalid("failurePolicy is %q, want Fail or Ignore", w.FailurePolicy)
	}
	if t := w.TimeoutSeconds; t != nil && (*t < minWebhookTimeoutSeconds || *t > maxWebhookTimeoutSeconds) {
		return invalid("timeoutSeconds is %d, want %d to %d", *t, minWebhookTimeoutSeconds, maxWebhookTimeoutSeconds)
	}
	return nil
}

func (c WebhookClientConfig) validate() error {
	u, err := url.Parse(c.URL)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "":
		return invalid("url %q is not an https URL with a host", c.URL)
	case u.User != nil:
		return invalid("url carries a user name or password")
	case strings.ContainsAny(c.URL, "?#"):
		return invalid("url carries a query or a fragment")
	}
	if !x509.NewCertPool().AppendCertsFromPEM(c.CABundle) {
		return invalid("caBundle holds no PEM certificate")
	}
	return nil
}

func (r WebhookRule) validate() error {
	if len(r.Operations) == 0 || len(r.APIGroups) == 0 || len(r.APIVersions) == 0 || len(r.Resources) == 0 {
		return invalid("operations, apiGroups, apiVersions and resources each list at least one value")
	}
	for _, op := range r.Operations {
		switch op {
		case OperationCreate, OperationUpdate, OperationDelete, OperationAll:
		default:
			return invalid("operation %q is not one of CREATE, UPDATE, DELETE, *", op)
		}
	}
	for _, v := range append(append([]string{}, r.APIGroups...), r.APIVersions...) {
		if v == "" {
			return invalid("apiGroups or apiVersions holds an empty name")
		}
	}
	for _, res := range r.Resources {
		if !validResourcePattern(res) {
			return invalid("resource %q is not *, */*, a resource, or a resource/subresource with * for either part", res)
		}
	}
	return nil
}

// validResourcePattern accepts "*", or "<part>" and "<part>/<part>" where a
// part is "*" or a lowercase name.
func validResourcePattern(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if p != "*" && !resourceName.MatchString(p) {
			return false
		}
	}
	return true
}
