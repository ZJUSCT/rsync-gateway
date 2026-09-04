package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// RsyncRouteSpec defines the desired state of a RsyncRoute.
type RsyncRouteSpec struct {
	// ParentRefs identifies the Gateway(s) this route wants to be attached
	// to. Group and Kind default to "gateway.networking.k8s.io"/"Gateway"
	// and Namespace defaults to the namespace of this route, matching the
	// HTTPRoute semantics.
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	ParentRefs []gatewayv1.ParentReference `json:"parentRefs"`

	// ModuleNames is the list of rsync module names served by this route.
	// When empty, the effective list is the single module
	// metadata.name.
	//
	// Each name must be non-empty printable ASCII without control
	// characters, must not start with '#' and must be at most 255
	// characters long. Module names are newline-terminated on the rsync
	// wire protocol, which makes these restrictions a security requirement
	// rather than a cosmetic one.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Pattern=`^[\x21-\x22\x24-\x7E][\x20-\x7E]{0,254}$`
	ModuleNames []string `json:"moduleNames,omitempty"`

	// BackendRefs lists the backend Services that requests to any of the
	// route's modules are relayed to. The port field is REQUIRED (rsync
	// needs a deterministic port and the core BackendObjectReference type
	// marks it optional, so rsync-gateway rejects references without a
	// port at reconciliation time with ResolvedRefs=False/BackendNotFound).
	//
	// v0 only supports core Service references (group "" kind Service).
	// The weight field may be set but is only honored for the
	// zero/non-zero distinction: every backend with weight > 0 is part of
	// the routing pool and receives the same treatment; backends with
	// weight 0 are excluded.
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	BackendRefs []gatewayv1.BackendRef `json:"backendRefs"`
}

// RsyncRouteStatus defines the observed state of a RsyncRoute. It follows
// the standard gateway-api RouteStatus shape.
type RsyncRouteStatus struct {
	// Parents describes the status of the route with respect to each
	// Gateway parent. rsync-gateway only writes entries whose
	// controllerName is gateway.rsync.zjusct.io/rsync-gateway and
	// preserves entries written by other controllers.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	Parents []gatewayv1.RouteParentStatus `json:"parents,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.parents[?(@.controllerName=="gateway.rsync.zjusct.io/rsync-gateway")].conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RsyncRoute is the Schema for the rsyncroutes API. Binding a RsyncRoute to
// a Gateway (via parentRefs) publishes the route's modules on the Gateway's
// rsync listeners and relays module traffic to the route's backendRefs.
type RsyncRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the route.
	Spec RsyncRouteSpec `json:"spec,omitempty"`

	// Status defines the observed state of the route.
	// +optional
	Status RsyncRouteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RsyncRouteList contains a list of RsyncRoute.
type RsyncRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RsyncRoute `json:"items"`
}
