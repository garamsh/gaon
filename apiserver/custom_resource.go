package apiserver

import (
	"context"
	"net/http"

	"github.com/garamsh/gaon/apiserver/internal/resourceapi"
	"github.com/garamsh/gaon/lib/api"
)

// customPathOf is the kind a request to a registered kind's path addresses.
func customPathOf(r *http.Request) resourceapi.CustomPath {
	return resourceapi.CustomPath{Group: r.PathValue("group"), Version: r.PathValue("version"), Plural: r.PathValue("plural")}
}

// putCustomObject writes the object of the body, or its status.
func (h *handler) putCustomObject(call func(context.Context, resourceapi.CustomPath, string, *api.CustomObject) (*api.CustomObject, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in api.CustomObject
		if !h.decode(w, r, &in) {
			return
		}
		out, err := call(r.Context(), customPathOf(r), r.PathValue("name"), &in)
		h.respond(w, r, out, err)
	}
}

func (h *handler) deleteCustomObject(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rv := api.ResourceVersion(r.URL.Query().Get("resourceVersion"))
		out, err := svc.DeleteCustomObject(r.Context(), customPathOf(r), r.PathValue("name"), rv)
		h.respond(w, r, out, err)
	}
}

func (h *handler) getCustomObject(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.GetCustomObject(r.Context(), customPathOf(r), r.PathValue("name"))
		h.respond(w, r, out, err)
	}
}

func (h *handler) listOrWatchCustomObjects(svc resourceapi.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := customPathOf(r)
		listOrWatch(h,
			func(ctx context.Context, sel api.Selector) (*api.CustomObjectList, error) {
				return svc.ListCustomObjects(ctx, p, sel)
			},
			func(ctx context.Context, rv api.ResourceVersion, sel api.Selector) (<-chan api.WatchEvent, error) {
				return svc.WatchCustomObjects(ctx, p, rv, sel)
			})(w, r)
	}
}
