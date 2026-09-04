package gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
)

func service(ns, name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func rsyncListener(name string, port int32) gatewayv1.Listener {
	return gatewayv1.Listener{Name: gatewayv1.SectionName(name), Port: port, Protocol: v1alpha1.ProtocolRsync}
}

func route(ns, name string, parentRefs ...gatewayv1.ParentReference) *v1alpha1.RsyncRoute {
	if parentRefs == nil {
		parentRefs = []gatewayv1.ParentReference{{
			Group: ptrGroup(gatewayAPIGroup),
			Kind:  ptrKind("Gateway"),
			Name:  "gw",
		}}
	}
	return &v1alpha1.RsyncRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1alpha1.RsyncRouteSpec{
			ParentRefs:  parentRefs,
			BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: backendRef(ns, "backend", 873)}},
		},
	}
}

func backendRef(ns, name string, port int32) gatewayv1.BackendObjectReference {
	return gatewayv1.BackendObjectReference{
		Name:      gatewayv1.ObjectName(name),
		Namespace: ptrNamespace(ns),
		Port:      ptrPort(port),
	}
}

func ptrGroup(s string) *gatewayv1.Group                           { g := gatewayv1.Group(s); return &g }
func ptrKind(s string) *gatewayv1.Kind                             { k := gatewayv1.Kind(s); return &k }
func ptrNamespace(s string) *gatewayv1.Namespace                   { n := gatewayv1.Namespace(s); return &n }
func ptrSectionName(s string) *gatewayv1.SectionName               { s2 := gatewayv1.SectionName(s); return &s2 }
func ptrPort(p int32) *gatewayv1.PortNumber                        { return &p }
func ptrFrom(f gatewayv1.FromNamespaces) *gatewayv1.FromNamespaces { return &f }

func ptrObjName(s string) *gatewayv1.ObjectName {
	n := gatewayv1.ObjectName(s)
	return &n
}

// testFixture builds a snapshot with one claimed, accepted GatewayClass
// ("class") backed by GatewayConfig "cfg", and one managed Gateway
// "default/gw" with a single rsync listener on port 873.
func testFixture(t *testing.T, mutate ...func(*Snapshot)) (*Snapshot, map[string]ClassClaim) {
	t.Helper()

	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "class"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: v1alpha1.ControllerName,
			ParametersRef: &gatewayv1.ParametersReference{
				Group: gatewayv1.Group(v1alpha1.GroupVersion.Group),
				Kind:  "GatewayConfig",
				Name:  "cfg",
			},
		},
	}
	cfg := &v1alpha1.GatewayConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg"}}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "class",
			Listeners:        []gatewayv1.Listener{rsyncListener("rsync", 873)},
		},
	}
	snap := NewSnapshot(
		[]*gatewayv1.GatewayClass{gc},
		[]*v1alpha1.GatewayConfig{cfg},
		[]*gatewayv1.Gateway{gw},
		nil,
		[]*corev1.Service{service("default", "backend")},
		nil,
	)
	for _, m := range mutate {
		m(snap)
	}
	return snap, ClaimClasses(snap)
}

