package fakemvmd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	maxProbes    = 8
	maxProbeTime = 24 * time.Hour
	minProbeTime = time.Millisecond
	maxProbePath = 2048
	maxProbeName = 63
)

var probeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// checkProbes refuses a Create the way mvmd refuses one whose probes the Guest
// Agent would refuse, naming the probe. The caller holds s.mu.
func checkProbes(probes []*v1alpha2.Probe) error {
	rejected := func(name, msg string) error {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, fmt.Sprintf("probe %q: %s", name, msg))
	}
	if len(probes) > maxProbes {
		return v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_REJECTED, "probes holds more than 8 probes")
	}
	names := map[string]bool{}
	for _, p := range probes {
		n := p.GetName()
		switch {
		case n == "" || len(n) > maxProbeName || !probeName.MatchString(n):
			return rejected(n, "the name is empty, over 63 bytes or holds a character other than letters, digits, '-', '_' and '.'")
		case names[n]:
			return rejected(n, "the name is given twice")
		}
		names[n] = true
		if err := checkProbeKind(p); err != nil {
			return rejected(n, err.Error())
		}
		for _, d := range []struct {
			field string
			value *durationpb.Duration
			min   time.Duration
		}{{"initial_delay", p.GetInitialDelay(), 0}, {"period", p.GetPeriod(), minProbeTime}, {"timeout", p.GetTimeout(), minProbeTime}} {
			if d.value == nil {
				continue
			}
			if v := d.value.AsDuration(); v < d.min || v > maxProbeTime {
				return rejected(n, d.field+" is outside "+d.min.String()+" to 24h")
			}
		}
	}
	return nil
}

func checkProbeKind(p *v1alpha2.Probe) error {
	port := func(n uint32) error {
		if n < 1 || n > 65535 {
			return fmt.Errorf("the port is %d, want 1 to 65535", n)
		}
		return nil
	}
	switch k := p.GetKind().(type) {
	case *v1alpha2.Probe_Exec:
		if len(k.Exec.GetArgv()) == 0 || k.Exec.GetArgv()[0] == "" {
			return fmt.Errorf("the exec argv is empty")
		}
	case *v1alpha2.Probe_Tcp:
		return port(k.Tcp.GetPort())
	case *v1alpha2.Probe_HttpGet:
		h := k.HttpGet
		if err := port(h.GetPort()); err != nil {
			return err
		}
		if !strings.HasPrefix(h.GetPath(), "/") || len(h.GetPath()) > maxProbePath {
			return fmt.Errorf("the path %q does not begin with '/' or is over 2048 bytes", h.GetPath())
		}
		if h.GetScheme() != "" && h.GetScheme() != "http" {
			return fmt.Errorf("the scheme %q is not http", h.GetScheme())
		}
	default:
		return fmt.Errorf("it sets none of exec, tcp and http_get")
	}
	return nil
}

// unknownProbes is one UNKNOWN result for each probe, in order.
func unknownProbes(probes []*v1alpha2.Probe) []*v1alpha2.ProbeResult {
	out := make([]*v1alpha2.ProbeResult, 0, len(probes))
	for _, p := range probes {
		out = append(out, &v1alpha2.ProbeResult{Name: p.GetName(), Status: v1alpha2.ProbeStatus_PROBE_STATUS_UNKNOWN})
	}
	return out
}

// SetProbe sets the result of the probe name of the Machine id, as the guest's
// last report gives it. It panics when the Machine has no such probe, since
// that is the test's mistake.
func (s *Server) SetProbe(id, name string, status v1alpha2.ProbeStatus, lastFailure string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.machines[id].GetProbes() {
		if r.GetName() == name {
			r.Status, r.LastFailure = status, lastFailure
			r.ChangedAt = timestamppb.New(s.clock.Now())
			return
		}
	}
	panic(fmt.Sprintf("fakemvmd: machine %s has no probe %s", id, name))
}

// reported returns a copy of the Machine m as Inspect and List answer for it:
// a Running or Paused Machine reports the results held, any other state
// UNKNOWN for each probe. The caller holds s.mu.
func reported(m *v1alpha2.Machine) *v1alpha2.Machine {
	out := proto.Clone(m).(*v1alpha2.Machine)
	if m.GetState() == v1alpha2.MachineState_MACHINE_STATE_RUNNING || m.GetState() == v1alpha2.MachineState_MACHINE_STATE_PAUSED {
		return out
	}
	for _, r := range out.GetProbes() {
		r.Status, r.ChangedAt, r.LastFailure = v1alpha2.ProbeStatus_PROBE_STATUS_UNKNOWN, nil, ""
	}
	return out
}
