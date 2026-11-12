package api

import (
	"bytes"
	"encoding/json"
	"time"
)

// RunStrategy says when a MicroVM runs.
type RunStrategy string

// The run strategies.
const (
	RunAlways         RunStrategy = "Always"
	RunRerunOnFailure RunStrategy = "RerunOnFailure"
	RunOnce           RunStrategy = "Once"
	RunOnDemand       RunStrategy = "OnDemand"
	RunHalted         RunStrategy = "Halted"
)

// MicroVMPhase is the phase a MicroVM's status reports.
type MicroVMPhase string

// The MicroVM phases.
const (
	MicroVMPending   MicroVMPhase = "Pending"
	MicroVMRunning   MicroVMPhase = "Running"
	MicroVMSuspended MicroVMPhase = "Suspended"
	MicroVMSucceeded MicroVMPhase = "Succeeded"
	MicroVMFailed    MicroVMPhase = "Failed"
	MicroVMStopped   MicroVMPhase = "Stopped"
)

// MicroVM is a microVM.
type MicroVM struct {
	TypeMeta
	Metadata ObjectMeta    `json:"metadata"`
	Spec     MicroVMSpec   `json:"spec"`
	Status   MicroVMStatus `json:"status"`
}

// MicroVMList is the answer to listing MicroVMs.
type MicroVMList struct {
	TypeMeta
	Metadata ListMeta  `json:"metadata"`
	Items    []MicroVM `json:"items"`
}

// ResourceList holds the quantities of cpu and memory.
type ResourceList struct {
	CPU    Quantity `json:"cpu,omitempty"`
	Memory Quantity `json:"memory,omitempty"`
}

// ResourceRequirements holds a MicroVM's requests and limits.
type ResourceRequirements struct {
	Requests ResourceList `json:"requests"`
	Limits   ResourceList `json:"limits"`
}

// MicroVMVolume mounts the Volume named Name at MountPath.
type MicroVMVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// MicroVMSpec is the desired state of a MicroVM. NodeName and BindingEpoch
// are set by the binding only.
type MicroVMSpec struct {
	Image                string               `json:"image,omitempty"`
	Kernel               string               `json:"kernel,omitempty"`
	Resources            ResourceRequirements `json:"resources"`
	RootDiskSize         Quantity             `json:"rootDiskSize,omitempty"`
	Volumes              []MicroVMVolume      `json:"volumes,omitempty"`
	Networks             []string             `json:"networks,omitempty"`
	Ports                []int32              `json:"ports,omitempty"`
	RunStrategy          RunStrategy          `json:"runStrategy"`
	IdleTimeoutSeconds   int32                `json:"idleTimeoutSeconds,omitempty"`
	NodeSelector         map[string]string    `json:"nodeSelector,omitempty"`
	RequiredCapabilities []string             `json:"requiredCapabilities,omitempty"`
	NodeName             string               `json:"nodeName,omitempty"`
	BindingEpoch         Epoch                `json:"bindingEpoch,omitempty"`
}

// MicroVMStatus is the observed state of a MicroVM.
type MicroVMStatus struct {
	Phase              MicroVMPhase `json:"phase,omitempty"`
	BindingEpoch       Epoch        `json:"bindingEpoch,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	Conditions         []Condition  `json:"conditions,omitempty"`
	RestartCount       int32        `json:"restartCount,omitempty"`
	Addresses          []string     `json:"addresses,omitempty"`
	LastActiveTime     *time.Time   `json:"lastActiveTime,omitempty"`
	LastExit           *ExitRecord  `json:"lastExit,omitempty"`
}

// ExitRecord is the exit code and time of a MicroVM's last exit.
type ExitRecord struct {
	Code int32     `json:"code"`
	Time time.Time `json:"time"`
}

// Binding is the body of POST /microvms/{name}/binding: the Node to bind to
// and the new epoch, conditioned on ResourceVersion.
type Binding struct {
	ResourceVersion ResourceVersion `json:"resourceVersion"`
	NodeName        string          `json:"nodeName"`
	BindingEpoch    Epoch           `json:"bindingEpoch"`
}

// Validate checks the identity and the spec of m, the section a user or a
// controller writes.
func (m *MicroVM) Validate() error {
	if err := validateIdentity(m.TypeMeta, GroupVersion, KindMicroVM, m.Metadata); err != nil {
		return err
	}
	switch m.Spec.RunStrategy {
	case RunAlways, RunRerunOnFailure, RunOnce, RunOnDemand, RunHalted:
		return nil
	default:
		return invalid("spec.runStrategy is %q, want Always, RerunOnFailure, Once, OnDemand or Halted", m.Spec.RunStrategy)
	}
}

// ValidateUpdate checks that m changes nothing of old's spec but runStrategy,
// idleTimeoutSeconds, resources and volumes. That includes nodeName and
// bindingEpoch, which only a binding sets.
func (m *MicroVM) ValidateUpdate(old *MicroVM) error {
	if err := m.Validate(); err != nil {
		return err
	}
	frozen := m.Spec
	frozen.RunStrategy = old.Spec.RunStrategy
	frozen.IdleTimeoutSeconds = old.Spec.IdleTimeoutSeconds
	frozen.Resources = old.Spec.Resources
	frozen.Volumes = old.Spec.Volumes
	if !sameJSON(frozen, old.Spec) {
		return invalid("spec changes a field other than runStrategy, idleTimeoutSeconds, resources and volumes")
	}
	return nil
}

// Bind sets the node and epoch of b on m, if b names a node and its epoch is
// m's current epoch plus one.
func (m *MicroVM) Bind(b Binding) error {
	if b.NodeName == "" {
		return invalid("binding names no node")
	}
	if b.BindingEpoch != m.Spec.BindingEpoch+1 {
		return invalid("binding epoch is %d, want %d", b.BindingEpoch, m.Spec.BindingEpoch+1)
	}
	m.Spec.NodeName = b.NodeName
	m.Spec.BindingEpoch = b.BindingEpoch
	return nil
}

// ValidateStatusUpdate checks m as a status write to current: a known phase
// and the epoch of current's spec.
func (m *MicroVM) ValidateStatusUpdate(current *MicroVM) error {
	switch m.Status.Phase {
	case MicroVMPending, MicroVMRunning, MicroVMSuspended, MicroVMSucceeded, MicroVMFailed, MicroVMStopped:
	default:
		return invalid("status.phase is %q, want Pending, Running, Suspended, Succeeded, Failed or Stopped", m.Status.Phase)
	}
	if m.Status.BindingEpoch != current.Spec.BindingEpoch {
		return invalid("status.bindingEpoch is %d, want the spec's %d", m.Status.BindingEpoch, current.Spec.BindingEpoch)
	}
	return nil
}

// EffectivePhase is the phase a reader acts on: a status whose epoch differs
// from the spec's is void and reads as Pending.
func (m *MicroVM) EffectivePhase() MicroVMPhase {
	if m.Status.BindingEpoch != m.Spec.BindingEpoch {
		return MicroVMPending
	}
	return m.Status.Phase
}

func sameJSON(a, b any) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}
