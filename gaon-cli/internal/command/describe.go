package command

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// describe prints res, the result of reading one object of k, as labelled
// lines followed by its status conditions.
func describe(out io.Writer, k kind, res result) error {
	m := res.meta
	lines := []detail{
		{"Name", m.Name},
		{"Kind", k.apiKind},
		{"Resource version", orDash(string(m.ResourceVersion))},
		{"Generation", strconv.FormatInt(m.Generation, 10)},
	}
	if m.DeletionTimestamp != nil {
		lines = append(lines, detail{"Deletion timestamp", m.DeletionTimestamp.Format(time.RFC3339)})
	}
	lines = append(lines, detail{"Finalizers", joinOrDash(m.Finalizers)})
	lines = append(lines, res.details...)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, l := range lines {
		fmt.Fprintf(w, "%s:\t%s\n", l.label, l.value)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !res.withConditions {
		return nil
	}
	fmt.Fprintln(out, "Conditions:")
	if len(res.conditions) == 0 {
		fmt.Fprintln(out, "  -")
		return nil
	}
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  TYPE\tSTATUS\tREASON\tLAST TRANSITION\tMESSAGE")
	for _, c := range res.conditions {
		since := "-"
		if c.LastTransitionTime != nil {
			since = c.LastTransitionTime.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", c.Type, c.Status, orDash(c.Reason), since, orDash(c.Message))
	}
	return w.Flush()
}

func joinOrDash(items []string) string {
	return orDash(strings.Join(items, ", "))
}

// named renders a quantity as name=value, or as nothing when it is empty.
func named(name string, q api.Quantity) string {
	if q == "" {
		return ""
	}
	return name + "=" + string(q)
}

// quantities joins the non-empty parts, which named produced.
func quantities(parts ...string) string {
	var set []string
	for _, p := range parts {
		if p != "" {
			set = append(set, p)
		}
	}
	return joinOrDash(set)
}

func volumeMounts(vs []api.MicroVMVolume) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Name + ":" + v.MountPath
	}
	return out
}

func ports(ps []api.MicroVMPort) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = strconv.FormatInt(int64(p.Port), 10) + "/" + string(p.Protocol)
		if p.Name != "" {
			out[i] = p.Name + ":" + out[i]
		}
	}
	return out
}

func lastExit(e *api.ExitRecord) string {
	if e == nil {
		return "-"
	}
	return fmt.Sprintf("code %d at %s", e.Code, e.Time.Format(time.RFC3339))
}

func attachment(a *api.Attachment) string {
	if a == nil {
		return "-"
	}
	return fmt.Sprintf("%s (epoch %d)", a.MicroVMName, a.BindingEpoch)
}

// requestState is Denied when the request has a Denied condition that is
// true, else Approved when it has an Approved one, else Pending.
func requestState(r *api.CertificateSigningRequest) string {
	state := "Pending"
	for _, c := range r.Status.Conditions {
		switch {
		case c.Type == api.CSRDenied && c.Status == api.ConditionTrue:
			return "Denied"
		case c.Type == api.CSRApproved && c.Status == api.ConditionTrue:
			state = "Approved"
		}
	}
	return state
}

func issued(certificate string) string {
	if certificate == "" {
		return "-"
	}
	return "issued"
}

// ruleDetails lists one line per rule, or one line for none.
func ruleDetails(rules []api.PolicyRule) []detail {
	if len(rules) == 0 {
		return []detail{{"Rules", "-"}}
	}
	out := make([]detail, len(rules))
	for i, r := range rules {
		out[i] = detail{
			"Rule " + strconv.Itoa(i+1),
			fmt.Sprintf("apiGroups=%v resources=%v verbs=%v", r.APIGroups, r.Resources, r.Verbs),
		}
	}
	return out
}

func subjects(ss []api.Subject) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s.Kind) + "/" + s.Name
	}
	return out
}

func versionNames(vs []api.CustomResourceVersion) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Name
	}
	return out
}

func hasStatusSubresource(vs []api.CustomResourceVersion) bool {
	for _, v := range vs {
		if v.Subresources != nil && v.Subresources.Status != nil {
			return true
		}
	}
	return false
}

// established is the status of the Established condition, or - when the
// definition has none.
func established(d *api.CustomResourceDefinition) string {
	for _, c := range d.Status.Conditions {
		if c.Type == api.CRDEstablished {
			return string(c.Status)
		}
	}
	return "-"
}

// sortedKeys lists the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// secretKeys lists each key of a Secret's data with the size of its value.
// The values are never rendered.
func secretKeys(data map[string][]byte) string {
	keys := sortedKeys(data)
	for i, k := range keys {
		keys[i] = fmt.Sprintf("%s (%d bytes)", k, len(data[k]))
	}
	return joinOrDash(keys)
}

// labelPairs lists a label map as key=value, in key order.
func labelPairs(m map[string]string) []string {
	pairs := sortedKeys(m)
	for i, k := range pairs {
		pairs[i] = k + "=" + m[k]
	}
	return pairs
}

func servicePorts(ps []api.ServicePort) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = strconv.FormatInt(int64(p.Port), 10) + "/" + string(p.Protocol)
		if p.TargetPort != nil {
			out[i] += "->" + portRef(*p.TargetPort)
		}
		if p.Name != "" {
			out[i] = p.Name + ":" + out[i]
		}
	}
	return out
}

func portRef(r api.PortRef) string {
	if r.Name != "" {
		return r.Name
	}
	return strconv.FormatInt(int64(r.Number), 10)
}

// ingressAddresses lists the LAN addresses the API server gave a Service.
func ingressAddresses(s api.ServiceStatus) []string {
	if s.LoadBalancer == nil {
		return nil
	}
	out := make([]string, len(s.LoadBalancer.Ingress))
	for i, in := range s.LoadBalancer.Ingress {
		out[i] = in.IP
	}
	return out
}

func endpointAddresses(subsets []api.EndpointSubset) []string {
	var out []string
	for _, s := range subsets {
		for _, a := range s.Addresses {
			out = append(out, a.IP)
		}
	}
	return out
}

// subsetDetails lists one line per subset, or one line for none.
func subsetDetails(subsets []api.EndpointSubset) []detail {
	if len(subsets) == 0 {
		return []detail{{"Subsets", "-"}}
	}
	out := make([]detail, len(subsets))
	for i, s := range subsets {
		ports := make([]string, len(s.Ports))
		for j, p := range s.Ports {
			ports[j] = strconv.FormatInt(int64(p.Port), 10) + "/" + string(p.Protocol)
			if p.Name != "" {
				ports[j] = p.Name + ":" + ports[j]
			}
		}
		out[i] = detail{"Subset " + strconv.Itoa(i+1), fmt.Sprintf("addresses=%v ports=%v", endpointAddresses([]api.EndpointSubset{s}), ports)}
	}
	return out
}

func policyTypes(s api.NetworkPolicySpec) string {
	types := s.EffectivePolicyTypes()
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return joinOrDash(out)
}

// selectorText renders a label selector, or "all MicroVMs" for the empty one.
func selectorText(l api.LabelSelector) string {
	parts := labelPairs(l.MatchLabels)
	for _, r := range l.MatchExpressions {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", r.Key, r.Operator, strings.Join(r.Values, ", ")))
	}
	if len(parts) == 0 {
		return "all MicroVMs"
	}
	return strings.Join(parts, ", ")
}
