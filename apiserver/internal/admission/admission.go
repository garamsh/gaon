package admission

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/garamsh/gaon/lib/api"
)

// maxAnswer bounds the answer of a webhook that is read.
const maxAnswer = 16 << 20

// Hook is one webhook of a configuration.
type Hook struct {
	// Configuration is the name of the configuration that holds the webhook.
	Configuration string
	api.Webhook
}

// Source lists the registered webhooks in the order they are called: the
// configurations by name, the webhooks of each as listed.
type Source interface {
	Mutating(ctx context.Context) ([]Hook, error)
	Validating(ctx context.Context) ([]Hook, error)
}

// Request describes one write. Object is the new object and OldObject the
// stored one, both JSON, either absent as ADR 0010 item 5 says.
type Request struct {
	Kind        api.GroupVersionKind
	Resource    api.GroupVersionResource
	SubResource string
	Operation   api.Operation
	Name        string
	User        api.UserInfo
	Object      json.RawMessage
	OldObject   json.RawMessage
}

// Caller calls the webhooks Source lists.
type Caller struct {
	Source Source
}

// Mutate sends the write to each matching mutating webhook in turn and returns
// the object as their patches left it; each webhook sees the object as the
// patch before it left it. check refuses a patched object that is not a usable
// object of the kind, which makes the call of the webhook that patched it
// failed.
func (c *Caller) Mutate(ctx context.Context, req Request, check func(json.RawMessage) error) (json.RawMessage, error) {
	hooks, err := c.Source.Mutating(ctx)
	if err != nil {
		return nil, err
	}
	return c.run(ctx, hooks, req, true, check)
}

// Validate sends the write, whose object is final, to each matching validating
// webhook in turn.
func (c *Caller) Validate(ctx context.Context, req Request) error {
	hooks, err := c.Source.Validating(ctx)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, hooks, req, false, nil)
	return err
}

func (c *Caller) run(ctx context.Context, hooks []Hook, req Request, mutating bool, check func(json.RawMessage) error) (json.RawMessage, error) {
	obj := req.Object
	for _, h := range hooks {
		if !matches(h.Webhook, req.Resource, req.SubResource, req.Operation) {
			continue
		}
		req.Object = obj
		resp, err := call(ctx, h, req)
		if err == nil && !resp.Allowed {
			return nil, refusal(h, resp)
		}
		if err == nil {
			var next json.RawMessage
			if next, err = consume(resp, req, mutating, check); err == nil && next != nil {
				obj = next
			}
		}
		if err == nil || h.FailurePolicyOrDefault() == api.FailurePolicyIgnore {
			continue
		}
		return nil, api.NewAPIError(api.ReasonInternalError, "the call to admission webhook %q of %s failed: %v", h.Name, h.Configuration, err)
	}
	return obj, nil
}

func refusal(h Hook, resp *api.AdmissionResponse) error {
	if resp.Status != nil && resp.Status.Message != "" {
		return api.NewAPIError(api.ReasonForbidden, "admission webhook %q of %s denied the request: %s", h.Name, h.Configuration, resp.Status.Message)
	}
	return api.NewAPIError(api.ReasonForbidden, "admission webhook %q of %s denied the request", h.Name, h.Configuration)
}

// consume returns the object the allowing response leaves, or nil where it
// leaves the object as it was. Only a mutating webhook may patch, and not the
// delete of an object, which has no new object to patch.
func consume(resp *api.AdmissionResponse, req Request, mutating bool, check func(json.RawMessage) error) (json.RawMessage, error) {
	if len(resp.Patch) == 0 {
		return nil, nil
	}
	if !mutating {
		return nil, errors.New("a validating webhook answered with a patch")
	}
	if req.Operation == api.OperationDelete {
		return nil, errors.New("the webhook answered a DELETE with a patch, which has no object to apply to")
	}
	return applyPatch(req.Object, resp.Patch, check)
}

// call posts the request to the webhook and returns its decoded answer to it.
func call(ctx context.Context, h Hook, req Request) (*api.AdmissionResponse, error) {
	uid, err := newUID()
	if err != nil {
		return nil, err
	}
	review := api.AdmissionReview{
		TypeMeta: api.TypeMeta{APIVersion: api.AdmissionGroupVersion, Kind: api.KindAdmissionReview},
		Request: &api.AdmissionRequest{
			UID: uid, Kind: req.Kind, Resource: req.Resource, SubResource: req.SubResource,
			Name: req.Name, Operation: req.Operation, UserInfo: req.User,
			Object: req.Object, OldObject: req.OldObject,
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		return nil, err
	}
	client, err := clientFor(h)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.Timeout())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.ClientConfig.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the webhook answered status %d, want 200", httpResp.StatusCode)
	}
	answer, err := io.ReadAll(io.LimitReader(httpResp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	var got api.AdmissionReview
	if err := json.Unmarshal(answer, &got); err != nil {
		return nil, fmt.Errorf("decode the answer: %w", err)
	}
	if err := got.ValidateResponseTo(uid); err != nil {
		return nil, err
	}
	return got.Response, nil
}

// clientFor returns a client that trusts the webhook's certificate through its
// caBundle only, and follows no redirect.
func clientFor(h Hook) (*http.Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(h.ClientConfig.CABundle) {
		return nil, errors.New("caBundle holds no certificate")
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func newUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
