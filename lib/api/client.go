package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 1 << 20

// Client calls the resource API. Every write is a compare-and-swap on the
// ResourceVersion the object or request carries; an empty one creates.
type Client struct {
	root string
	http *http.Client
}

// NewClient returns a Client for the API whose group and version root is
// root. A nil httpClient means http.DefaultClient. A watch is one long
// response, so httpClient must not set a Timeout.
func NewClient(root string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{root: strings.TrimSuffix(root, "/"), http: httpClient}
}

// PutMachine creates m, or changes its spec when m carries the ResourceVersion
// it was read at (PUT /machines/{name}).
func (c *Client) PutMachine(ctx context.Context, m *Machine) (*Machine, error) {
	return put(ctx, c, "/machines/"+url.PathEscape(m.Metadata.Name), m)
}

// BindMachine binds a Machine to a node and sets its epoch
// (POST /machines/{name}/binding).
func (c *Client) BindMachine(ctx context.Context, name string, b Binding) (*Machine, error) {
	return call[Machine](ctx, c, http.MethodPost, "/machines/"+url.PathEscape(name)+"/binding", nil, b)
}

// PutMachineStatus writes m's status (PUT /machines/{name}/status).
func (c *Client) PutMachineStatus(ctx context.Context, m *Machine) (*Machine, error) {
	return put(ctx, c, "/machines/"+url.PathEscape(m.Metadata.Name)+"/status", m)
}

// DeleteMachine sets the deletionTimestamp of a Machine, if it is still at
// version rv (DELETE /machines/{name}).
func (c *Client) DeleteMachine(ctx context.Context, name string, rv ResourceVersion) (*Machine, error) {
	if rv == "" {
		return nil, errors.New("delete machine: resource version is empty")
	}
	return call[Machine](ctx, c, http.MethodDelete, "/machines/"+url.PathEscape(name), url.Values{"resourceVersion": {string(rv)}}, nil)
}

// GetMachine returns the named Machine (GET /machines/{name}).
func (c *Client) GetMachine(ctx context.Context, name string) (*Machine, error) {
	return call[Machine](ctx, c, http.MethodGet, "/machines/"+url.PathEscape(name), nil, nil)
}

// ListMachines returns every Machine and the version to watch from
// (GET /machines).
func (c *Client) ListMachines(ctx context.Context) (*MachineList, error) {
	return call[MachineList](ctx, c, http.MethodGet, "/machines", nil, nil)
}

// WatchMachines streams the changes made after version rv
// (GET /machines?watch=true). When rv has expired, Next returns ErrExpired:
// list again and watch from the list's version.
func (c *Client) WatchMachines(ctx context.Context, rv ResourceVersion) (*Watch[Machine], error) {
	return watch[Machine](ctx, c, "/machines", KindMachine, rv)
}

// PutNode creates n, or changes its spec when n carries the ResourceVersion
// it was read at (PUT /nodes/{name}).
func (c *Client) PutNode(ctx context.Context, n *Node) (*Node, error) {
	return put(ctx, c, "/nodes/"+url.PathEscape(n.Metadata.Name), n)
}

// PutNodeStatus writes n's status (PUT /nodes/{name}/status).
func (c *Client) PutNodeStatus(ctx context.Context, n *Node) (*Node, error) {
	return put(ctx, c, "/nodes/"+url.PathEscape(n.Metadata.Name)+"/status", n)
}

// GetNode returns the named Node (GET /nodes/{name}).
func (c *Client) GetNode(ctx context.Context, name string) (*Node, error) {
	return call[Node](ctx, c, http.MethodGet, "/nodes/"+url.PathEscape(name), nil, nil)
}

// ListNodes returns every Node and the version to watch from (GET /nodes).
func (c *Client) ListNodes(ctx context.Context) (*NodeList, error) {
	return call[NodeList](ctx, c, http.MethodGet, "/nodes", nil, nil)
}

// WatchNodes streams the changes made after version rv
// (GET /nodes?watch=true), as WatchMachines does.
func (c *Client) WatchNodes(ctx context.Context, rv ResourceVersion) (*Watch[Node], error) {
	return watch[Node](ctx, c, "/nodes", KindNode, rv)
}

// GetLease returns the named Lease (GET /leases/{name}).
func (c *Client) GetLease(ctx context.Context, name string) (*Lease, error) {
	return call[Lease](ctx, c, http.MethodGet, "/leases/"+url.PathEscape(name), nil, nil)
}

// ListLeases returns every Lease and the version to watch from (GET /leases).
func (c *Client) ListLeases(ctx context.Context) (*LeaseList, error) {
	return call[LeaseList](ctx, c, http.MethodGet, "/leases", nil, nil)
}

// WatchLeases streams the changes made after version rv
// (GET /leases?watch=true), as WatchMachines does.
func (c *Client) WatchLeases(ctx context.Context, rv ResourceVersion) (*Watch[Lease], error) {
	return watch[Lease](ctx, c, "/leases", KindLease, rv)
}

// PutVolumeStatus writes v's status (PUT /volumes/{name}/status).
func (c *Client) PutVolumeStatus(ctx context.Context, v *Volume) (*Volume, error) {
	return put(ctx, c, "/volumes/"+url.PathEscape(v.Metadata.Name)+"/status", v)
}

// PutLease creates l, or renews it when l carries the ResourceVersion it was
// read at (PUT /leases/{name}).
func (c *Client) PutLease(ctx context.Context, l *Lease) (*Lease, error) {
	return put(ctx, c, "/leases/"+url.PathEscape(l.Metadata.Name), l)
}

func watch[T any](ctx context.Context, c *Client, path, kind string, rv ResourceVersion) (*Watch[T], error) {
	if rv == "" {
		return nil, fmt.Errorf("watch %s: resource version is empty", path)
	}
	query := url.Values{"watch": {"true"}, "resourceVersion": {string(rv)}}
	resp, err := c.send(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return newWatch[T](kind, resp.Body), nil
}

func put[T any](ctx context.Context, c *Client, path string, in *T) (*T, error) {
	return call[T](ctx, c, http.MethodPut, path, nil, in)
}

func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, in any) (*T, error) {
	var out T
	if err := c.do(ctx, method, path, query, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do sends one request and decodes a successful JSON answer into out.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("%s %s: encode request: %w", method, path, err)
		}
	}
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return decodeError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	target := c.root + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// decodeError returns the APIError in a failed response, or an opaque error
// naming its status when the body holds none.
func decodeError(resp *http.Response) error {
	var e APIError
	err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&e)
	if err != nil || e.Reason == "" {
		return fmt.Errorf("%s %s: unexpected response %s", resp.Request.Method, resp.Request.URL.Path, resp.Status)
	}
	return &e
}
