package gateway

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// ParentResult is the per-parentRef binding outcome computed for a
// RsyncRoute.
type ParentResult struct {
	// ParentRef is the route's spec.parentRefs entry this result belongs
	// to.
	ParentRef gatewayv1.ParentReference
	// Gateway is the resolved Gateway, or the zero value when it could
	// not be resolved.
	Gateway types.NamespacedName
	// Listener is the listener the route attached to when the parentRef
	// carries a sectionName; nil for whole-Gateway attachments.
	Listener *gatewayv1.Listener
	// Accepted reports whether the route is bound to the parent.
	Accepted bool
	// Reason is the Accepted condition reason (standard gateway-api
	// reason or an implementation-specific one declared in this package).
	Reason gatewayv1.RouteConditionReason
	// Message explains the outcome.
	Message string
}

// gatewayAPIGroup is the group of the gateway-api core resources.
const gatewayAPIGroup = "gateway.networking.k8s.io"

// BindRoutes computes the per-parent binding results for every RsyncRoute in
// the snapshot. The returned map has one entry per route (in spec order for
// the parentRefs) and accounts for module conflicts across routes bound to
// the same Gateway.
func BindRoutes(snap *Snapshot, claims map[string]ClassClaim) map[types.NamespacedName][]ParentResult {
	results := make(map[types.NamespacedName][]ParentResult, len(snap.RsyncRoutes))
	for key, route := range snap.RsyncRoutes {
		perParent := make([]ParentResult, 0, len(route.Spec.ParentRefs))
		for _, parentRef := range route.Spec.ParentRefs {
			perParent = append(perParent, bindParent(snap, claims, key, route, parentRef))
		}
		results[key] = perParent
	}
	resolveModuleConflicts(snap, results)
	return results
}

// bindParent evaluates a single parentRef against the snapshot.
func bindParent(snap *Snapshot, claims map[string]ClassClaim, routeKey types.NamespacedName, route *v1alpha1.RsyncRoute, parentRef gatewayv1.ParentReference) ParentResult {
	result := ParentResult{ParentRef: parentRef}

	group := gatewayv1.Group(gatewayAPIGroup)
	if parentRef.Group != nil {
		group = *parentRef.Group
	}
	kind := gatewayv1.Kind("Gateway")
	if parentRef.Kind != nil {
		kind = *parentRef.Kind
	}
	if group != gatewayv1.Group(gatewayAPIGroup) || kind != gatewayv1.Kind("Gateway") {
		result.Reason = gatewayv1.RouteReasonNoMatchingParent
		result.Message = fmt.Sprintf("unsupported parent group/kind %s/%s; only %s/Gateway is supported", group, kind, gatewayAPIGroup)
		return result
	}

	ns := routeKey.Namespace
	if parentRef.Namespace != nil && *parentRef.Namespace != "" {
		ns = string(*parentRef.Namespace)
	}
	gatewayKey := types.NamespacedName{Namespace: ns, Name: string(parentRef.Name)}
	result.Gateway = gatewayKey
	gw := snap.Gateways[gatewayKey]
	if gw == nil {
		result.Reason = gatewayv1.RouteReasonNoMatchingParent
		result.Message = fmt.Sprintf("Gateway %s not found", gatewayKey)
		return result
	}

	claim, claimed := claims[string(gw.Spec.GatewayClassName)]
	if !claimed || !claim.Claimed {
		result.Reason = gatewayv1.RouteReasonNoMatchingParent
		result.Message = fmt.Sprintf("GatewayClass %q of Gateway %s is not managed by %s", gw.Spec.GatewayClassName, gatewayKey, v1alpha1.ControllerName)
		return result
	}
	if !claim.Accepted {
		result.Reason = gatewayv1.RouteReasonNoMatchingParent
		result.Message = fmt.Sprintf("GatewayClass %q is not accepted: %s", gw.Spec.GatewayClassName, claim.Message)
		return result
	}

	listeners := rsyncListeners(gw)
	if len(listeners) == 0 {
		result.Reason = gatewayv1.RouteReasonNoMatchingParent
		result.Message = fmt.Sprintf("Gateway %s has no listener with protocol %s", gatewayKey, v1alpha1.ProtocolRsync)
		return result
	}

	var candidates []gatewayv1.Listener
	if parentRef.SectionName != nil && *parentRef.SectionName != "" {
		want := string(*parentRef.SectionName)
		for _, l := range listeners {
			if string(l.Name) == want {
				candidates = append(candidates, l)
				break
			}
		}
		if len(candidates) == 0 {
			result.Reason = gatewayv1.RouteReasonNoMatchingParent
			result.Message = fmt.Sprintf("Gateway %s has no listener named %q with protocol %s", gatewayKey, want, v1alpha1.ProtocolRsync)
			return result
		}
	} else {
		candidates = listeners
	}

	rejections := make([]string, 0, len(candidates))
	for _, l := range candidates {
		if why := listenerAllows(&l, gatewayKey.Namespace, routeKey, route); why == "" {
			matched := l
			result.Listener = &matched
			result.Accepted = true
			result.Reason = gatewayv1.RouteReasonAccepted
			return result
		} else {
			rejections = append(rejections, fmt.Sprintf("listener %q: %s", l.Name, why))
		}
	}

	result.Reason = gatewayv1.RouteReasonNotAllowedByListeners
	result.Message = fmt.Sprintf("no listener of Gateway %s allows this route: %s", gatewayKey, strings.Join(rejections, "; "))
	return result
}

