package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// maxErrorBody bounds how much of a failed response is read.
const maxErrorBody = 1 << 20

// openSubresource sends one request to a subresource of the MicroVM name and
// returns the response of a 200. The caller closes the body. Any other answer
// is returned as the *api.APIError it carries, so that NotRunning and
// Unavailable reach the user by reason.
func openSubresource(ctx context.Context, cl *cluster, method, name, sub string, query url.Values, body []byte) (*http.Response, error) {
	target := strings.TrimSuffix(cl.server, "/") + "/microvms/" + url.PathEscape(name) + "/" + sub
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := cl.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	var e api.APIError
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&e); err != nil || e.Reason == "" {
		return nil, fmt.Errorf("unexpected response %s", resp.Status)
	}
	return nil, &e
}
