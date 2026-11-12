package api

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The defaults of a probe's timing, applied by the reader of a zero field.
const (
	DefaultProbePeriodSeconds            int32 = 10
	DefaultProbeTimeoutSeconds           int32 = 1
	DefaultProbeSuccessThreshold         int32 = 1
	DefaultProbeFailureThreshold         int32 = 3
	DefaultTerminationGracePeriodSeconds int32 = 30
)

// PortRef names a port of a MicroVM by its name or by its number.
type PortRef struct {
	Name   string
	Number int32
}

// MarshalJSON writes r as a JSON string for a name and a JSON number for a
// number.
func (r PortRef) MarshalJSON() ([]byte, error) {
	if r.Name != "" {
		return json.Marshal(r.Name)
	}
	return json.Marshal(r.Number)
}

// UnmarshalJSON reads a JSON string as a name and a JSON number as a number.
func (r *PortRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		*r = PortRef{}
		return json.Unmarshal(b, &r.Name)
	}
	*r = PortRef{}
	return json.Unmarshal(b, &r.Number)
}

// ExecAction runs Command in the guest; exit status 0 is success.
type ExecAction struct {
	Command []string `json:"command"`
}

// TCPSocketAction opens a TCP connection to Port in the guest.
type TCPSocketAction struct {
	Port PortRef `json:"port"`
}

// HTTPGetAction sends an HTTP GET to Path on Port in the guest; a status of
// 200 to 399 is success.
type HTTPGetAction struct {
	Path string  `json:"path,omitempty"`
	Port PortRef `json:"port"`
}

// Probe is a check mvmd runs in the guest: exactly one of Exec, TCPSocket and
// HTTPGet. A zero timing field takes the default of DefaultProbe...; a zero
// InitialDelaySeconds is no delay.
type Probe struct {
	Exec                *ExecAction      `json:"exec,omitempty"`
	TCPSocket           *TCPSocketAction `json:"tcpSocket,omitempty"`
	HTTPGet             *HTTPGetAction   `json:"httpGet,omitempty"`
	InitialDelaySeconds int32            `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int32            `json:"periodSeconds,omitempty"`
	TimeoutSeconds      int32            `json:"timeoutSeconds,omitempty"`
	SuccessThreshold    int32            `json:"successThreshold,omitempty"`
	FailureThreshold    int32            `json:"failureThreshold,omitempty"`
}

func (r PortRef) validate(field string, ports []MicroVMPort) error {
	switch {
	case r.Name != "" && r.Number != 0:
		return invalid("%s names both a name and a number, want one", field)
	case r.Name != "":
		for _, p := range ports {
			if p.Name == r.Name {
				return nil
			}
		}
		return invalid("%s is %q, which spec.ports does not declare", field, r.Name)
	case r.Number < 1 || r.Number > 65535:
		return invalid("%s is %d, want a name or a number from 1 to 65535", field, r.Number)
	}
	return nil
}

// validate checks p as the probe field of a spec. liveness probes take a
// success threshold of 1 only.
func (p *Probe) validate(field string, ports []MicroVMPort, liveness bool) error {
	if p == nil {
		return nil
	}
	handlers := 0
	if p.Exec != nil {
		handlers++
	}
	if p.TCPSocket != nil {
		handlers++
	}
	if p.HTTPGet != nil {
		handlers++
	}
	if handlers != 1 {
		return invalid("%s gives %d of exec, tcpSocket and httpGet, want one", field, handlers)
	}
	if p.Exec != nil && (len(p.Exec.Command) == 0 || p.Exec.Command[0] == "") {
		return invalid("%s.exec.command is empty, want the program to run", field)
	}
	if p.TCPSocket != nil {
		if err := p.TCPSocket.Port.validate(field+".tcpSocket.port", ports); err != nil {
			return err
		}
	}
	if p.HTTPGet != nil {
		if p.HTTPGet.Path != "" && !strings.HasPrefix(p.HTTPGet.Path, "/") {
			return invalid("%s.httpGet.path is %q, want it to start with '/'", field, p.HTTPGet.Path)
		}
		if err := p.HTTPGet.Port.validate(field+".httpGet.port", ports); err != nil {
			return err
		}
	}
	for _, t := range []struct {
		name  string
		value int32
	}{
		{"initialDelaySeconds", p.InitialDelaySeconds},
		{"periodSeconds", p.PeriodSeconds},
		{"timeoutSeconds", p.TimeoutSeconds},
		{"successThreshold", p.SuccessThreshold},
		{"failureThreshold", p.FailureThreshold},
	} {
		if t.value < 0 {
			return invalid("%s.%s is %d, want 0 or more", field, t.name, t.value)
		}
	}
	if liveness && p.SuccessThreshold > 1 {
		return invalid("%s.successThreshold is %d, want 1 for a liveness probe", field, p.SuccessThreshold)
	}
	return nil
}
