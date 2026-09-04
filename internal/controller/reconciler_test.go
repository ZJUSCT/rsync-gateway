package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
	"github.com/ustclug/rsync-proxy/pkg/server"
)

// fakeApplier captures the tables pushed into the data plane.
type fakeApplier struct {
	tables []*server.Config
	err    error
}

func (f *fakeApplier) Apply(cfg *server.Config) error {
	if f.err != nil {
		return f.err
	}
	f.tables = append(f.tables, cfg)
	return nil
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return scheme
}

type fixture struct {
	class  *gatewayv1.GatewayClass
	config *v1alpha1.GatewayConfig
	gw     *gatewayv1.Gateway
	route  *v1alpha1.RsyncRoute
	svc    *corev1.Service
}

func gatewayClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "rsync-class", Generation: 1},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: v1alpha1.ControllerName,
			ParametersRef: &gatewayv1.ParametersReference{
				Group: gatewayv1.Group(v1alpha1.GroupVersion.Group),
				Kind:  "GatewayConfig",
				Name:  "cfg",
			},
		},
	}
}

func gatewayConfig() *v1alpha1.GatewayConfig {
	motd := "hello from gateway"
	return &v1alpha1.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Generation: 1},
		Spec: v1alpha1.GatewayConfigSpec{
			Motd:                 &motd,
			MaxActiveConnections: int32Ptr(5),
		},
	}
}

func gatewayObj() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw", Generation: 1},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "rsync-class",
			Listeners: []gatewayv1.Listener{{
				Name:     "rsync",
				Port:     873,
				Protocol: v1alpha1.ProtocolRsync,
			}},
		},
	}
}

func rsyncRoute() *v1alpha1.RsyncRoute {
	return &v1alpha1.RsyncRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ubuntu", Generation: 1},
		Spec: v1alpha1.RsyncRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "gw"}},
			BackendRefs: []gatewayv1.BackendRef{{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name:      "ubuntu-svc",
					Namespace: nsPtr("default"),
					Port:      portPtr(873),
				},
			}},
		},
	}
}

func service(ns, name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func nsPtr(s string) *gatewayv1.Namespace { n := gatewayv1.Namespace(s); return &n }

func portPtr(p int32) *gatewayv1.PortNumber {
	return &p
}

func int32Ptr(v int32) *int32 { return &v }

// newTestReconciler builds a reconciler over a fake client with the given
// objects and a capturing fake applier.
func newTestReconciler(t *testing.T, fx fixture) (*GatewayReconciler, *fakeApplier, client.Client) {
	t.Helper()
	scheme := testScheme(t)

	objs := make([]client.Object, 0, 5)
	for _, o := range []client.Object{fx.class, fx.config, fx.gw, fx.route, fx.svc} {
		if o != nil {
			objs = append(objs, o)
		}
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&gatewayv1.GatewayClass{},
			&gatewayv1.Gateway{},
			&v1alpha1.GatewayConfig{},
			&v1alpha1.RsyncRoute{},
		).
		WithObjects(objs...).
		Build()

	applier := &fakeApplier{}
	r := &GatewayReconciler{
		Client:    c,
		Scheme:    scheme,
		Applier:   applier,
		BindAddrs: []string{":873"},
	}
	return r, applier, c
}

func reconcileOnce(t *testing.T, r *GatewayReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), RebuildRequest)
	require.NoError(t, err)
	return res
}

// TestReconcileHappyPath verifies the wiring end to end: route accepted,
// statuses written, and the data plane receiving the expected upstream
// table.
func TestReconcileHappyPath(t *testing.T) {
	r, applier, c := newTestReconciler(t, fixture{
		class:  gatewayClass(),
		config: gatewayConfig(),
		gw:     gatewayObj(),
		route:  rsyncRoute(),
		svc:    service("default", "ubuntu-svc"),
	})
	reconcileOnce(t, r)

	// The data plane received exactly one upstream with a deterministic
	// name, address and module list.
	require.Len(t, applier.tables, 1)
	table := applier.tables[0]
	require.Len(t, table.Upstreams, 1)
	up, ok := table.Upstreams["default-ubuntu-0"]
	require.True(t, ok, "upstream name should be <ns>-<route>-<index>, got %v", table.Upstreams)
	assert.Equal(t, "ubuntu-svc.default.svc:873", up.Address)
	assert.Equal(t, []string{"ubuntu"}, up.Modules, "effective modules default to the route name")
	assert.False(t, up.DiscoverModules)
	assert.False(t, up.UseProxyProtocol)
	assert.Equal(t, 5, up.MaxActiveConns, "per-upstream defaults come from GatewayConfig")
	assert.Equal(t, "hello from gateway", table.Proxy.Motd)

	// Route status: accepted with resolved refs on our parent entry.
	var route v1alpha1.RsyncRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ubuntu"}, &route))
	require.Len(t, route.Status.Parents, 1)
	parent := route.Status.Parents[0]
	assert.Equal(t, v1alpha1.ControllerName, parent.ControllerName)
	accepted := findCondition(t, parent.Conditions, "Accepted")
	assert.Equal(t, metav1.ConditionTrue, accepted.Status)
	assert.Equal(t, "Accepted", accepted.Reason)
	refs := findCondition(t, parent.Conditions, "ResolvedRefs")
	assert.Equal(t, metav1.ConditionTrue, refs.Status)

	// Gateway status: our listener is accepted and programmed (port 873
	// is in the static bind set).
	var gw gatewayv1.Gateway
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "gw"}, &gw))
	require.Len(t, gw.Status.Listeners, 1)
	lsAccepted := findCondition(t, gw.Status.Listeners[0].Conditions, "Accepted")
	assert.Equal(t, metav1.ConditionTrue, lsAccepted.Status)
	lsProgrammed := findCondition(t, gw.Status.Listeners[0].Conditions, "Programmed")
	assert.Equal(t, metav1.ConditionTrue, lsProgrammed.Status)

	// GatewayClass and GatewayConfig statuses: accepted.
	var class gatewayv1.GatewayClass
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rsync-class"}, &class))
	assert.Equal(t, metav1.ConditionTrue, findCondition(t, class.Status.Conditions, "Accepted").Status)

	var config v1alpha1.GatewayConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg"}, &config))
	assert.Equal(t, metav1.ConditionTrue, findCondition(t, config.Status.Conditions, "Accepted").Status)
}

// TestReconcileApplyError verifies that apply failures are reflected in the
// GatewayConfig status and returned for backoff.
func TestReconcileApplyError(t *testing.T) {
	r, applier, c := newTestReconciler(t, fixture{
		class:  gatewayClass(),
		config: gatewayConfig(),
		gw:     gatewayObj(),
		route:  rsyncRoute(),
		svc:    service("default", "ubuntu-svc"),
	})
	applier.err = fmt.Errorf("boom")

	_, err := r.Reconcile(context.Background(), RebuildRequest)
	require.Error(t, err)

	var config v1alpha1.GatewayConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg"}, &config))
	accepted := findCondition(t, config.Status.Conditions, "Accepted")
	assert.Equal(t, metav1.ConditionFalse, accepted.Status)
	assert.Equal(t, "Invalid", accepted.Reason)
	assert.Contains(t, accepted.Message, "boom")
}

// findCondition returns the condition with the given type, failing the test
// when absent.
func findCondition(t *testing.T, conds []metav1.Condition, typ string) metav1.Condition {
	t.Helper()
	for _, c := range conds {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("condition %q not found in %v", typ, conds)
	return metav1.Condition{}
}
