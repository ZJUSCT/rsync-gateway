package gateway

import (
	"fmt"
	"net"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// ClassCondition builds the Accepted status condition for a claimed
// GatewayClass from its claim result. Unclaimed classes produce nil: their
// status is owned by whoever claims them.
func ClassCondition(gc *gatewayv1.GatewayClass, claim ClassClaim) *metav1.Condition {
	if gc == nil || !claim.Claimed {
		return nil
	}
	status := metav1.ConditionFalse
	if claim.Accepted {
		status = metav1.ConditionTrue
	}
	return &metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             status,
		Reason:             string(claim.Reason),
		Message:            claim.Message,
		ObservedGeneration: gc.Generation,
	}
}

// BuildGatewayStatus assembles the full Gateway status owned by
// rsync-gateway: the top-level conditions plus listener statuses for the
// rsync listeners only. Status entries of foreign listeners are preserved.
// The Gateway is only managed when its class is claimed and accepted, so
// the top-level conditions are always True here.
func BuildGatewayStatus(gw *gatewayv1.Gateway, _ ClassClaim, servedPorts []int32) gatewayv1.GatewayStatus {
	status := gatewayv1.GatewayStatus{}

	// Preserve listener status entries written for listeners we do not
	// manage.
	for _, ls := range gw.Status.Listeners {
		if !isManagedListener(gw, ls.Name) {
			status.Listeners = append(status.Listeners, ls)
		}
	}
	status.Listeners = append(status.Listeners, listenerStatuses(gw, servedPorts)...)

	conditions := []metav1.Condition{
		{
			Type:               string(gatewayv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.GatewayReasonAccepted),
			Message:            fmt.Sprintf("GatewayClass %q is claimed and accepted", gw.Spec.GatewayClassName),
			ObservedGeneration: gw.Generation,
		},
		{
			Type:               string(gatewayv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.GatewayReasonProgrammed),
			Message:            fmt.Sprintf("routes bound to %s/%s are programmed into the data plane", gw.Namespace, gw.Name),
			ObservedGeneration: gw.Generation,
		},
	}
	for i := range conditions {
		conditions[i] = StableCondition(gw.Status.Conditions, conditions[i])
	}
	status.Conditions = MergeConditions(gw.Status.Conditions, conditions)
	return status
}

// listenerStatuses builds status entries for every rsync listener of the
// Gateway, keeping condition timestamps stable across rebuilds.
func listenerStatuses(gw *gatewayv1.Gateway, servedPorts []int32) []gatewayv1.ListenerStatus {
	supported := []gatewayv1.RouteGroupKind{v1alpha1.RouteGroupKind()}
	var out []gatewayv1.ListenerStatus
	for _, l := range gw.Spec.Listeners {
		if l.Protocol != v1alpha1.ProtocolRsync {
			continue
		}
		var existingConds []metav1.Condition
		for _, ls := range gw.Status.Listeners {
			if ls.Name == l.Name {
				existingConds = ls.Conditions
				break
			}
		}
		conds := []metav1.Condition{
			listenerAcceptedCondition(gw, &l),
			listenerProgrammedCondition(gw, &l, servedPorts),
		}
		for i := range conds {
			conds[i] = StableCondition(existingConds, conds[i])
		}
		out = append(out, gatewayv1.ListenerStatus{
			Name:           l.Name,
			SupportedKinds: supported,
			Conditions:     conds,
		})
	}
	return out
}

// listenerAcceptedCondition evaluates the Accepted condition of an rsync
// listener. TLS termination is not supported in v0.
func listenerAcceptedCondition(gw *gatewayv1.Gateway, l *gatewayv1.Listener) metav1.Condition {
	if l.TLS != nil {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             string(ReasonTLSNotSupported),
			Message:            "rsync-gateway v0 does not terminate TLS on rsync listeners",
			ObservedGeneration: gw.Generation,
		}
	}
	if why := allowedRoutesParseError(l); why != "" {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             string(gatewayv1.ListenerReasonUnsupportedValue),
			Message:            why,
			ObservedGeneration: gw.Generation,
		}
	}
	return metav1.Condition{
		Type:               string(gatewayv1.ListenerConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.ListenerReasonAccepted),
		Message:            "listener is accepted",
		ObservedGeneration: gw.Generation,
	}
}

