// Package garbagecollector deletes an object once every owner named in its
// ownerReferences is gone.
package garbagecollector

import (
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// Config holds the numbers the collector does not decide.
type Config struct {
	// Interval is the time between the end of one sweep and the start of the
	// next.
	Interval time.Duration
}

// resource is a kind the collector can list and read, and whether the API
// serves a DELETE for it.
type resource struct {
	typ       api.CustomResourceType
	deletable bool
}

// kindKey identifies a kind as an owner reference does: by the apiVersion and
// the kind it names.
type kindKey struct {
	apiVersion string
	kind       string
}

func (r resource) key() kindKey {
	return kindKey{apiVersion: r.typ.Group + "/" + r.typ.Version, kind: r.typ.Kind}
}

// sameIncarnation reports whether an owner reference holding refUID may name
// the object that holds objUID. A uid on either side that is empty cannot be
// compared, so it does not tell one incarnation from another.
func sameIncarnation(refUID, objUID api.UID) bool {
	return refUID == "" || objUID == "" || refUID == objUID
}
