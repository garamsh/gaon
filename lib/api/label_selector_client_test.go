package api_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

func TestClientListAndWatchSendTheLabelSelector(t *testing.T) {
	ctx := testContext(t)
	var queries []url.Values
	ts := newRecordingServer(t, func(r *http.Request) { queries = append(queries, r.URL.Query()) })
	sel := mustSelector(t, "tier=front,app in (web,db)")
	c := api.NewClient(ts.URL, nil, api.WithLabelSelector(sel))

	_, err := c.ListMicroVMs(ctx)
	require.NoError(t, err)
	_, err = c.ListClusterRoles(ctx)
	require.NoError(t, err)
	w, err := c.WatchVolumes(ctx, "3")
	require.NoError(t, err)
	_ = w.Close()

	require.Len(t, queries, 3)
	for _, q := range queries {
		assert.Equal(t, sel.String(), q.Get(api.LabelSelectorParam))
	}
	assert.Equal(t, "true", queries[2].Get("watch"), "a watch keeps its own parameters beside the selector")
	assert.Equal(t, "3", queries[2].Get("resourceVersion"))
}

func TestClientWithoutASelectorSendsNone(t *testing.T) {
	ctx := testContext(t)
	var query url.Values
	ts := newRecordingServer(t, func(r *http.Request) { query = r.URL.Query() })

	for name, opts := range map[string][]api.ClientOption{
		"a plain client":     nil,
		"the empty selector": {api.WithLabelSelector(nil)},
	} {
		_, err := api.NewClient(ts.URL, nil, opts...).ListMicroVMs(ctx)
		require.NoError(t, err)
		assert.False(t, query.Has(api.LabelSelectorParam), name)
	}

	_, err := api.NewClient(ts.URL, nil, api.WithLabelSelector(mustSelector(t, "app=web"))).ListMicroVMs(ctx)
	require.NoError(t, err)
	assert.Equal(t, "app=web", query.Get(api.LabelSelectorParam), "control")
}

func TestCustomResourceClientListSendsTheLabelSelector(t *testing.T) {
	ctx := testContext(t)
	var query url.Values
	ts := newRecordingServer(t, func(r *http.Request) { query = r.URL.Query() })
	c, err := api.NewCustomResourceClient(ts.URL, nil, widgetType)
	require.NoError(t, err)

	_, err = c.List(ctx)
	require.NoError(t, err)
	assert.False(t, query.Has(api.LabelSelectorParam))

	sel := mustSelector(t, "app=web")
	sc, err := api.NewCustomResourceClient(ts.URL, nil, widgetType, api.WithLabelSelector(sel))
	require.NoError(t, err)
	_, err = sc.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, "app=web", query.Get(api.LabelSelectorParam))
}
