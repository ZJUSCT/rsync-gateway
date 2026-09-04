package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ThroughputFloorSpec configures the minimum average throughput a relay
// connection must sustain. It maps to the min_throughput_* settings of
// rsync-proxy.
type ThroughputFloorSpec struct {
	// MinBytes is the minimum number of bytes (counting both relay
	// directions) that a connection must transfer within any window of
	// Window seconds. 0 disables the floor.
	// +optional
	MinBytes *int64 `json:"minBytes,omitempty"`

	// Window is the sliding window over which MinBytes is enforced,
	// expressed as a GEP-2257 duration string (e.g. "60s"). When MinBytes
	// is positive and Window is unset, a default of 60s applies (mirroring
	// the rsync-proxy default).
	// +optional
	Window *gatewayv1.Duration `json:"window,omitempty"`

	// Grace suppresses the throughput check for the first Grace seconds of
	// a connection so slow-start sessions are not killed. When unset it
	// defaults to the effective window.
	// +optional
	Grace *gatewayv1.Duration `json:"grace,omitempty"`
}

// GatewayConfigSpec defines the desired state of a GatewayConfig. All fields
// are optional; omitted fields mirror the rsync-proxy defaults (which mostly
// means "limit disabled").
type GatewayConfigSpec struct {
	// Motd is the message of the day sent to clients after the protocol
	// version handshake. Maps to rsync-proxy's proxy.motd.
	// +optional
	Motd *string `json:"motd,omitempty"`

	// RelayIdleTimeout is the idle timeout applied during the relay phase
	// (rsyncd "timeout" semantics). Unset means disabled.
	// +optional
	RelayIdleTimeout *gatewayv1.Duration `json:"relayIdleTimeout,omitempty"`

	// RelayMaxDuration is a hard cap on the total wall-clock duration of a
	// relay connection. Unset means disabled.
	// +optional
	RelayMaxDuration *gatewayv1.Duration `json:"relayMaxDuration,omitempty"`

	// TCPKeepAlive is the TCP keepalive period applied to client and
	// upstream connections. Unset leaves OS-default behavior.
	// +optional
	TCPKeepAlive *gatewayv1.Duration `json:"tcpKeepAlive,omitempty"`

	// DialTimeout caps how long the data plane waits when dialing an
	// upstream rsync server. Unset leaves OS-default behavior.
	// +optional
	DialTimeout *gatewayv1.Duration `json:"dialTimeout,omitempty"`

	// PerIPMaxActiveConnections is the proxy-wide default for the per-IP
	// concurrency cap per upstream. 0 or unset disables the cap.
	// +optional
	PerIPMaxActiveConnections *int32 `json:"perIPMaxActiveConnections,omitempty"`

	// MaxActiveConnections is the per-upstream default for the maximum
	// number of concurrent active relay connections. 0 or unset disables
	// the limit.
	// +optional
	MaxActiveConnections *int32 `json:"maxActiveConnections,omitempty"`

	// MaxQueuedConnections is the per-upstream default for the maximum
	// number of connections queued while all active slots are busy. 0 or
	// unset disables the limit.
	// +optional
	MaxQueuedConnections *int32 `json:"maxQueuedConnections,omitempty"`

	// ThroughputFloor configures the minimum average throughput a relay
	// connection must sustain.
	// +optional
	ThroughputFloor *ThroughputFloorSpec `json:"throughputFloor,omitempty"`
}

// GatewayConfigStatus defines the observed state of a GatewayConfig.
type GatewayConfigStatus struct {
	// Conditions describe the current state of the GatewayConfig. The
	// "Accepted" condition (reason Accepted or Invalid) is maintained by
	// rsync-gateway, following the GEP-713 style parameter validation
	// semantics.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GatewayConfig is the cluster-scoped parameters resource referenced by a
// GatewayClass via spec.parametersRef. It carries the data-plane settings
// (timeouts, connection limits, MOTD) applied by rsync-gateway to every
// Gateway owned by that GatewayClass.
type GatewayConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired data-plane parameters.
	// +optional
	Spec GatewayConfigSpec `json:"spec,omitempty"`

	// Status contains the observed state of the GatewayConfig.
	// +optional
	Status GatewayConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GatewayConfigList contains a list of GatewayConfig.
type GatewayConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GatewayConfig `json:"items"`
}
