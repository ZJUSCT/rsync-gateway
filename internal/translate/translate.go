// Package translate maps gateway API objects into the data-plane
// configuration consumed by pkg/server (rsync-proxy).
package translate

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
	"github.com/ustclug/rsync-proxy/pkg/server"
)

// RouteBackend is a backendRef that resolved to a Service and a port.
type RouteBackend struct {
	// Name is the Service name.
	Name string
	// Namespace is the Service namespace.
	Namespace string
	// Port is the (required) backend port.
	Port int32
}

// RouteInput carries everything needed to translate one bound RsyncRoute
// into upstream table entries.
type RouteInput struct {
	// Route is the namespace/name of the RsyncRoute.
	Route types.NamespacedName
	// Modules is the route's effective module list.
	Modules []string
	// Backends are the resolved, usable (weight > 0) backends.
	Backends []RouteBackend
	// Config is the GatewayConfig spec in effect for this route, taken
	// from the GatewayClass of the Gateway the route is bound to. May be
	// nil, in which case rsync-proxy defaults apply to the route's
	// upstreams.
	Config *v1alpha1.GatewayConfigSpec
}

// UpstreamName derives the data-plane upstream name for the backend at the
// given index of a route: "<ns>-<name>-<index>", lowercased, reduced to the
// [a-z0-9-] character set and truncated to 63 characters.
func UpstreamName(route types.NamespacedName, index int) string {
	raw := fmt.Sprintf("%s-%s-%d", route.Namespace, route.Name, index)
	raw = strings.ToLower(raw)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// DurationSecs converts a GEP-2257 duration to whole seconds. A nil pointer
// yields 0 (the "unset" sentinel understood by rsync-proxy). Sub-second
// durations truncate to 0, mirroring the second-granularity of the
// underlying rsync-proxy settings.
func DurationSecs(d *gatewayv1.Duration) (int, error) {
	if d == nil {
		return 0, nil
	}
	dur, err := time.ParseDuration(string(*d))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", *d, err)
	}
	if dur < 0 {
		return 0, fmt.Errorf("duration must be non-negative, got %s", *d)
	}
	return int(dur / time.Second), nil
}

// ProxySettings translates a GatewayConfigSpec into the proxy-wide settings
// of the data plane. Listen/ListenTLS/ListenHTTP are intentionally left
// empty: listeners are owned by the dataplane wrapper, not by translation.
// Throughput-floor defaults mirror pkg/server.loadConfig: the window
// defaults to 60s when a floor is configured without one, and the grace
// period defaults to the effective window.
func ProxySettings(cfg *v1alpha1.GatewayConfigSpec) (server.ProxySettings, error) {
	if cfg == nil {
		return server.ProxySettings{}, nil
	}
	if err := cfg.Validate(); err != nil {
		return server.ProxySettings{}, err
	}

	idle, err := DurationSecs(cfg.RelayIdleTimeout)
	if err != nil {
		return server.ProxySettings{}, fmt.Errorf("relayIdleTimeout: %w", err)
	}
	maxDuration, err := DurationSecs(cfg.RelayMaxDuration)
	if err != nil {
		return server.ProxySettings{}, fmt.Errorf("relayMaxDuration: %w", err)
	}
	keepAlive, err := DurationSecs(cfg.TCPKeepAlive)
	if err != nil {
		return server.ProxySettings{}, fmt.Errorf("tcpKeepAlive: %w", err)
	}
	dialTimeout, err := DurationSecs(cfg.DialTimeout)
	if err != nil {
		return server.ProxySettings{}, fmt.Errorf("dialTimeout: %w", err)
	}

	settings := server.ProxySettings{
		RelayIdleTimeoutSecs: idle,
		RelayMaxDurationSecs: maxDuration,
		TCPKeepAliveSecs:     keepAlive,
		DialTimeoutSecs:      dialTimeout,
		PerIPMaxActiveConns:  int(derefInt32(cfg.PerIPMaxActiveConnections)),
	}
	if cfg.Motd != nil {
		settings.Motd = *cfg.Motd
	}

	var minBytes int64
	var window, grace int
	if cfg.ThroughputFloor != nil {
		if cfg.ThroughputFloor.MinBytes != nil {
			minBytes = *cfg.ThroughputFloor.MinBytes
		}
		window, err = DurationSecs(cfg.ThroughputFloor.Window)
		if err != nil {
			return server.ProxySettings{}, fmt.Errorf("throughputFloor.window: %w", err)
		}
		grace, err = DurationSecs(cfg.ThroughputFloor.Grace)
		if err != nil {
			return server.ProxySettings{}, fmt.Errorf("throughputFloor.grace: %w", err)
		}
	}
	if minBytes > 0 {
		if window == 0 {
			window = 60
		}
		if grace == 0 {
			grace = window
		}
	}
	settings.MinThroughputBytes = minBytes
	settings.MinThroughputWindowSecs = window
	settings.MinThroughputGraceSecs = grace

	return settings, nil
}

// BuildServerConfig assembles the data-plane configuration: the proxy-wide
// settings from the primary GatewayConfig spec plus one upstream per
// (route, backend).
func BuildServerConfig(primary *v1alpha1.GatewayConfigSpec, routes []RouteInput) (*server.Config, error) {
	proxy, err := ProxySettings(primary)
	if err != nil {
		return nil, fmt.Errorf("invalid GatewayConfig: %w", err)
	}
	cfg := &server.Config{
		Proxy:     proxy,
		Upstreams: make(map[string]*server.Upstream),
	}
	for _, route := range routes {
		spec := route.Config
		if spec == nil {
			spec = primary
		}
		if spec != nil {
			if err := spec.Validate(); err != nil {
				return nil, fmt.Errorf("route %s: invalid GatewayConfig: %w", route.Route, err)
			}
		}
		for i, backend := range route.Backends {
			name := UpstreamName(route.Route, i)
			cfg.Upstreams[name] = &server.Upstream{
				Address:             fmt.Sprintf("%s.%s.svc:%d", backend.Name, backend.Namespace, backend.Port),
				Modules:             append([]string(nil), route.Modules...),
				DiscoverModules:     false,
				UseProxyProtocol:    false,
				MaxActiveConns:      int(derefInt32(spec.MaxActiveConnections)),
				MaxQueuedConns:      int(derefInt32(spec.MaxQueuedConnections)),
				PerIPMaxActiveConns: int(derefInt32(spec.PerIPMaxActiveConnections)),
			}
		}
	}
	return cfg, nil
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}
