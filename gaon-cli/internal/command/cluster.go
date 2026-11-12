package command

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// cluster is how one command reaches the API server: the --server URL, which
// is the compute group's root, and the HTTP client. The root of every other
// group is derived from it.
type cluster struct {
	server string
	http   *http.Client

	definitions     []api.CustomResourceDefinition
	definitionsRead bool
}

// base is the address of the API server, the --server URL without the
// compute group's root.
func (cl *cluster) base() (string, error) {
	base, ok := strings.CutSuffix(strings.TrimSuffix(cl.server, "/"), "/apis/"+api.GroupVersion)
	if !ok {
		return "", usagef("--server %q does not end in /apis/%s, so the root of another API group cannot be derived from it", cl.server, api.GroupVersion)
	}
	return base, nil
}

// client returns the client of an API group. The compute group is the
// --server URL as given.
func (cl *cluster) client(group string) (*api.Client, error) {
	if group == api.Group {
		return api.NewClient(cl.server, cl.http), nil
	}
	base, err := cl.base()
	if err != nil {
		return nil, err
	}
	return api.NewClient(base+"/apis/"+group+"/"+api.Version, cl.http), nil
}

// established lists the definitions the API server serves, read once per
// command. A server with no definitions path has none.
func (cl *cluster) established(ctx context.Context) ([]api.CustomResourceDefinition, error) {
	if cl.definitionsRead {
		return cl.definitions, nil
	}
	c, err := cl.client(api.APIExtensionsGroup)
	if err != nil {
		return nil, err
	}
	list, err := c.ListCustomResourceDefinitions(ctx)
	switch {
	case errors.Is(err, api.ErrNotFound):
		list = &api.CustomResourceDefinitionList{}
	case err != nil:
		return nil, err
	}
	for _, d := range list.Items {
		for _, cond := range d.Status.Conditions {
			if cond.Type == api.CRDEstablished && cond.Status == api.ConditionTrue {
				cl.definitions = append(cl.definitions, d)
			}
		}
	}
	cl.definitionsRead = true
	return cl.definitions, nil
}
