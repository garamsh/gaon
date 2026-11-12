package api

import (
	"fmt"
	"slices"
)

// PolicyType is a direction a NetworkPolicy limits.
type PolicyType string

// The policy types.
const (
	PolicyIngress PolicyType = "Ingress"
	PolicyEgress  PolicyType = "Egress"
)

// NetworkPolicy limits the traffic of the MicroVMs its PodSelector selects,
// with the semantics of Kubernetes' NetworkPolicy: a MicroVM that no policy
// selects for a direction is unrestricted in it, and one that some policy
// selects for it allows only what some policy allows. It has no status.
type NetworkPolicy struct {
	TypeMeta
	Metadata ObjectMeta        `json:"metadata"`
	Spec     NetworkPolicySpec `json:"spec"`
}

// NetworkPolicyList is the answer to listing NetworkPolicies.
type NetworkPolicyList struct {
	TypeMeta
	Metadata ListMeta        `json:"metadata"`
	Items    []NetworkPolicy `json:"items"`
}

// NetworkPolicySpec says which MicroVMs a policy selects and what it allows.
// PolicyTypes names the directions it limits; when empty it is Ingress, and
// Egress as well if Egress has rules (EffectivePolicyTypes). A direction with
// no rules allows nothing.
type NetworkPolicySpec struct {
	PodSelector LabelSelector `json:"podSelector"`
	PolicyTypes []PolicyType  `json:"policyTypes,omitempty"`
	Ingress     []IngressRule `json:"ingress,omitempty"`
	Egress      []EgressRule  `json:"egress,omitempty"`
}

// IngressRule allows traffic from From on Ports. An empty From allows every
// source, and empty Ports every port.
type IngressRule struct {
	From  []NetworkPolicyPeer `json:"from,omitempty"`
	Ports []NetworkPolicyPort `json:"ports,omitempty"`
}

// EgressRule allows traffic to To on Ports. An empty To allows every
// destination, and empty Ports every port.
type EgressRule struct {
	To    []NetworkPolicyPeer `json:"to,omitempty"`
	Ports []NetworkPolicyPort `json:"ports,omitempty"`
}

// NetworkPolicyPeer is the other end of traffic: exactly one of the MicroVMs
// a PodSelector selects, and the addresses of an IPBlock.
type NetworkPolicyPeer struct {
	PodSelector *LabelSelector `json:"podSelector,omitempty"`
	IPBlock     *IPBlock       `json:"ipBlock,omitempty"`
}

// IPBlock is the addresses of a CIDR other than those of the prefixes in
// Except, each of which lies inside CIDR and is narrower than it.
type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except,omitempty"`
}

// NetworkPolicyPort is a protocol and a port number.
type NetworkPolicyPort struct {
	Protocol Protocol `json:"protocol"`
	Port     int32    `json:"port"`
}

// EffectivePolicyTypes is the directions s limits: PolicyTypes when it is
// given, otherwise Ingress, and Egress too when s has egress rules.
func (s NetworkPolicySpec) EffectivePolicyTypes() []PolicyType {
	if len(s.PolicyTypes) > 0 {
		return s.PolicyTypes
	}
	if len(s.Egress) > 0 {
		return []PolicyType{PolicyIngress, PolicyEgress}
	}
	return []PolicyType{PolicyIngress}
}

// Validate checks the identity, the name and the metadata of p, its selector,
// its policy types and every peer and port of its rules.
func (p *NetworkPolicy) Validate() error {
	if err := validateIdentity(p.TypeMeta, GroupVersion, KindNetworkPolicy, p.Metadata); err != nil {
		return err
	}
	if err := validateObjectName("metadata.name", p.Metadata.Name); err != nil {
		return err
	}
	if err := p.Metadata.Validate(); err != nil {
		return err
	}
	return p.Spec.validate()
}

func (s NetworkPolicySpec) validate() error {
	if _, err := s.PodSelector.Selector(); err != nil {
		return invalid("spec.podSelector: %v", err)
	}
	seen := map[PolicyType]bool{}
	for i, t := range s.PolicyTypes {
		if t != PolicyIngress && t != PolicyEgress {
			return invalid("spec.policyTypes[%d] is %q, want Ingress or Egress", i, t)
		}
		if seen[t] {
			return invalid("spec.policyTypes[%d] repeats %s", i, t)
		}
		seen[t] = true
	}
	types := s.EffectivePolicyTypes()
	if len(s.Ingress) > 0 && !slices.Contains(types, PolicyIngress) {
		return invalid("spec.ingress has rules, want Ingress in spec.policyTypes")
	}
	if len(s.Egress) > 0 && !slices.Contains(types, PolicyEgress) {
		return invalid("spec.egress has rules, want Egress in spec.policyTypes")
	}
	for i, r := range s.Ingress {
		if err := validatePeers(fmt.Sprintf("spec.ingress[%d].from", i), r.From); err != nil {
			return err
		}
		if err := validatePolicyPorts(fmt.Sprintf("spec.ingress[%d].ports", i), r.Ports); err != nil {
			return err
		}
	}
	for i, r := range s.Egress {
		if err := validatePeers(fmt.Sprintf("spec.egress[%d].to", i), r.To); err != nil {
			return err
		}
		if err := validatePolicyPorts(fmt.Sprintf("spec.egress[%d].ports", i), r.Ports); err != nil {
			return err
		}
	}
	return nil
}
