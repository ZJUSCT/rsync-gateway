// Command rsync-gateway is the self-converging Kubernetes rsync gateway: a
// controller-runtime manager and the embedded rsync-proxy data plane in a
// single process. Every replica watches the same Gateway API resources,
// rebuilds the same desired routing table, applies it to its local data
// plane and writes the same statuses.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/ustclug/rsync-proxy/api/v1alpha1"
	"github.com/ustclug/rsync-proxy/internal/controller"
	"github.com/ustclug/rsync-proxy/internal/dataplane"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		adminAddr   string
		gracePeriod time.Duration
		metricsAddr string
		probeAddr   string
		zapOpts     zap.Options
	)
	// The rsync data plane listeners are static: pkg/server binds its
	// listeners once at startup, so the bind set cannot change without a
	// restart (listeners added later are reported with
	// Programmed=False/RestartRequired).
	bindAddrs := stringArray{vals: []string{":873"}}

	fs := flag.CommandLine
	fs.Var(&bindAddrs, "bind", "rsync listener address (repeatable)")
	fs.StringVar(&adminAddr, "admin", ":9080", "admin HTTP address serving /metrics and /status")
	fs.DurationVar(&gracePeriod, "grace-period", 30*time.Second, "grace period for draining active connections on shutdown")
	fs.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address for the controller metrics endpoint (0 disables)")
	fs.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address for the health probe endpoint (0 disables)")
	zapOpts.BindFlags(fs)
	ctrl.RegisterFlags(fs)
	flag.Parse()

	logf.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := logf.Log.WithName("rsync-gateway")

	// Data plane: apply an initial (possibly empty) table, bind the
	// listeners, then serve in the background.
	dp, err := dataplane.New(dataplane.Options{
		BindAddrs: bindAddrs.vals,
		AdminAddr: adminAddr,
	})
	if err != nil {
		return err
	}
	defer dp.Close()
	dp.Start()
	logger.Info("data plane started", "rsync", dp.Addrs(), "admin", dp.AdminAddr())

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := gatewayv1.Install(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readyz check: %w", err)
	}

	reconciler := &controller.GatewayReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Applier:   dp,
		BindAddrs: dp.Addrs(),
		// TODO: migrate to mgr.GetEventRecorder (events.k8s.io API); the
		// legacy recorder keeps the client-go Eventf signatures we use.
		Recorder: mgr.GetEventRecorderFor("rsync-gateway"), //nolint:staticcheck // deprecated; controller-runtime nolints this call internally
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup controller: %w", err)
	}

	// Stop the manager if the data plane fails asynchronously.
	ctx := ctrl.SetupSignalHandler()
	mgrCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case err := <-dp.Err():
			if err != nil {
				logger.Error(err, "data plane failed")
				cancel()
			}
		case <-mgrCtx.Done():
		}
	}()

	logger.Info("starting manager")
	startErr := mgr.Start(mgrCtx)

	// Drain active relay connections within the grace period.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), gracePeriod)
	defer drainCancel()
	if remaining := dp.Drain(drainCtx); remaining > 0 {
		logger.Info("finished draining with active connections left", "remaining", remaining)
	}
	if startErr != nil {
		return fmt.Errorf("manager exited: %w", startErr)
	}
	return nil
}

// stringArray is a repeatable string flag. The first explicit Set replaces
// the default value entirely (pflag StringArray semantics): without this,
// a default of {":873"} plus one --bind flag would bind the same address
// twice.
type stringArray struct {
	vals    []string
	changed bool
}

func (a *stringArray) String() string {
	out := ""
	for i, s := range a.vals {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func (a *stringArray) Set(v string) error {
	if !a.changed {
		a.vals = []string{v}
		a.changed = true
		return nil
	}
	a.vals = append(a.vals, v)
	return nil
}