// allowedRoutesParseError validates the parts of allowedRoutes the binding
// code relies on, so that malformed values surface on the listener instead
// of silently failing route attachment.
func allowedRoutesParseError(l *gatewayv1.Listener) string {
	if l.AllowedRoutes == nil || l.AllowedRoutes.Namespaces == nil {
		return ""
	}
	ns := l.AllowedRoutes.Namespaces
	if ns.From == nil {
		return ""
	}
	switch *ns.From {
	case gatewayv1.NamespacesFromAll, gatewayv1.NamespacesFromSame:
		return ""
	case gatewayv1.NamespacesFromSelector:
		if ns.Selector == nil {
			return "allowedRoutes.namespaces.from=Selector requires a selector"
		}
		return ""
	default:
		return fmt.Sprintf("allowedRoutes.namespaces.from=%q is not supported", *ns.From)
	}
}

// listenerProgrammedCondition reports whether the listener's port is part
// of the static bind set of the data plane. v0 binds listeners once at
// startup: listeners on other ports stay unprogrammed until a restart.
func listenerProgrammedCondition(gw *gatewayv1.Gateway, l *gatewayv1.Listener, servedPorts []int32) metav1.Condition {
	for _, p := range servedPorts {
		if p == l.Port {
			return metav1.Condition{
				Type:               string(gatewayv1.ListenerConditionProgrammed),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.ListenerReasonProgrammed),
				Message:            "listener port is served by the data plane",
				ObservedGeneration: gw.Generation,
			}
		}
	}
	return metav1.Condition{
		Type:               string(gatewayv1.ListenerConditionProgrammed),
		Status:             metav1.ConditionFalse,
		Reason:             string(ReasonRestartRequired),
		Message:            "listener port is not in the static bind set; restart the gateway to pick it up (v0 limitation)",
		ObservedGeneration: gw.Generation,
	}
}

// BuildRouteStatus assembles the parents status entries for a route:
// entries written by other controllers are preserved, ours are rebuilt from
// the binding results (one per spec parentRef, in spec order). Condition
// timestamps stay stable when type and status are unchanged.
func BuildRouteStatus(route *v1alpha1.RsyncRoute, results []ParentResult, backends BackendStatus) []gatewayv1.RouteParentStatus {
	parents := make([]gatewayv1.RouteParentStatus, 0, len(route.Spec.ParentRefs))
	for _, p := range route.Status.Parents {
		if p.ControllerName != v1alpha1.ControllerName {
			parents = append(parents, p)
		}
	}

	for _, res := range results {
		existingConds := findExistingConditions(route, res.ParentRef)
		conds := []metav1.Condition{StableCondition(existingConds, metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             conditionStatus(res.Accepted),
			Reason:             string(res.Reason),
			Message:            res.Message,
			ObservedGeneration: route.Generation,
		})}
		if res.Accepted {
			conds = append(conds, StableCondition(existingConds, metav1.Condition{
				Type:               string(gatewayv1.RouteConditionResolvedRefs),
				Status:             conditionStatus(backends.Resolved),
				Reason:             string(backends.Reason),
				Message:            backends.Message,
				ObservedGeneration: route.Generation,
			}))
		}
		parents = append(parents, gatewayv1.RouteParentStatus{
			ParentRef:      res.ParentRef,
			ControllerName: v1alpha1.ControllerName,
			Conditions:     conds,
		})
	}
	return parents
}

// findExistingConditions returns the conditions of our own previous status
// entry matching the given parentRef, if any.
func findExistingConditions(route *v1alpha1.RsyncRoute, parentRef gatewayv1.ParentReference) []metav1.Condition {
	for _, p := range route.Status.Parents {
		if p.ControllerName != v1alpha1.ControllerName {
			continue
		}
		if parentRefsEqual(p.ParentRef, parentRef) {
			return p.Conditions
		}
	}
	return nil
}

func parentRefsEqual(a, b gatewayv1.ParentReference) bool {
	return ParentRefString(a) == ParentRefString(b)
}

// ServedPorts parses a list of listen addresses into the deduplicated TCP
// ports the data plane is bound to. Addresses whose port cannot be
// determined are skipped.
func ServedPorts(addrs []string) []int32 {
	var ports []int32
	seen := make(map[int32]bool, len(addrs))
	for _, addr := range addrs {
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			continue
		}
		p := int32(port)
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports
}

func conditionStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// isManagedListener reports whether a listener name belongs to an rsync
// listener of the Gateway spec.
func isManagedListener(gw *gatewayv1.Gateway, name gatewayv1.SectionName) bool {
	for _, l := range gw.Spec.Listeners {
		if l.Name == name {
			return l.Protocol == v1alpha1.ProtocolRsync
		}
	}
	return false
}