// TestBindRoutes covers the parentRef decisions that decide whether a route
// reaches the data plane at all.
func TestBindRoutes(t *testing.T) {
	testCases := []struct {
		name       string
		parentRefs []gatewayv1.ParentReference
		wantOK     bool
		wantReason gatewayv1.RouteConditionReason
	}{
		{
			name:       "defaults bind whole gateway",
			parentRefs: nil, // filled in as the plain Gateway ref
			wantOK:     true,
			wantReason: gatewayv1.RouteReasonAccepted,
		},
		{
			name: "missing gateway",
			parentRefs: []gatewayv1.ParentReference{{
				Group: ptrGroup(gatewayAPIGroup),
				Kind:  ptrKind("Gateway"),
				Name:  "missing",
			}},
			wantOK:     false,
			wantReason: gatewayv1.RouteReasonNoMatchingParent,
		},
		{
			name: "unknown sectionName",
			parentRefs: []gatewayv1.ParentReference{{
				Group:       ptrGroup(gatewayAPIGroup),
				Kind:        ptrKind("Gateway"),
				Name:        "gw",
				SectionName: ptrSectionName("nope"),
			}},
			wantOK:     false,
			wantReason: gatewayv1.RouteReasonNoMatchingParent,
		},
		{
			name: "sectionName pointing at foreign-protocol listener",
			parentRefs: []gatewayv1.ParentReference{{
				Group:       ptrGroup(gatewayAPIGroup),
				Kind:        ptrKind("Gateway"),
				Name:        "gw",
				SectionName: ptrSectionName("http"),
			}},
			wantOK:     false,
			wantReason: gatewayv1.RouteReasonNoMatchingParent,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			parentRefs := tc.parentRefs
			if parentRefs == nil {
				parentRefs = []gatewayv1.ParentReference{{Name: "gw"}}
			}
			snap, claims := testFixture(t, func(s *Snapshot) {
				s.Gateways[types.NamespacedName{Namespace: "default", Name: "gw"}].Spec.Listeners = append(
					s.Gateways[types.NamespacedName{Namespace: "default", Name: "gw"}].Spec.Listeners,
					gatewayv1.Listener{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
				)
				s.RsyncRoutes[types.NamespacedName{Namespace: "default", Name: "r"}] = route("default", "r", parentRefs...)
			})
			results := BindRoutes(snap, claims)
			res := results[types.NamespacedName{Namespace: "default", Name: "r"}]
			require.Len(t, res, 1)
			assert.Equal(t, tc.wantOK, res[0].Accepted)
			assert.Equal(t, tc.wantReason, res[0].Reason)
		})
	}
}

// TestBindRoutesAllowedRoutes covers listener-level admission.
func TestBindRoutesAllowedRoutes(t *testing.T) {
	testCases := []struct {
		name       string
		allowed    *gatewayv1.AllowedRoutes
		routeNS    string
		wantOK     bool
		wantReason gatewayv1.RouteConditionReason
	}{
		{
			name:       "no allowedRoutes binds",
			allowed:    nil,
			routeNS:    "default",
			wantOK:     true,
			wantReason: gatewayv1.RouteReasonAccepted,
		},
		{
			name: "kinds without RsyncRoute are NotAllowedByListeners",
			allowed: &gatewayv1.AllowedRoutes{Kinds: []gatewayv1.RouteGroupKind{
				{Group: ptrGroup(gatewayAPIGroup), Kind: "HTTPRoute"},
			}},
			routeNS:    "default",
			wantOK:     false,
			wantReason: gatewayv1.RouteReasonNotAllowedByListeners,
		},
		{
			name:       "namespaces From=Same rejects other namespace",
			allowed:    &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptrFrom(gatewayv1.NamespacesFromSame)}},
			routeNS:    "other",
			wantOK:     false,
			wantReason: gatewayv1.RouteReasonNotAllowedByListeners,
		},
		{
			name:       "namespaces From=All accepts other namespace",
			allowed:    &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptrFrom(gatewayv1.NamespacesFromAll)}},
			routeNS:    "other",
			wantOK:     true,
			wantReason: gatewayv1.RouteReasonAccepted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			snap, claims := testFixture(t, func(s *Snapshot) {
				gw := s.Gateways[types.NamespacedName{Namespace: "default", Name: "gw"}]
				gw.Spec.Listeners[0].AllowedRoutes = tc.allowed
				parent := gatewayv1.ParentReference{Name: "gw"}
				if tc.routeNS != "default" {
					parent.Namespace = ptrNamespace("default")
				}
				s.RsyncRoutes[types.NamespacedName{Namespace: tc.routeNS, Name: "r"}] = route(tc.routeNS, "r", parent)
			})
			results := BindRoutes(snap, claims)
			res := results[types.NamespacedName{Namespace: tc.routeNS, Name: "r"}]
			require.Len(t, res, 1)
			assert.Equal(t, tc.wantOK, res[0].Accepted)
			assert.Equal(t, tc.wantReason, res[0].Reason)
		})
	}
}

