package apiserver_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"
)

// writePath is one write the service has. prepare stores what the write acts
// on, with no webhook registered, and returns the write and a reader of what
// the Store holds for the object the write is about: the object, or the
// error of reading it where it is absent.
type writePath struct {
	name    string
	prepare func(t *testing.T, e *env) (write func() error, held func() any)
}

// stored reads an object through get, as the value a test compares.
func stored[T any](t *testing.T, get func() (*T, error)) func() any {
	return func() any {
		o, err := get()
		if err != nil {
			return err.Error()
		}
		return o
	}
}

func discard[T any](o *T, err error) error { return err }

const holdFinalizer = "example.com/hold"

// webhookResources narrows the webhooks of a write that makes more than one
// call to the resources named here, so a refusal can come only from the call
// the row is about. A write not named here is matched on every resource.
var webhookResources = map[string][]string{
	// The binding is also a write of the MicroVM; only the Volume's own
	// update is sent to these webhooks.
	"volume pin at a binding": {"volumes"},
}

func writePaths() []writePath {
	return []writePath{
		{"microvm create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error { return discard(e.api.PutMicroVM(ctx(t), microvm("m1"))) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},
		{"microvm update", func(t *testing.T, e *env) (func() error, func() any) {
			m := e.createMicroVM(t, microvm("m1"))
			m.Spec.IdleTimeoutSeconds = 30
			return func() error { return discard(e.api.PutMicroVM(ctx(t), m)) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},
		{"microvm status", func(t *testing.T, e *env) (func() error, func() any) {
			m := e.bind(t, e.createMicroVM(t, microvm("m1")), "n1", 1)
			m.Status = api.MicroVMStatus{Phase: api.MicroVMRunning, BindingEpoch: 1}
			return func() error { return discard(e.api.PutMicroVMStatus(ctx(t), m)) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},
		{"microvm binding", func(t *testing.T, e *env) (func() error, func() any) {
			m := e.createMicroVM(t, microvm("m1"))
			b := api.Binding{ResourceVersion: m.Metadata.ResourceVersion, NodeName: "n1", BindingEpoch: 1}
			return func() error { return discard(e.api.BindMicroVM(ctx(t), "m1", b)) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},
		{"microvm delete", func(t *testing.T, e *env) (func() error, func() any) {
			m := e.createMicroVM(t, microvm("m1"))
			return func() error { return discard(e.api.DeleteMicroVM(ctx(t), "m1", m.Metadata.ResourceVersion)) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},
		{"microvm finalizer removal", func(t *testing.T, e *env) (func() error, func() any) {
			held := microvm("m1")
			held.Metadata.Finalizers = []string{holdFinalizer}
			m := e.createMicroVM(t, held)
			deleting, err := e.api.DeleteMicroVM(ctx(t), "m1", m.Metadata.ResourceVersion)
			require.NoError(t, err)
			require.NotNil(t, deleting.Metadata.DeletionTimestamp)
			deleting.Metadata.Finalizers = nil
			return func() error { return discard(e.api.PutMicroVM(ctx(t), deleting)) },
				stored(t, func() (*api.MicroVM, error) { return e.api.GetMicroVM(ctx(t), "m1") })
		}},

		{"node create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error { return discard(e.api.PutNode(ctx(t), node("n1"))) },
				stored(t, func() (*api.Node, error) { return e.api.GetNode(ctx(t), "n1") })
		}},
		{"node update", func(t *testing.T, e *env) (func() error, func() any) {
			n := e.createNode(t, "n1")
			n.Spec.Admitted = true
			return func() error { return discard(e.api.PutNode(ctx(t), n)) },
				stored(t, func() (*api.Node, error) { return e.api.GetNode(ctx(t), "n1") })
		}},
		{"node status", func(t *testing.T, e *env) (func() error, func() any) {
			n := e.createNode(t, "n1")
			n.Status.Addresses = []string{"10.0.0.1"}
			return func() error { return discard(e.api.PutNodeStatus(ctx(t), n)) },
				stored(t, func() (*api.Node, error) { return e.api.GetNode(ctx(t), "n1") })
		}},

		{"lease create", func(t *testing.T, e *env) (func() error, func() any) {
			e.createNode(t, "n1")
			return func() error { return discard(e.api.PutLease(ctx(t), lease("n1"))) },
				stored(t, func() (*api.Lease, error) { return e.api.GetLease(ctx(t), "n1") })
		}},
		{"lease update", func(t *testing.T, e *env) (func() error, func() any) {
			e.createNode(t, "n1")
			l, err := e.api.PutLease(ctx(t), lease("n1"))
			require.NoError(t, err)
			l.Spec.RenewTime = l.Spec.RenewTime.Add(time.Second)
			return func() error { return discard(e.api.PutLease(ctx(t), l)) },
				stored(t, func() (*api.Lease, error) { return e.api.GetLease(ctx(t), "n1") })
		}},

		{"volume create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error { return discard(e.api.PutVolume(ctx(t), volume("v1"))) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},
		{"volume update", func(t *testing.T, e *env) (func() error, func() any) {
			v := e.createVolume(t, "v1")
			v.Metadata.Finalizers = []string{holdFinalizer}
			return func() error { return discard(e.api.PutVolume(ctx(t), v)) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},
		{"volume delete", func(t *testing.T, e *env) (func() error, func() any) {
			v := e.createVolume(t, "v1")
			return func() error { return discard(e.api.DeleteVolume(ctx(t), "v1", v.Metadata.ResourceVersion)) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},
		{"volume finalizer removal", func(t *testing.T, e *env) (func() error, func() any) {
			held := volume("v1")
			held.Metadata.Finalizers = []string{holdFinalizer}
			v, err := e.api.PutVolume(ctx(t), held)
			require.NoError(t, err)
			deleting, err := e.api.DeleteVolume(ctx(t), "v1", v.Metadata.ResourceVersion)
			require.NoError(t, err)
			require.NotNil(t, deleting.Metadata.DeletionTimestamp)
			deleting.Metadata.Finalizers = nil
			return func() error { return discard(e.api.PutVolume(ctx(t), deleting)) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},
		{"volume pin at a binding", func(t *testing.T, e *env) (func() error, func() any) {
			e.createVolume(t, "v1")
			m := e.microvmUsing(t, "m1", "v1")
			return func() error { return discard(e.api.BindMicroVM(ctx(t), "m1", binding(m, "n1", 1))) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},
		{"volume status", func(t *testing.T, e *env) (func() error, func() any) {
			v := e.createVolume(t, "v1")
			e.boundMicroVM(t, "m1", 1)
			return func() error { return discard(e.api.PutVolumeStatus(ctx(t), attached(v, "m1", 1))) },
				stored(t, func() (*api.Volume, error) { return e.api.GetVolume(ctx(t), "v1") })
		}},

		{"volume snapshot create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error { return discard(e.api.PutVolumeSnapshot(ctx(t), volumeSnapshot("s1", "v1"))) },
				stored(t, func() (*api.VolumeSnapshot, error) { return e.api.GetVolumeSnapshot(ctx(t), "s1") })
		}},
		{"volume snapshot update", func(t *testing.T, e *env) (func() error, func() any) {
			s := e.createVolumeSnapshot(t, "s1", "v1")
			s.Metadata.Finalizers = []string{holdFinalizer}
			return func() error { return discard(e.api.PutVolumeSnapshot(ctx(t), s)) },
				stored(t, func() (*api.VolumeSnapshot, error) { return e.api.GetVolumeSnapshot(ctx(t), "s1") })
		}},
		{"volume snapshot status", func(t *testing.T, e *env) (func() error, func() any) {
			s := e.createVolumeSnapshot(t, "s1", "v1")
			s.Status = readyStatus("n1")
			return func() error { return discard(e.api.PutVolumeSnapshotStatus(ctx(t), s)) },
				stored(t, func() (*api.VolumeSnapshot, error) { return e.api.GetVolumeSnapshot(ctx(t), "s1") })
		}},
		{"volume snapshot delete", func(t *testing.T, e *env) (func() error, func() any) {
			s := e.createVolumeSnapshot(t, "s1", "v1")
			return func() error { return discard(e.api.DeleteVolumeSnapshot(ctx(t), "s1", s.Metadata.ResourceVersion)) },
				stored(t, func() (*api.VolumeSnapshot, error) { return e.api.GetVolumeSnapshot(ctx(t), "s1") })
		}},
		{"volume snapshot finalizer removal", func(t *testing.T, e *env) (func() error, func() any) {
			held := volumeSnapshot("s1", "v1")
			held.Metadata.Finalizers = []string{holdFinalizer}
			s, err := e.api.PutVolumeSnapshot(ctx(t), held)
			require.NoError(t, err)
			deleting, err := e.api.DeleteVolumeSnapshot(ctx(t), "s1", s.Metadata.ResourceVersion)
			require.NoError(t, err)
			require.NotNil(t, deleting.Metadata.DeletionTimestamp)
			deleting.Metadata.Finalizers = nil
			return func() error { return discard(e.api.PutVolumeSnapshot(ctx(t), deleting)) },
				stored(t, func() (*api.VolumeSnapshot, error) { return e.api.GetVolumeSnapshot(ctx(t), "s1") })
		}},

		{"certificate signing request create", func(t *testing.T, e *env) (func() error, func() any) {
			r := csr(t, "c1")
			return func() error { return discard(e.api.PutCertificateSigningRequest(ctx(t), r)) },
				stored(t, func() (*api.CertificateSigningRequest, error) {
					return e.api.GetCertificateSigningRequest(ctx(t), "c1")
				})
		}},
		{"certificate signing request status", func(t *testing.T, e *env) (func() error, func() any) {
			r, err := e.api.PutCertificateSigningRequest(ctx(t), csr(t, "c1"))
			require.NoError(t, err)
			r.Status = api.CertificateSigningRequestStatus{
				Conditions:  []api.Condition{{Type: api.CSRApproved, Status: "True", Reason: "Test"}},
				Certificate: "PEM",
			}
			return func() error { return discard(e.api.PutCertificateSigningRequestStatus(ctx(t), r)) },
				stored(t, func() (*api.CertificateSigningRequest, error) {
					return e.api.GetCertificateSigningRequest(ctx(t), "c1")
				})
		}},

		{"cluster role create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error {
					return discard(e.rbacClient().PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet))))
				},
				stored(t, func() (*api.ClusterRole, error) { return e.rbacClient().GetClusterRole(ctx(t), "r1") })
		}},
		{"cluster role update", func(t *testing.T, e *env) (func() error, func() any) {
			r, err := e.rbacClient().PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
			require.NoError(t, err)
			r.Rules = []api.PolicyRule{rule(api.Group, "microvms", api.VerbList)}
			return func() error { return discard(e.rbacClient().PutClusterRole(ctx(t), r)) },
				stored(t, func() (*api.ClusterRole, error) { return e.rbacClient().GetClusterRole(ctx(t), "r1") })
		}},
		{"cluster role delete", func(t *testing.T, e *env) (func() error, func() any) {
			r, err := e.rbacClient().PutClusterRole(ctx(t), clusterRole("r1", rule(api.Group, "microvms", api.VerbGet)))
			require.NoError(t, err)
			return func() error {
					return discard(e.rbacClient().DeleteClusterRole(ctx(t), "r1", r.Metadata.ResourceVersion))
				},
				stored(t, func() (*api.ClusterRole, error) { return e.rbacClient().GetClusterRole(ctx(t), "r1") })
		}},

		{"cluster role binding create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error {
					return discard(e.rbacClient().PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("u1"))))
				},
				stored(t, func() (*api.ClusterRoleBinding, error) { return e.rbacClient().GetClusterRoleBinding(ctx(t), "b1") })
		}},
		{"cluster role binding update", func(t *testing.T, e *env) (func() error, func() any) {
			b, err := e.rbacClient().PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("u1")))
			require.NoError(t, err)
			b.Subjects = []api.Subject{userSubject("u2")}
			return func() error { return discard(e.rbacClient().PutClusterRoleBinding(ctx(t), b)) },
				stored(t, func() (*api.ClusterRoleBinding, error) { return e.rbacClient().GetClusterRoleBinding(ctx(t), "b1") })
		}},
		{"cluster role binding delete", func(t *testing.T, e *env) (func() error, func() any) {
			b, err := e.rbacClient().PutClusterRoleBinding(ctx(t), clusterRoleBinding("b1", "r1", userSubject("u1")))
			require.NoError(t, err)
			return func() error {
					return discard(e.rbacClient().DeleteClusterRoleBinding(ctx(t), "b1", b.Metadata.ResourceVersion))
				},
				stored(t, func() (*api.ClusterRoleBinding, error) { return e.rbacClient().GetClusterRoleBinding(ctx(t), "b1") })
		}},

		{"custom resource definition create", func(t *testing.T, e *env) (func() error, func() any) {
			return func() error {
					return discard(e.extensions().PutCustomResourceDefinition(ctx(t), widgetDefinition(t, false)))
				},
				stored(t, func() (*api.CustomResourceDefinition, error) {
					return e.extensions().GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
				})
		}},
		{"custom resource definition update", func(t *testing.T, e *env) (func() error, func() any) {
			d := e.define(t, widgetDefinition(t, false))
			next := widgetDefinition(t, true)
			next.Metadata.ResourceVersion = d.Metadata.ResourceVersion
			return func() error { return discard(e.extensions().PutCustomResourceDefinition(ctx(t), next)) },
				stored(t, func() (*api.CustomResourceDefinition, error) {
					return e.extensions().GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
				})
		}},
		{"custom resource definition delete", func(t *testing.T, e *env) (func() error, func() any) {
			d := e.define(t, widgetDefinition(t, false))
			return func() error {
					return discard(e.extensions().DeleteCustomResourceDefinition(ctx(t), d.Metadata.Name, d.Metadata.ResourceVersion))
				},
				stored(t, func() (*api.CustomResourceDefinition, error) {
					return e.extensions().GetCustomResourceDefinition(ctx(t), "widgets."+appsGroup)
				})
		}},

		{"custom object create", func(t *testing.T, e *env) (func() error, func() any) {
			e.define(t, widgetDefinition(t, true))
			c := e.custom(t, widgetType)
			return func() error { return discard(c.Put(ctx(t), widget("w1", `{"size":2}`))) },
				stored(t, func() (*api.CustomObject, error) { return c.Get(ctx(t), "w1") })
		}},
		{"custom object update", func(t *testing.T, e *env) (func() error, func() any) {
			e.define(t, widgetDefinition(t, true))
			c := e.custom(t, widgetType)
			o, err := c.Put(ctx(t), widget("w1", `{"size":2}`))
			require.NoError(t, err)
			o.Spec = []byte(`{"size":3}`)
			return func() error { return discard(c.Put(ctx(t), o)) },
				stored(t, func() (*api.CustomObject, error) { return c.Get(ctx(t), "w1") })
		}},
		{"custom object status", func(t *testing.T, e *env) (func() error, func() any) {
			e.define(t, widgetDefinition(t, true))
			c := e.custom(t, widgetType)
			o, err := c.Put(ctx(t), widget("w1", `{"size":2}`))
			require.NoError(t, err)
			o.Status = []byte(`{"ready":true}`)
			return func() error { return discard(c.PutStatus(ctx(t), o)) },
				stored(t, func() (*api.CustomObject, error) { return c.Get(ctx(t), "w1") })
		}},
		{"custom object delete", func(t *testing.T, e *env) (func() error, func() any) {
			e.define(t, widgetDefinition(t, true))
			c := e.custom(t, widgetType)
			o, err := c.Put(ctx(t), widget("w1", `{"size":2}`))
			require.NoError(t, err)
			return func() error { return discard(c.Delete(ctx(t), "w1", o.Metadata.ResourceVersion)) },
				stored(t, func() (*api.CustomObject, error) { return c.Get(ctx(t), "w1") })
		}},
		{"custom object finalizer removal", func(t *testing.T, e *env) (func() error, func() any) {
			e.define(t, widgetDefinition(t, true))
			c := e.custom(t, widgetType)
			held := widget("w1", `{"size":2}`)
			held.Metadata.Finalizers = []string{holdFinalizer}
			o, err := c.Put(ctx(t), held)
			require.NoError(t, err)
			deleting, err := c.Delete(ctx(t), "w1", o.Metadata.ResourceVersion)
			require.NoError(t, err)
			require.NotNil(t, deleting.Metadata.DeletionTimestamp)
			deleting.Metadata.Finalizers = nil
			return func() error { return discard(c.Put(ctx(t), deleting)) },
				stored(t, func() (*api.CustomObject, error) { return c.Get(ctx(t), "w1") })
		}},
	}
}

// TestAdmission_EveryWriteIsSentToBothStages runs each write the service has
// against one mutating and one validating webhook that match everything. Each
// stage in turn refuses: the write is Forbidden, names the webhook, and
// leaves the Store as it was. With both allowing, the same write is stored and
// both stages have been called.
func TestAdmission_EveryWriteIsSentToBothStages(t *testing.T) {
	const (
		mutatingPath   = "/mutate"
		validatingPath = "/validate"
	)
	modes := []struct{ name, refusing, webhook string }{
		{"mutating refuses", mutatingPath, "mutate.hooks.example"},
		{"validating refuses", validatingPath, "validate.hooks.example"},
		{"both allow", "", ""},
	}
	for _, wp := range writePaths() {
		for _, mode := range modes {
			t.Run(wp.name+"/"+mode.name, func(t *testing.T) {
				e := newEnv(t)
				write, held := wp.prepare(t, e)

				w := newWebhookServer(t)
				w.answer = func(c call) *api.AdmissionResponse {
					if c.path == mode.refusing {
						return deny(c, http.StatusConflict, "refused by test")
					}
					return allow(c)
				}
				resources := []string{"*", "*/*"}
				if only, ok := webhookResources[wp.name]; ok {
					resources = only
				}
				e.mutating(t, "mutate", w.hook("mutate.hooks.example", mutatingPath, resources...))
				e.validating(t, "validate", w.hook("validate.hooks.example", validatingPath, resources...))

				before := held()
				err := write()

				if mode.refusing == "" {
					require.NoError(t, err)
					assert.NotEqual(t, before, held(), "the write changes the Store")
					var paths []string
					for _, c := range w.received() {
						paths = append(paths, c.path)
					}
					assert.Contains(t, paths, mutatingPath)
					assert.Contains(t, paths, validatingPath)
					return
				}
				require.ErrorIs(t, err, api.ErrForbidden)
				assert.Contains(t, err.Error(), mode.webhook)
				assert.Equal(t, before, held(), "a refused write leaves the Store as it was")
			})
		}
	}
}
