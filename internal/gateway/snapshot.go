// Package gateway implements the rsync-gateway control plane semantics:
// claiming GatewayClasses, binding RsyncRoutes to Gateways, resolving module
// conflicts and building the API statuses defined by the frozen v0 design.
//
// The functions in this package are pure: they operate on a Snapshot of the
// cluster state and return computed values without touching the API server
// or the data plane. The controller (package controller) is responsible for
// reading the snapshot and writing the results back.
package gateway

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// Snapshot is a point-in-time view of every resource relevant to one global
// rebuild of the gateway state.
type Snapshot struct {
	// GatewayClasses indexes GatewayClasses by name.
	GatewayClasses map[string]*gatewayv1.GatewayClass
	// GatewayConfigs indexes GatewayConfigs by name.
	GatewayConfigs map[string]*v1alpha1.GatewayConfig
	// Gateways indexes Gateways by namespace/name.
	Gateways map[types.NamespacedName]*gatewayv1.Gateway
	// RsyncRoutes indexes RsyncRoutes by namespace/name.
	RsyncRoutes map[types.NamespacedName]*v1alpha1.RsyncRoute
	// Services indexes Services by namespace/name.
	Services map[types.NamespacedName]*corev1.Service
	// ReferenceGrants groups ReferenceGrants by the namespace they live
	// in (i.e. the namespace whose objects may be granted).
	ReferenceGrants map[string][]*gatewayv1.ReferenceGrant
}

// NewSnapshot builds a Snapshot from flat lists, mirroring what a controller
// reads from its cache after a List of each kind.
func NewSnapshot(
	gatewayClasses []*gatewayv1.GatewayClass,
	gatewayConfigs []*v1alpha1.GatewayConfig,
	gateways []*gatewayv1.Gateway,
	routes []*v1alpha1.RsyncRoute,
	services []*corev1.Service,
	referenceGrants []*gatewayv1.ReferenceGrant,
) *Snapshot {
	snap := &Snapshot{
		GatewayClasses:  make(map[string]*gatewayv1.GatewayClass, len(gatewayClasses)),
		GatewayConfigs:  make(map[string]*v1alpha1.GatewayConfig, len(gatewayConfigs)),
		Gateways:        make(map[types.NamespacedName]*gatewayv1.Gateway, len(gateways)),
		RsyncRoutes:     make(map[types.NamespacedName]*v1alpha1.RsyncRoute, len(routes)),
		Services:        make(map[types.NamespacedName]*corev1.Service, len(services)),
		ReferenceGrants: make(map[string][]*gatewayv1.ReferenceGrant),
	}
	for _, gc := range gatewayClasses {
		if gc != nil {
			snap.GatewayClasses[gc.Name] = gc
		}
	}
	for _, gc := range gatewayConfigs {
		if gc != nil {
			snap.GatewayConfigs[gc.Name] = gc
		}
	}
	for _, gw := range gateways {
		if gw != nil {
			snap.Gateways[types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}] = gw
		}
	}
	for _, r := range routes {
		if r != nil {
			snap.RsyncRoutes[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = r
		}
	}
	for _, svc := range services {
		if svc != nil {
			snap.Services[types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}] = svc
		}
	}
	for _, g := range referenceGrants {
		if g != nil {
			snap.ReferenceGrants[g.Namespace] = append(snap.ReferenceGrants[g.Namespace], g)
		}
	}
	return snap
}
