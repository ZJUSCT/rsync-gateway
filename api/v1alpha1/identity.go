package v1alpha1

import (
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Identity constants shared by every rsync-gateway component. They are
// defined in one place so that the API, the controller and the docs always
// agree on the gateway API extension points used by this implementation.
const (
	// ProtocolRsync is the Gateway listener protocol implemented by
	// rsync-gateway. It follows the implementation-specific ProtocolType
	// convention of gateway-api ("domain/path").
	ProtocolRsync gatewayv1.ProtocolType = "rsync.zjusct.io/rsync"

	// ControllerName identifies rsync-gateway as the controller of the
	// resources it manages (GatewayClass.controllerName and the
	// controllerName field written into route status entries).
	ControllerName gatewayv1.GatewayController = "gateway.rsync.zjusct.io/rsync-gateway"

	// RouteKind is the kind of the route resource served by this
	// implementation.
	RouteKind gatewayv1.Kind = "RsyncRoute"

	// CoreGroup is the core (empty) API group, used e.g. for Service
	// references.
	CoreGroup gatewayv1.Group = ""
)

// RouteGroupKind returns the GroupKind pair used to advertise (and match)
// RsyncRoute resources in gateway-api structures such as
// AllowedRoutes.Kinds and ListenerStatus.SupportedKinds.
func RouteGroupKind() gatewayv1.RouteGroupKind {
	return gatewayv1.RouteGroupKind{
		Group: groupPtr(),
		Kind:  RouteKind,
	}
}

func groupPtr() *gatewayv1.Group {
	g := gatewayv1.Group(GroupVersion.Group)
	return &g
}