// rsyncListeners returns the Gateway listeners whose protocol is the rsync
// implementation-specific protocol. Listeners of other protocols are owned
// by other implementations and never touched.
func rsyncListeners(gw *gatewayv1.Gateway) []gatewayv1.Listener {
	var out []gatewayv1.Listener
	for _, l := range gw.Spec.Listeners {
		if l.Protocol == v1alpha1.ProtocolRsync {
			out = append(out, l)
		}
	}
	return out
}

// listenerAllows reports whether the route may attach to the listener per
// its allowedRoutes settings. gatewayNS is the namespace of the Gateway
// owning the listener. An empty return value means "allowed".
func listenerAllows(l *gatewayv1.Listener, gatewayNS string, routeKey types.NamespacedName, route *v1alpha1.RsyncRoute) string {
	if !kindsAllow(l) {
		return fmt.Sprintf("route kind %s/%s is not allowed by allowedRoutes.kinds", v1alpha1.GroupVersion.Group, v1alpha1.RouteKind)
	}
	return namespacesAllow(l, gatewayNS, routeKey, route)
}

// kindsAllow implements allowedRoutes.kinds matching. An absent or empty
// list implies the route kind associated with the listener protocol, which
// is RsyncRoute for rsync listeners.
func kindsAllow(l *gatewayv1.Listener) bool {
	if l.AllowedRoutes == nil || len(l.AllowedRoutes.Kinds) == 0 {
		return true
	}
	for _, k := range l.AllowedRoutes.Kinds {
		group := gatewayv1.Group(gatewayAPIGroup)
		if k.Group != nil {
			group = *k.Group
		}
		if group == gatewayv1.Group(v1alpha1.GroupVersion.Group) && k.Kind == v1alpha1.RouteKind {
			return true
		}
	}
	return false
}

