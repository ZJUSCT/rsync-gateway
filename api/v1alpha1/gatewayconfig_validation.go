package v1alpha1

import (
	"fmt"
	"time"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Validate checks the semantic validity of a GatewayConfigSpec. CRD
// structural validation already constrains most fields; this check exists for
// programmatic callers (controllers, tests, the gateway binary reading a
// spec from the API server) so that an invalid spec is reported before it is
// translated into data-plane configuration.
func (s GatewayConfigSpec) Validate() error {
	intFields := []struct {
		name string
		val  *int32
	}{
		{"perIPMaxActiveConnections", s.PerIPMaxActiveConnections},
		{"maxActiveConnections", s.MaxActiveConnections},
		{"maxQueuedConnections", s.MaxQueuedConnections},
	}
	for _, f := range intFields {
		if f.val != nil && *f.val < 0 {
			return fmt.Errorf("%s must be non-negative, got %d", f.name, *f.val)
		}
	}

	durationFields := []struct {
		name string
		val  *gatewayv1.Duration
	}{
		{"relayIdleTimeout", s.RelayIdleTimeout},
		{"relayMaxDuration", s.RelayMaxDuration},
		{"tcpKeepAlive", s.TCPKeepAlive},
		{"dialTimeout", s.DialTimeout},
		{"throughputFloor.window", s.ThroughputFloorWindow()},
		{"throughputFloor.grace", s.ThroughputFloorGrace()},
	}
	for _, f := range durationFields {
		if f.val == nil {
			continue
		}
		dur, err := time.ParseDuration(string(*f.val))
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q: %w", f.name, *f.val, err)
		}
		if dur < 0 {
			return fmt.Errorf("%s must be non-negative, got %s", f.name, *f.val)
		}
	}

	if s.ThroughputFloor != nil && s.ThroughputFloor.MinBytes != nil && *s.ThroughputFloor.MinBytes < 0 {
		return fmt.Errorf("throughputFloor.minBytes must be non-negative, got %d", *s.ThroughputFloor.MinBytes)
	}
	return nil
}

// ThroughputFloorWindow returns the raw window duration, if set.
func (s GatewayConfigSpec) ThroughputFloorWindow() *gatewayv1.Duration {
	if s.ThroughputFloor == nil {
		return nil
	}
	return s.ThroughputFloor.Window
}

// ThroughputFloorGrace returns the raw grace duration, if set.
func (s GatewayConfigSpec) ThroughputFloorGrace() *gatewayv1.Duration {
	if s.ThroughputFloor == nil {
		return nil
	}
	return s.ThroughputFloor.Grace
}
