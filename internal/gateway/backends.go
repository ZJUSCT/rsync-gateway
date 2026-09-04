package gateway

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// BackendStatus is the outcome of resolving a route's backendRefs against
// the snapshot. It backs the ResolvedRefs condition written on every parent
// entry of an accepted route.
type BackendStatus struct {
	// Resolved reports whether every backendRef resolves.
	Resolved bool
	// Reason is a standard gateway-api ResolvedRefs reason.
	Reason gatewayv1.RouteConditionReason
	// Message explains the first failure encountered.
	Message string
}

// ResolvedBackend is a backendRef that resolved to a Service and a port.
type ResolvedBackend struct {
	// Service is the resolved Service namespace/name.
	Service types.NamespacedName
	// Port is the backendRef's required port.
	Port int32
}

// ResolveBackends resolves every backendRef of a route. It returns the
// backends usable by the data plane (only references to core Services with
// an explicit port and a weight > 0 are usable) and the aggregate
// ResolvedRefs outcome. Evaluation order per backendRef is: group/kind,
// then port presence, then Service existence, then ReferenceGrant
// permission for cross-namespace references.
func ResolveBackends(snap *Snapshot, routeKey types.NamespacedName, route *v1alpha1.RsyncRoute) ([]ResolvedBackend, BackendStatus) {
	var usable []ResolvedBackend
	status := BackendStatus{Resolved: true, Reason: gatewayv1.RouteReasonResolvedRefs}

	fail := func(reason gatewayv1.RouteConditionReason, format string, args ...any) {
		if status.Resolved {
			status.Resolved = false
			status.Reason = reason
			status.Message = fmt.Sprintf(format, args...)
		}
	}

	for _, ref := range route.Spec.BackendRefs {
		// Weight 0 means "no traffic"; v0 simply excludes such backends
		// from the routing pool but still validates them like any other.
		excluded := ref.Weight != nil && *ref.Weight == 0

		group := gatewayv1.Group("")
		if ref.Group != nil {
			group = *ref.Group
		}
		kind := gatewayv1.Kind("Service")
		if ref.Kind != nil {
			kind = *ref.Kind
		}
		if group != v1alpha1.CoreGroup || kind != gatewayv1.Kind("Service") {
			fail(gatewayv1.RouteReasonInvalidKind, "unsupported backend group/kind %s/%s; only core Services are supported", group, kind)
			continue
		}
		if ref.Port == nil {
			fail(gatewayv1.RouteReasonBackendNotFound, "backendRef %q has no port; port is required by rsync-gateway", string(ref.Name))
			continue
		}

		ns := routeKey.Namespace
		if ref.Namespace != nil && *ref.Namespace != "" {
			ns = string(*ref.Namespace)
		}
		svcKey := types.NamespacedName{Namespace: ns, Name: string(ref.Name)}
		if _, ok := snap.Services[svcKey]; !ok {
			fail(gatewayv1.RouteReasonBackendNotFound, "Service %s not found", svcKey)
			continue
		}

		if ns != routeKey.Namespace && !referenceGrantPermits(snap, routeKey.Namespace, ns, string(ref.Name)) {
			fail(gatewayv1.RouteReasonRefNotPermitted, "cross-namespace reference from %s to Service %s is not permitted by a ReferenceGrant", routeKey, svcKey)
			continue
		}

		if !excluded {
			usable = append(usable, ResolvedBackend{Service: svcKey, Port: *ref.Port})
		}
	}
	return usable, status
}

// referenceGrantPermits reports whether a ReferenceGrant living in the
// backend namespace allows RsyncRoutes from fromNamespace to reference the
// named Service.
func referenceGrantPermits(snap *Snapshot, fromNamespace, backendNamespace, serviceName string) bool {
	for _, grant := range snap.ReferenceGrants[backendNamespace] {
		for _, from := range grant.Spec.From {
			// from.Group is the API group of the *referring* resource
			// (GEP-724): RsyncRoute lives in our own API group, not in
			// gateway.networking.k8s.io.
			if from.Group != gatewayv1.Group(v1alpha1.GroupVersion.Group) ||
				from.Kind != gatewayv1.Kind(v1alpha1.RouteKind) ||
				string(from.Namespace) != fromNamespace {
				continue
			}
			for _, to := range grant.Spec.To {
				if to.Group != v1alpha1.CoreGroup || to.Kind != gatewayv1.Kind("Service") {
					continue
				}
				if to.Name == nil || *to.Name == "" || *to.Name == gatewayv1.ObjectName(serviceName) {
					return true
				}
			}
		}
	}
	return false
}