// namespacesAllow implements allowedRoutes.namespaces matching. An absent
// setting defaults to From=Same, mirroring the gateway-api default.
func namespacesAllow(l *gatewayv1.Listener, gatewayNS string, routeKey types.NamespacedName, route *v1alpha1.RsyncRoute) string {
	if l.AllowedRoutes == nil || l.AllowedRoutes.Namespaces == nil {
		return sameNamespaceAllow(gatewayNS, routeKey)
	}
	ns := l.AllowedRoutes.Namespaces
	if ns.From == nil {
		return sameNamespaceAllow(gatewayNS, routeKey)
	}
	switch *ns.From {
	case gatewayv1.NamespacesFromAll:
		return ""
	case gatewayv1.NamespacesFromSame:
		return sameNamespaceAllow(gatewayNS, routeKey)
	case gatewayv1.NamespacesFromSelector:
		if ns.Selector == nil {
			return "allowedRoutes.namespaces.from=Selector requires a selector"
		}
		sel, err := metav1.LabelSelectorAsSelector(ns.Selector)
		if err != nil {
			return fmt.Sprintf("invalid allowedRoutes.namespaces.selector: %v", err)
		}
		if !sel.Matches(labels.Set(route.Labels)) {
			return "route namespace labels do not match allowedRoutes.namespaces.selector"
		}
		return ""
	default:
		return fmt.Sprintf("allowedRoutes.namespaces.from=%s is not supported", *ns.From)
	}
}

// sameNamespaceAllow implements the From=Same semantics: the Gateway and
// the route must live in the same namespace.
func sameNamespaceAllow(gatewayNS string, routeKey types.NamespacedName) string {
	if gatewayNS != routeKey.Namespace {
		return "route namespace differs from the Gateway namespace (allowedRoutes.namespaces.from=Same)"
	}
	return ""
}

// EffectiveModules returns the module list a route serves: spec.moduleNames
// or, when empty, the single module named after the route.
func EffectiveModules(route *v1alpha1.RsyncRoute) []string {
	if len(route.Spec.ModuleNames) == 0 {
		return []string{route.Name}
	}
	return route.Spec.ModuleNames
}

// resolveModuleConflicts applies the module precedence rules in place: for
// every managed Gateway, bound routes are ordered by creation timestamp
// (ties broken by namespace/name) and older routes win the module names. A
// route whose module is claimed by an older route on the same Gateway has
// all its accepted results for that Gateway rewritten to
// Accepted=False/ModuleConflicted.
func resolveModuleConflicts(snap *Snapshot, results map[types.NamespacedName][]ParentResult) {
	// gateway key -> routes bound to that gateway.
	bound := make(map[types.NamespacedName][]types.NamespacedName)
	for routeKey, perParent := range results {
		seen := make(map[types.NamespacedName]bool)
		for _, res := range perParent {
			if !res.Accepted || seen[res.Gateway] {
				continue
			}
			seen[res.Gateway] = true
			bound[res.Gateway] = append(bound[res.Gateway], routeKey)
		}
	}

	for gatewayKey, routeKeys := range bound {
		sort.Slice(routeKeys, func(i, j int) bool {
			a, b := snap.RsyncRoutes[routeKeys[i]], snap.RsyncRoutes[routeKeys[j]]
			if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
				return a.CreationTimestamp.Before(&b.CreationTimestamp)
			}
			return routeKeys[i].String() < routeKeys[j].String()
		})

		owner := make(map[string]types.NamespacedName)
		for _, routeKey := range routeKeys {
			route := snap.RsyncRoutes[routeKey]

			// First pass: detect whether any module is already claimed
			// by an older route. A conflicted route loses the whole
			// binding on this Gateway and claims nothing.
			var winner types.NamespacedName
			var winnerModule string
			conflicted := false
			for _, module := range EffectiveModules(route) {
				if prev, ok := owner[module]; ok {
					conflicted = true
					winner, winnerModule = prev, module
					break
				}
			}
			if conflicted {
				for i := range results[routeKey] {
					res := &results[routeKey][i]
					if res.Accepted && res.Gateway == gatewayKey {
						res.Accepted = false
						res.Reason = ReasonModuleConflicted
						res.Message = fmt.Sprintf("module %q is already claimed by route %q on Gateway %s; older routes win", winnerModule, winner, gatewayKey)
					}
				}
				continue
			}
			for _, module := range EffectiveModules(route) {
				owner[module] = routeKey
			}
		}
	}
}
