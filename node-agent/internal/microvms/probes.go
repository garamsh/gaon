package microvms

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

// The names the agent gives the two probes in mvmd, which reports a result
// under each.
const (
	readinessProbe = "readiness"
	livenessProbe  = "liveness"
)

// Reasons of the Ready condition of a Running MicroVM whose readiness probe
// has not passed.
const (
	// ReasonReadinessProbePending is the reason while mvmd reports no result
	// of the probe yet.
	ReasonReadinessProbePending = "ReadinessProbePending"
	// ReasonReadinessProbeFailing is the reason while mvmd reports the probe
	// failing, with its last failure as the message.
	ReasonReadinessProbeFailing = "ReadinessProbeFailing"
)

// withProbes puts the MicroVM's readiness and liveness probes in req.
func withProbes(m *api.MicroVM, req *v1alpha2.CreateMachineRequest) error {
	for _, p := range []struct {
		name  string
		probe *api.Probe
	}{{readinessProbe, m.Spec.ReadinessProbe}, {livenessProbe, m.Spec.LivenessProbe}} {
		if p.probe == nil {
			continue
		}
		rp, err := runtimeProbe(p.name, p.probe, m.Spec.Ports)
		if err != nil {
			return invalidSpec{err}
		}
		req.Probes = append(req.Probes, rp)
	}
	return nil
}

// runtimeProbe translates p into mvmd's probe. A zero field is left unset,
// which mvmd defaults as the API does; a named port becomes its number and an
// empty path "/". A value mvmd cannot take, such as a period over 24 hours, is
// left for mvmd to refuse.
func runtimeProbe(name string, p *api.Probe, ports []api.MicroVMPort) (*v1alpha2.Probe, error) {
	out := &v1alpha2.Probe{
		Name:             name,
		InitialDelay:     seconds(p.InitialDelaySeconds),
		Period:           seconds(p.PeriodSeconds),
		Timeout:          seconds(p.TimeoutSeconds),
		SuccessThreshold: uint32(p.SuccessThreshold),
		FailureThreshold: uint32(p.FailureThreshold),
	}
	switch {
	case p.Exec != nil:
		out.Kind = &v1alpha2.Probe_Exec{Exec: &v1alpha2.ExecProbe{Argv: slices.Clone(p.Exec.Command)}}
	case p.TCPSocket != nil:
		port, err := portNumber(p.TCPSocket.Port, ports)
		if err != nil {
			return nil, fmt.Errorf("spec.%sProbe.tcpSocket.port: %w", name, err)
		}
		out.Kind = &v1alpha2.Probe_Tcp{Tcp: &v1alpha2.TcpProbe{Port: port}}
	case p.HTTPGet != nil:
		port, err := portNumber(p.HTTPGet.Port, ports)
		if err != nil {
			return nil, fmt.Errorf("spec.%sProbe.httpGet.port: %w", name, err)
		}
		path := p.HTTPGet.Path
		if path == "" {
			path = "/"
		}
		out.Kind = &v1alpha2.Probe_HttpGet{HttpGet: &v1alpha2.HttpGetProbe{Port: port, Path: path}}
	}
	return out, nil
}

func seconds(n int32) *durationpb.Duration {
	if n == 0 {
		return nil
	}
	return durationpb.New(time.Duration(n) * time.Second)
}

// portNumber is the number of r: its own, or that of the port of spec.ports it
// names.
func portNumber(r api.PortRef, ports []api.MicroVMPort) (uint32, error) {
	if r.Name == "" {
		return uint32(r.Number), nil
	}
	for _, p := range ports {
		if p.Name == r.Name {
			return uint32(p.Port), nil
		}
	}
	return 0, fmt.Errorf("%q is not a port spec.ports declares", r.Name)
}

// probeStatus is the status mvmd reports for the probe name of r, UNKNOWN
// when it reports none.
func probeStatus(r *v1alpha2.Machine, name string) (v1alpha2.ProbeStatus, string) {
	for _, p := range r.GetProbes() {
		if p.GetName() == name {
			return p.GetStatus(), p.GetLastFailure()
		}
	}
	return v1alpha2.ProbeStatus_PROBE_STATUS_UNKNOWN, ""
}

// readiness is the Ready condition of the Running MicroVM r whose readiness
// probe is set: ready is kept while the probe passes.
func readiness(ready api.Condition, r *v1alpha2.Machine) api.Condition {
	status, failure := probeStatus(r, readinessProbe)
	switch status {
	case v1alpha2.ProbeStatus_PROBE_STATUS_PASSING:
		return ready
	case v1alpha2.ProbeStatus_PROBE_STATUS_FAILING:
		return api.Condition{Type: ConditionReady, Status: api.ConditionFalse, Reason: ReasonReadinessProbeFailing, Message: failure}
	}
	return api.Condition{Type: ConditionReady, Status: api.ConditionFalse, Reason: ReasonReadinessProbePending, Message: "mvmd reports no result of the readiness probe yet"}
}

// livenessFailure reports whether the liveness probe of the Running MicroVM r
// is failing, and says how.
func livenessFailure(m *api.MicroVM, r *v1alpha2.Machine) (string, bool) {
	if m.Spec.LivenessProbe == nil {
		return "", false
	}
	status, failure := probeStatus(r, livenessProbe)
	if status != v1alpha2.ProbeStatus_PROBE_STATUS_FAILING {
		return "", false
	}
	return "the liveness probe failed: " + failure, true
}

// stopUnhealthy stops the Running MicroVM whose liveness probe is failing, for
// why, and hands it to the exit handling of its run strategy: a MicroVM that
// restarts is started again after the backoff, and any other is reported
// failed.
func (s *service) stopUnhealthy(ctx context.Context, m *api.MicroVM, id string, r *v1alpha2.Machine, why string) (outcome, error) {
	stopped, err := s.runtime.StopMachine(ctx, stopRequest(id, m))
	if err != nil {
		return outcome{runtime: r}, fmt.Errorf("stop %s: %w", id, err)
	}
	if r = stopped.GetMachine(); r.GetState() != v1alpha2.MachineState_MACHINE_STATE_STOPPED {
		return outcome{runtime: r}, fmt.Errorf("stop %s: mvmd holds it %s", id, r.GetState())
	}
	s.runOf(id).liveness = why
	s.log.Info("stopped microvm whose liveness probe failed", "id", id)
	if restarts(m.Spec.RunStrategy) {
		return s.restart(ctx, m, id, r)
	}
	return s.ended(ctx, id, r)
}

// stopRequest is the request that stops the mvmd machine id, giving the guest
// the grace period of m, or the default when m is nil. The period is always
// set, so that a period of 0 reaches mvmd as 0 and not as an unset one.
func stopRequest(id string, m *api.MicroVM) *v1alpha2.StopMachineRequest {
	grace := api.DefaultTerminationGracePeriodSeconds
	if m != nil && m.Spec.TerminationGracePeriodSeconds != nil {
		grace = *m.Spec.TerminationGracePeriodSeconds
	}
	return &v1alpha2.StopMachineRequest{MachineId: id, GracePeriod: durationpb.New(time.Duration(grace) * time.Second)}
}
