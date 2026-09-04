// Package controller implements the rsync-gateway reconciler: a single
// controller that watches every relevant resource and rebuilds the whole
// desired state (bindings, data-plane table and statuses) on each event.
// Every replica of the gateway performs the same rebuild, which makes
// status writes idempotent and last-write-wins benign (v0 runs without
// leader election).
package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
	"github.com/ustclug/rsync-proxy/internal/gateway"
	"github.com/ustclug/rsync-proxy/internal/translate"
	"github.com/ustclug/rsync-proxy/pkg/server"
)

// Applier pushes a computed routing table into the data plane.
type Applier interface {
	// Apply hot-swaps the data-plane configuration.
	Apply(cfg *server.Config) error
}

// RebuildRequest is the fixed reconcile request every watch event maps to:
// any change triggers one global rebuild.
var RebuildRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "gateway-rebuild"}}

// GatewayReconciler rebuilds bindings, the data-plane table and all API
// statuses from a snapshot of the cluster state.
type GatewayReconciler struct {
	client.Client
	// Scheme is the runtime scheme (used by the status client).
	Scheme *runtime.Scheme
	// Applier receives the computed data-plane configuration.
	Applier Applier
	// BindAddrs lists the static rsync bind addresses; listener status
	// reflects whether a listener port is served by them.
	BindAddrs []string
	// Recorder emits events on routes whose acceptance changed.
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.rsync.zjusct.io,resources=gatewayconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.rsync.zjusct.io,resources=gatewayconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.rsync.zjusct.io,resources=rsyncroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.rsync.zjusct.io,resources=rsyncroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile performs one global rebuild. The incoming request is ignored:
// every event maps to the same fixed request.
func (r *GatewayReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	snap, err := r.readSnapshot(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("read cluster state: %w", err)
	}

	claims := gateway.ClaimClasses(snap)
	results := gateway.BindRoutes(snap, claims)

	table, _, err := r.buildTable(snap, claims, results)
	if err != nil {
		// The computed table is invalid (e.g. a GatewayConfig spec that
		// only fails at translation time). Reflect it and requeue with
		// backoff.
		logger.Error(err, "building data-plane table failed")
		return ctrl.Result{}, r.writeStatuses(ctx, snap, claims, results, err)
	}

	applyErr := r.Applier.Apply(table)

	statusErr := r.writeStatuses(ctx, snap, claims, results, applyErr)
	if applyErr != nil {
		logger.Error(applyErr, "applying data-plane table failed")
		// Requeue with exponential backoff.
		return ctrl.Result{}, applyErr
	}
	return ctrl.Result{}, statusErr
}

// readSnapshot lists every watched resource through the manager cache.
func (r *GatewayReconciler) readSnapshot(ctx context.Context) (*gateway.Snapshot, error) {
	var (
		classes  gatewayv1.GatewayClassList
		configs  v1alpha1.GatewayConfigList
		gateways gatewayv1.GatewayList
		routes   v1alpha1.RsyncRouteList
		services corev1.ServiceList
		grants   gatewayv1.ReferenceGrantList
	)
	for _, item := range []struct {
		list client.ObjectList
		obj  client.Object
	}{
		{&classes, &gatewayv1.GatewayClass{}},
		{&configs, &v1alpha1.GatewayConfig{}},
		{&gateways, &gatewayv1.Gateway{}},
		{&routes, &v1alpha1.RsyncRoute{}},
		{&services, &corev1.Service{}},
		{&grants, &gatewayv1.ReferenceGrant{}},
	} {
		if err := r.List(ctx, item.list); err != nil {
			return nil, fmt.Errorf("list %T: %w", item.obj, err)
		}
	}

	classPtrs := make([]*gatewayv1.GatewayClass, 0, len(classes.Items))
	for i := range classes.Items {
		classPtrs = append(classPtrs, &classes.Items[i])
	}
	configPtrs := make([]*v1alpha1.GatewayConfig, 0, len(configs.Items))
	for i := range configs.Items {
		configPtrs = append(configPtrs, &configs.Items[i])
	}
	gatewayPtrs := make([]*gatewayv1.Gateway, 0, len(gateways.Items))
	for i := range gateways.Items {
		gatewayPtrs = append(gatewayPtrs, &gateways.Items[i])
	}
	routePtrs := make([]*v1alpha1.RsyncRoute, 0, len(routes.Items))
	for i := range routes.Items {
		routePtrs = append(routePtrs, &routes.Items[i])
	}
	servicePtrs := make([]*corev1.Service, 0, len(services.Items))
	for i := range services.Items {
		servicePtrs = append(servicePtrs, &services.Items[i])
	}
	grantPtrs := make([]*gatewayv1.ReferenceGrant, 0, len(grants.Items))
	for i := range grants.Items {
		grantPtrs = append(grantPtrs, &grants.Items[i])
	}
	return gateway.NewSnapshot(classPtrs, configPtrs, gatewayPtrs, routePtrs, servicePtrs, grantPtrs), nil
}

// buildTable computes the data-plane configuration from the binding
// results.
func (r *GatewayReconciler) buildTable(snap *gateway.Snapshot, claims map[string]gateway.ClassClaim, results map[types.NamespacedName][]gateway.ParentResult) (*server.Config, []translate.RouteInput, error) {
	// Primary GatewayConfig: the config of the lexicographically smallest
	// managed Gateway (namespace/name). It supplies the proxy-wide
	// settings when several gateways are served by this process.
	var primaryConfig *v1alpha1.GatewayConfigSpec
	var primaryGateway types.NamespacedName
	for gwKey, gw := range snap.Gateways {
		claim, ok := claims[string(gw.Spec.GatewayClassName)]
		if !ok || !claim.Claimed || !claim.Accepted {
			continue
		}
		if primaryConfig == nil || gwKey.String() < primaryGateway.String() {
			var spec *v1alpha1.GatewayConfigSpec
			if claim.Config != nil {
				spec = &claim.Config.Spec
			}
			primaryConfig = spec
			primaryGateway = gwKey
		}
	}

	inputs := make([]translate.RouteInput, 0, len(snap.RsyncRoutes))
	for routeKey, route := range snap.RsyncRoutes {
		boundGateways := boundGatewaysFor(results[routeKey])
		if len(boundGateways) == 0 {
			continue
		}
		backends, _ := gateway.ResolveBackends(snap, routeKey, route)
		input := translate.RouteInput{
			Route:   routeKey,
			Modules: gateway.EffectiveModules(route),
		}
		for _, b := range backends {
			input.Backends = append(input.Backends, translate.RouteBackend{
				Name:      b.Service.Name,
				Namespace: b.Service.Namespace,
				Port:      b.Port,
			})
		}
		// Per-route config: the config of the smallest bound gateway's
		// class.
		for _, gwKey := range boundGateways {
			gw := snap.Gateways[gwKey]
			if gw == nil {
				continue
			}
			if claim, ok := claims[string(gw.Spec.GatewayClassName)]; ok && claim.Config != nil {
				cfg := claim.Config.Spec
				input.Config = &cfg
				break
			}
		}
		inputs = append(inputs, input)
	}

	table, err := translate.BuildServerConfig(primaryConfig, inputs)
	return table, inputs, err
}

// boundGatewaysFor returns the deduplicated keys of the gateways a route is
// accepted at, sorted by namespace/name.
func boundGatewaysFor(perParent []gateway.ParentResult) []types.NamespacedName {
	var out []types.NamespacedName
	seen := make(map[types.NamespacedName]bool)
	for _, res := range perParent {
		if !res.Accepted || seen[res.Gateway] {
			continue
		}
		seen[res.Gateway] = true
		out = append(out, res.Gateway)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// writeStatuses writes GatewayClass, Gateway, GatewayConfig and RsyncRoute
// statuses. Statuses are only written when their content actually changes,
// which keeps the reconciler from hot-looping on its own status updates.
// applyErr, when non-nil, is reflected on the GatewayConfig Accepted
// conditions.
func (r *GatewayReconciler) writeStatuses(
	ctx context.Context,
	snap *gateway.Snapshot,
	claims map[string]gateway.ClassClaim,
	results map[types.NamespacedName][]gateway.ParentResult,
	applyErr error,
) error {
	servedPorts := gateway.ServedPorts(r.BindAddrs)

	// GatewayClass statuses.
	for name, gc := range snap.GatewayClasses {
		claim := claims[name]
		cond := gateway.ClassCondition(gc, claim)
		if cond == nil {
			continue
		}
		stable := gateway.StableCondition(gc.Status.Conditions, *cond)
		if gateway.ConditionsEqual(gc.Status.Conditions, []metav1.Condition{stable}) {
			continue
		}
		gc.Status.Conditions = gateway.MergeConditions(gc.Status.Conditions, []metav1.Condition{stable})
		if err := r.Status().Update(ctx, gc); err != nil {
			return fmt.Errorf("update GatewayClass %s status: %w", name, err)
		}
	}

	// Gateway statuses.
	for gwKey, gw := range snap.Gateways {
		claim, ok := claims[string(gw.Spec.GatewayClassName)]
		if !ok || !claim.Claimed || !claim.Accepted {
			continue
		}
		desired := gateway.BuildGatewayStatus(gw, claim, servedPorts)
		if gateway.ConditionsEqual(gw.Status.Conditions, desired.Conditions) &&
			gateway.ListenerStatusesEqual(gw.Status.Listeners, desired.Listeners) {
			continue
		}
		gw.Status.Conditions = desired.Conditions
		gw.Status.Listeners = desired.Listeners
		if err := r.Status().Update(ctx, gw); err != nil {
			return fmt.Errorf("update Gateway %s status: %w", gwKey, err)
		}
	}

	// RsyncRoute statuses.
	for routeKey, route := range snap.RsyncRoutes {
		perParent, ok := results[routeKey]
		if !ok {
			continue
		}
		var backends gateway.BackendStatus
		if routeAccepted(perParent) {
			_, backends = gateway.ResolveBackends(snap, routeKey, route)
		} else {
			backends = gateway.BackendStatus{Resolved: true, Reason: gatewayv1.RouteReasonResolvedRefs}
		}
		desiredParents := gateway.BuildRouteStatus(route, perParent, backends)
		changed := !gateway.ParentStatusesEqual(route.Status.Parents, desiredParents)
		if changed {
			old := gateway.FindParentConditions(route.Status.Parents, perParent)
			route.Status.Parents = desiredParents
			if err := r.Status().Update(ctx, route); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("update RsyncRoute %s status: %w", routeKey, err)
			}
			r.emitRouteEvents(route, perParent, old)
		}
	}

	// GatewayConfig statuses: every config referenced by a claimed class
	// reflects the outcome of the rebuild.
	for _, claim := range claims {
		if !claim.Claimed || claim.Config == nil {
			continue
		}
		config := snap.GatewayConfigs[claim.Config.Name]
		if config == nil {
			continue
		}
		var cond metav1.Condition
		if applyErr != nil {
			cond = metav1.Condition{
				Type:               string(gatewayv1.GatewayConditionAccepted),
				Status:             metav1.ConditionFalse,
				Reason:             string(gatewayv1.GatewayReasonInvalid),
				Message:            fmt.Sprintf("failed to apply data-plane configuration: %v", applyErr),
				ObservedGeneration: config.Generation,
			}
		} else {
			cond = metav1.Condition{
				Type:               string(gatewayv1.GatewayConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.GatewayReasonAccepted),
				Message:            "configuration applied to the data plane",
				ObservedGeneration: config.Generation,
			}
		}
		cond = gateway.StableCondition(config.Status.Conditions, cond)
		if gateway.ConditionsEqual(config.Status.Conditions, []metav1.Condition{cond}) {
			continue
		}
		config.Status.Conditions = gateway.MergeConditions(config.Status.Conditions, []metav1.Condition{cond})
		if err := r.Status().Update(ctx, config); err != nil {
			return fmt.Errorf("update GatewayConfig %s status: %w", claim.Config.Name, err)
		}
	}
	return nil
}

// emitRouteEvents emits a Warning event when a parent result flipped from
// its previously observed state.
func (r *GatewayReconciler) emitRouteEvents(route *v1alpha1.RsyncRoute, perParent []gateway.ParentResult, previous map[int]metav1.Condition) {
	if r.Recorder == nil {
		return
	}
	for i, res := range perParent {
		if res.Accepted {
			continue
		}
		prev, ok := previous[i]
		if ok && prev.Status == metav1.ConditionFalse && prev.Reason == string(res.Reason) && prev.Message == res.Message {
			continue
		}
		r.Recorder.Eventf(route, corev1.EventTypeWarning, string(res.Reason), "parent %q: %s", gateway.ParentRefString(res.ParentRef), res.Message)
	}
}

func routeAccepted(perParent []gateway.ParentResult) bool {
	for _, res := range perParent {
		if res.Accepted {
			return true
		}
	}
	return false
}

// SetupWithManager registers the single global-rebuild controller watching
// every relevant resource kind.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("rsync-gateway").
		WatchesRawSource(source.Kind(mgr.GetCache(), &gatewayv1.GatewayClass{}, rebuildHandler[*gatewayv1.GatewayClass]())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &gatewayv1.Gateway{}, rebuildHandler[*gatewayv1.Gateway]())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &v1alpha1.GatewayConfig{}, rebuildHandler[*v1alpha1.GatewayConfig]())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &v1alpha1.RsyncRoute{}, rebuildHandler[*v1alpha1.RsyncRoute]())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &corev1.Service{}, rebuildHandler[*corev1.Service]())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &gatewayv1.ReferenceGrant{}, rebuildHandler[*gatewayv1.ReferenceGrant]())).
		Complete(r)
}

// rebuildHandler maps every event, regardless of the object, to the single
// fixed rebuild request.
func rebuildHandler[T client.Object]() handler.TypedEventHandler[T, reconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(context.Context, T) []reconcile.Request {
		return []reconcile.Request{RebuildRequest}
	})
}
