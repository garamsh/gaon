package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/garamsh/gaon/lib/api"
)

// stamp sets each condition's transition time: the time it had in old while its
// status is unchanged, and now otherwise.
func stamp(old, next []api.Condition, now time.Time) []api.Condition {
	for i := range next {
		t := now
		for _, o := range old {
			if o.Type == next[i].Type && o.Status == next[i].Status && o.LastTransitionTime != nil {
				t = *o.LastTransitionTime
			}
		}
		next[i].LastTransitionTime = &t
	}
	return next
}

// gatewayConditions returns the Accepted and Programmed conditions of a Gateway.
func gatewayConditions(check gatewayCheck, p provisioned) []api.Condition {
	served := len(check.servedListeners())
	var accepted api.Condition
	switch {
	case check.invalid != "":
		accepted = condition(CondAccepted, false, ReasonInvalid, check.invalid)
	case p.refused:
		accepted = condition(CondAccepted, false, ReasonAddressNotAssigned, p.message)
	case served == 0:
		accepted = condition(CondAccepted, false, ReasonListenersNotValid, "no listener is valid")
	case served < len(check.listeners):
		accepted = condition(CondAccepted, true, ReasonListenersNotValid, fmt.Sprintf("%d of %d listeners are not valid", len(check.listeners)-served, len(check.listeners)))
	default:
		accepted = condition(CondAccepted, true, ReasonAccepted, "")
	}
	return []api.Condition{accepted, programmed(p)}
}

func programmed(p provisioned) api.Condition {
	return condition(CondProgrammed, p.programmed, p.reason, p.message)
}

// listenerConditions returns the conditions of one listener.
func listenerConditions(c listenerCheck, p provisioned) []api.Condition {
	conflict := c.reason == ReasonProtocolConflict || c.reason == ReasonHostnameConflict
	out := []api.Condition{
		condition(CondAccepted, c.served, ReasonAccepted, ""),
		condition(CondConflicted, conflict, ReasonNoConflicts, ""),
		condition(CondResolvedRefs, !c.badCertificate, ReasonResolvedRefs, ""),
	}
	if !c.served {
		out[0].Reason, out[0].Message = c.reason, c.message
		out[1].Message = c.message
		if conflict {
			out[1].Reason = c.reason
		}
		if c.badCertificate {
			out[2].Reason, out[2].Message = ReasonInvalidCertificate, c.message
		}
		return append(out, condition(CondProgrammed, false, ReasonInvalid, "the listener is not served"))
	}
	return append(out, programmed(p))
}

// gatewayStatus returns the status of a Gateway, with the transition times of old.
func gatewayStatus(check gatewayCheck, p provisioned, attached map[string]int32, old GatewayStatus, now time.Time) GatewayStatus {
	next := GatewayStatus{Conditions: stamp(old.Conditions, gatewayConditions(check, p), now)}
	if p.address != "" {
		next.Addresses = []Address{{Type: AddressIPAddress, Value: p.address}}
	}
	for _, c := range check.listeners {
		var before []api.Condition
		for _, o := range old.Listeners {
			if o.Name == c.listener.Name {
				before = o.Conditions
			}
		}
		next.Listeners = append(next.Listeners, ListenerStatus{
			Name: c.listener.Name, AttachedRoutes: attached[c.listener.Name],
			Conditions: stamp(before, listenerConditions(c, p), now),
		})
	}
	return next
}

// routeStatus returns the status of a route: for each parent reference, whether
// it is accepted and whether the backends resolve.
func routeStatus(problem string, parents []attachment, resolved bool, backendMessage string, old RouteStatus, now time.Time) RouteStatus {
	var next RouteStatus
	for i, a := range parents {
		accepted := condition(CondAccepted, a.accepted, a.reason, a.message)
		if problem != "" {
			accepted = condition(CondAccepted, false, ReasonUnsupportedValue, problem)
		}
		backends := condition(CondResolvedRefs, resolved, ReasonResolvedRefs, "")
		if !resolved {
			backends.Reason, backends.Message = ReasonBackendNotFound, backendMessage
		}
		var before []api.Condition
		if i < len(old.Parents) && old.Parents[i].ParentRef == a.ref {
			before = old.Parents[i].Conditions
		}
		next.Parents = append(next.Parents, ParentStatus{ParentRef: a.ref, Conditions: stamp(before, []api.Condition{accepted, backends}, now)})
	}
	return next
}

// putStatus writes next as the status of obj unless it encodes as old does.
func putStatus(ctx context.Context, client *api.CustomResourceClient, obj *api.CustomObject, old, next any) error {
	before, err := json.Marshal(old)
	if err != nil {
		return err
	}
	after, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		return nil
	}
	_, err = client.PutStatus(ctx, &api.CustomObject{TypeMeta: obj.TypeMeta, Metadata: obj.Metadata, Status: after})
	return err
}