// TestModuleConflicted verifies module precedence: the older route wins,
// ties are broken by namespace/name, and the default module name is the
// route name.
func TestModuleConflicted(t *testing.T) {
	base := time.Now().Add(-time.Hour)

	t.Run("older route wins", func(t *testing.T) {
		snap, claims := testFixture(t, func(s *Snapshot) {
			old := route("default", "old")
			old.CreationTimestamp = metav1.NewTime(base)
			newer := route("default", "newer")
			newer.CreationTimestamp = metav1.NewTime(base.Add(time.Minute))
			newer.Spec.ModuleNames = []string{"old"} // conflicts with route named "old"
			s.RsyncRoutes[types.NamespacedName{Namespace: "default", Name: "old"}] = old
			s.RsyncRoutes[types.NamespacedName{Namespace: "default", Name: "newer"}] = newer
		})
		results := BindRoutes(snap, claims)
		assert.True(t, results[types.NamespacedName{Namespace: "default", Name: "old"}][0].Accepted)
		conflicted := results[types.NamespacedName{Namespace: "default", Name: "newer"}][0]
		assert.False(t, conflicted.Accepted)
		assert.Equal(t, ReasonModuleConflicted, conflicted.Reason)
		assert.Contains(t, conflicted.Message, `default/old`)
		assert.Contains(t, conflicted.Message, `"old"`)
	})

	t.Run("timestamp tie broken by name", func(t *testing.T) {
		snap, claims := testFixture(t, func(s *Snapshot) {
			a := route("default", "aaa")
			a.CreationTimestamp = metav1.NewTime(base)
			b := route("default", "bbb")
			b.CreationTimestamp = metav1.NewTime(base) // same timestamp
			for _, r := range []*v1alpha1.RsyncRoute{a, b} {
				r.Spec.ModuleNames = []string{"shared"}
				s.RsyncRoutes[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = r
			}
		})
		results := BindRoutes(snap, claims)
		assert.True(t, results[types.NamespacedName{Namespace: "default", Name: "aaa"}][0].Accepted)
		assert.Equal(t, ReasonModuleConflicted, results[types.NamespacedName{Namespace: "default", Name: "bbb"}][0].Reason)
	})
}

// TestResolveBackends covers backend resolution including the ReferenceGrant
// handshake for cross-namespace backends.
func TestResolveBackends(t *testing.T) {
	testCases := []struct {
		name         string
		mutate       func(*Snapshot)
		backendRefs  []gatewayv1.BackendRef
		wantResolved bool
		wantReason   gatewayv1.RouteConditionReason
		wantUsable   int
	}{
		{
			name:         "all resolve",
			backendRefs:  []gatewayv1.BackendRef{{BackendObjectReference: backendRef("default", "backend", 873)}},
			wantResolved: true,
			wantReason:   gatewayv1.RouteReasonResolvedRefs,
			wantUsable:   1,
		},
		{
			name:         "missing service",
			backendRefs:  []gatewayv1.BackendRef{{BackendObjectReference: backendRef("default", "missing", 873)}},
			wantResolved: false,
			wantReason:   gatewayv1.RouteReasonBackendNotFound,
		},
		{
			name: "cross namespace without grant",
			mutate: func(s *Snapshot) {
				s.Services[types.NamespacedName{Namespace: "other", Name: "backend"}] = service("other", "backend")
			},
			backendRefs:  []gatewayv1.BackendRef{{BackendObjectReference: backendRef("other", "backend", 873)}},
			wantResolved: false,
			wantReason:   gatewayv1.RouteReasonRefNotPermitted,
		},
		{
			name: "cross namespace with grant",
			mutate: func(s *Snapshot) {
				s.Services[types.NamespacedName{Namespace: "other", Name: "backend"}] = service("other", "backend")
				s.ReferenceGrants["other"] = append(s.ReferenceGrants["other"], &gatewayv1.ReferenceGrant{
					ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "grant"},
					Spec: gatewayv1.ReferenceGrantSpec{
						From: []gatewayv1.ReferenceGrantFrom{{
							Group:     gatewayv1.Group(v1alpha1.GroupVersion.Group),
							Kind:      v1alpha1.RouteKind,
							Namespace: "default",
						}},
						To: []gatewayv1.ReferenceGrantTo{{
							Group: "",
							Kind:  "Service",
							Name:  ptrObjName("backend"),
						}},
					},
				})
			},
			backendRefs:  []gatewayv1.BackendRef{{BackendObjectReference: backendRef("other", "backend", 873)}},
			wantResolved: true,
			wantReason:   gatewayv1.RouteReasonResolvedRefs,
			wantUsable:   1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			snap, _ := testFixture(t, func(s *Snapshot) {
				if tc.mutate != nil {
					tc.mutate(s)
				}
			})
			routeKey := types.NamespacedName{Namespace: "default", Name: "r"}
			r := route("default", "r")
			r.Spec.BackendRefs = tc.backendRefs
			backends, status := ResolveBackends(snap, routeKey, r)
			assert.Equal(t, tc.wantResolved, status.Resolved)
			assert.Equal(t, tc.wantReason, status.Reason)
			assert.Len(t, backends, tc.wantUsable)
		})
	}
}

