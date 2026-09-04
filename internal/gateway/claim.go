package gateway

import (
	"fmt"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

// Implementation-specific condition reasons used by rsync-gateway on top of
// the standard gateway-api vocabulary.
const (
	// ReasonTLSNotSupported marks a listener that sets TLS configuration,
	// which v0 does not support (rsync is not terminated over TLS).
	ReasonTLSNotSupported gatewayv1.ListenerConditionReason = "TLSNotSupported"

	// ReasonRestartRequired marks a listener whose port is not part of the
	// static bind set of the running data plane. v0 binds listeners once at
	// process startup, so adding a port requires a restart.
	ReasonRestartRequired gatewayv1.ListenerConditionReason = "RestartRequired"

	// ReasonModuleConflicted marks a parent binding where one of the
	// route's modules is already claimed by an older route on the same
	// Gateway.
	ReasonModuleConflicted gatewayv1.RouteConditionReason = "ModuleConflicted"
)

// ClassClaim is the result of evaluating a GatewayClass against this
// implementation.
type ClassClaim struct {
	// Name is the GatewayClass name.
	Name string
	// Claimed reports whether spec.controllerName matches
	// v1alpha1.ControllerName.
	Claimed bool
	// Accepted reports whether the class's parametersRef resolves to an
	// existing, valid GatewayConfig. Only meaningful when Claimed is true.
	Accepted bool
	// Reason and Message back the Accepted status condition written for
	// claimed classes.
	Reason gatewayv1.GatewayConditionReason
	// Message is the human-readable explanation for the condition.
	Message string
	// Config is the referenced GatewayConfig, or nil when absent or
	// unresolvable.
	Config *v1alpha1.GatewayConfig
}

// ClaimClasses evaluates every GatewayClass in the snapshot. A class is
// claimed when its controllerName matches ours; it is accepted when its
// parametersRef (if any) references an existing GatewayConfig that
// validates. A missing parametersRef is invalid: there is no point running
// the gateway with no configuration channel at all, and GEP-713 requires
// parametersRef-bearing implementations to reject classes whose parameters
// cannot be applied.
func ClaimClasses(snap *Snapshot) map[string]ClassClaim {
	claims := make(map[string]ClassClaim, len(snap.GatewayClasses))
	for name, gc := range snap.GatewayClasses {
		claim := ClassClaim{Name: name}
		if gc.Spec.ControllerName != v1alpha1.ControllerName {
			claim.Claimed = false
			claims[name] = claim
			continue
		}
		claim.Claimed = true
		config, reason, message := resolveClassConfig(gc, snap)
		claim.Config = config
		claim.Accepted = reason == gatewayv1.GatewayReasonAccepted
		claim.Reason = reason
		claim.Message = message
		claims[name] = claim
	}
	return claims
}

// resolveClassConfig resolves a claimed class's parametersRef to a valid
// GatewayConfig.
func resolveClassConfig(gc *gatewayv1.GatewayClass, snap *Snapshot) (*v1alpha1.GatewayConfig, gatewayv1.GatewayConditionReason, string) {
	invalid := func(msg string) (*v1alpha1.GatewayConfig, gatewayv1.GatewayConditionReason, string) {
		return nil, gatewayv1.GatewayReasonInvalidParameters, msg
	}

	ref := gc.Spec.ParametersRef
	if ref == nil {
		return invalid(fmt.Sprintf("GatewayClass %s must set spec.parametersRef to a GatewayConfig", gc.Name))
	}
	if ref.Group != gatewayv1.Group(v1alpha1.GroupVersion.Group) || ref.Kind != gatewayv1.Kind("GatewayConfig") {
		return invalid(fmt.Sprintf("unsupported parametersRef group/kind %s/%s; want %s/GatewayConfig", ref.Group, ref.Kind, v1alpha1.GroupVersion.Group))
	}
	if ref.Namespace != nil && *ref.Namespace != "" {
		return invalid("parametersRef.namespace must be empty; GatewayConfig is cluster-scoped")
	}
	config, ok := snap.GatewayConfigs[ref.Name]
	if !ok {
		return invalid(fmt.Sprintf("GatewayConfig %q not found", ref.Name))
	}
	if err := config.Spec.Validate(); err != nil {
		return invalid(fmt.Sprintf("GatewayConfig %q is invalid: %v", ref.Name, err))
	}
	return config, gatewayv1.GatewayReasonAccepted, fmt.Sprintf("GatewayConfig %q is valid", ref.Name)
}
