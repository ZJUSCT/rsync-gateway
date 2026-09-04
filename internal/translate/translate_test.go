package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
	"github.com/ustclug/rsync-proxy/pkg/server"
)

func duration(s string) *gatewayv1.Duration {
	d := gatewayv1.Duration(s)
	return &d
}

func int32Ptr(v int32) *int32 { return &v }
func int64Ptr(v int64) *int64 { return &v }
func strPtr(s string) *string { return &s }

// TestProxySettings covers the GatewayConfig → server.ProxySettings field
// mapping, the throughput-floor duration defaults, and rejection of invalid
// values.
func TestProxySettings(t *testing.T) {
	t.Run("full mapping", func(t *testing.T) {
		cfg := &v1alpha1.GatewayConfigSpec{
			Motd:                      strPtr("Welcome"),
			RelayIdleTimeout:          duration("600s"),
			RelayMaxDuration:          duration("2h"),
			TCPKeepAlive:              duration("45s"),
			DialTimeout:               duration("5s"),
			PerIPMaxActiveConnections: int32Ptr(4),
			MaxActiveConnections:      int32Ptr(10),
			MaxQueuedConnections:      int32Ptr(20),
			ThroughputFloor: &v1alpha1.ThroughputFloorSpec{
				MinBytes: int64Ptr(65536),
				Window:   duration("60s"),
				Grace:    duration("30s"),
			},
		}
		settings, err := ProxySettings(cfg)
		require.NoError(t, err)
		assert.Equal(t, "Welcome", settings.Motd)
		assert.Equal(t, 600, settings.RelayIdleTimeoutSecs)
		assert.Equal(t, 7200, settings.RelayMaxDurationSecs)
		assert.Equal(t, 45, settings.TCPKeepAliveSecs)
		assert.Equal(t, 5, settings.DialTimeoutSecs)
		assert.Equal(t, 4, settings.PerIPMaxActiveConns)
		assert.Equal(t, int64(65536), settings.MinThroughputBytes)
		assert.Equal(t, 60, settings.MinThroughputWindowSecs)
		assert.Equal(t, 30, settings.MinThroughputGraceSecs)
	})

	t.Run("floor duration defaults mirror pkg/server", func(t *testing.T) {
		cfg := &v1alpha1.GatewayConfigSpec{
			ThroughputFloor: &v1alpha1.ThroughputFloorSpec{MinBytes: int64Ptr(1024)},
		}
		settings, err := ProxySettings(cfg)
		require.NoError(t, err)
		// window defaults to 60s, grace defaults to the window.
		assert.Equal(t, 60, settings.MinThroughputWindowSecs)
		assert.Equal(t, 60, settings.MinThroughputGraceSecs)
	})

	t.Run("invalid values rejected", func(t *testing.T) {
		_, err := ProxySettings(&v1alpha1.GatewayConfigSpec{RelayIdleTimeout: duration("soon")})
		assert.Error(t, err)
		_, err = ProxySettings(&v1alpha1.GatewayConfigSpec{MaxActiveConnections: int32Ptr(-1)})
		assert.Error(t, err)
	})
}

func TestBuildServerConfig(t *testing.T) {
	primary := &v1alpha1.GatewayConfigSpec{
		Motd:                 strPtr("hello"),
		MaxActiveConnections: int32Ptr(10),
	}
	routes := []RouteInput{
		{
			Route:   types.NamespacedName{Namespace: "default", Name: "ubuntu"},
			Modules: []string{"ubuntu", "ubuntu-cd"},
			Backends: []RouteBackend{
				{Name: "a", Namespace: "default", Port: 873},
				{Name: "b", Namespace: "backend", Port: 9000},
			},
			Config: primary,
		},
		{
			Route:    types.NamespacedName{Namespace: "other", Name: "kernel"},
			Modules:  []string{"kernel"},
			Backends: []RouteBackend{{Name: "c", Namespace: "other", Port: 873}},
			Config:   nil, // falls back to the primary spec
		},
	}

	cfg, err := BuildServerConfig(primary, routes)
	require.NoError(t, err)

	assert.Equal(t, "hello", cfg.Proxy.Motd)
	require.Len(t, cfg.Upstreams, 3)

	up := cfg.Upstreams["default-ubuntu-0"]
	require.NotNil(t, up)
	assert.Equal(t, "a.default.svc:873", up.Address)
	assert.Equal(t, []string{"ubuntu", "ubuntu-cd"}, up.Modules)
	assert.False(t, up.DiscoverModules)
	assert.False(t, up.UseProxyProtocol)
	assert.Equal(t, 10, up.MaxActiveConns)

	up2 := cfg.Upstreams["default-ubuntu-1"]
	require.NotNil(t, up2)
	assert.Equal(t, "b.backend.svc:9000", up2.Address)
	assert.Equal(t, []string{"ubuntu", "ubuntu-cd"}, up2.Modules, "modules are cloned per upstream")

	up3 := cfg.Upstreams["other-kernel-0"]
	require.NotNil(t, up3)
	assert.Equal(t, "c.other.svc:873", up3.Address)
	assert.Equal(t, []string{"kernel"}, up3.Modules)
}

func TestBuildServerConfigEmpty(t *testing.T) {
	cfg, err := BuildServerConfig(nil, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.Upstreams)
	assert.Equal(t, server.ProxySettings{}, cfg.Proxy)
}