// TestBuildRouteStatusPreservesForeignEntries verifies that parent entries
// written by other controllers survive a rebuild.
func TestBuildRouteStatusPreservesForeignEntries(t *testing.T) {
	r := route("default", "r")
	foreign := gatewayv1.RouteParentStatus{
		ParentRef:      r.Spec.ParentRefs[0],
		ControllerName: "example.com/other",
		Conditions: []metav1.Condition{{
			Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted",
		}},
	}
	r.Status.Parents = []gatewayv1.RouteParentStatus{foreign}

	accepted := ParentResult{
		ParentRef: r.Spec.ParentRefs[0],
		Gateway:   types.NamespacedName{Namespace: "default", Name: "gw"},
		Accepted:  true, Reason: gatewayv1.RouteReasonAccepted,
	}
	parents := BuildRouteStatus(r, []ParentResult{accepted}, BackendStatus{Resolved: true})
	require.Len(t, parents, 2)
	assert.Equal(t, gatewayv1.GatewayController("example.com/other"), parents[0].ControllerName)
	assert.Equal(t, v1alpha1.ControllerName, parents[1].ControllerName)
}

// TestBuildRouteStatusStableTimestamps ensures re-running the status build
// does not bump LastTransitionTime when nothing changed.
func TestBuildRouteStatusStableTimestamps(t *testing.T) {
	r := route("default", "r")
	accepted := ParentResult{
		ParentRef: r.Spec.ParentRefs[0],
		Gateway:   types.NamespacedName{Namespace: "default", Name: "gw"},
		Accepted:  true, Reason: gatewayv1.RouteReasonAccepted,
	}
	first := BuildRouteStatus(r, []ParentResult{accepted}, BackendStatus{Resolved: true})
	r.Status.Parents = first
	second := BuildRouteStatus(r, []ParentResult{accepted}, BackendStatus{Resolved: true})
	require.Len(t, second, 1)
	assert.Equal(t, first[0].Conditions[0].LastTransitionTime, second[0].Conditions[0].LastTransitionTime)
}

func TestServedPorts(t *testing.T) {
	assert.Equal(t, []int32{873, 9080}, ServedPorts([]string{":873", "0.0.0.0:9080", "127.0.0.1:873"}))
	assert.Empty(t, ServedPorts([]string{"/run/rsync.sock", "no-port"}))
}
