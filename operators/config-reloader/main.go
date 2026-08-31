// Command config-reloader runs the operator's manager process: the
// long-running binary that watches ReloadTrigger/ConfigMap/Secret/
// Deployment objects and drives the reconcile loop in
// controllers/reloadtrigger_controller.go. This is the same binary
// packaged by the Dockerfile and run as config/manager/manager.yaml's
// Deployment in-cluster.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	reloaderv1alpha1 "config-reloader/api/v1alpha1"
	"config-reloader/controllers"
)

var (
	// scheme is the process-wide type registry: every Kind the manager's
	// client needs to read/write must be registered here before
	// ctrl.NewManager is called.
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// clientgoscheme registers all the built-in Kinds (Deployment,
	// ConfigMap, Secret, Event, ...) this controller touches.
	// reloaderv1alpha1.AddToScheme registers our one custom Kind,
	// ReloadTrigger — this is the func defined in
	// api/v1alpha1/groupversion_info.go. utilruntime.Must panics on error,
	// which is intentional here: a scheme registration failure is a
	// startup-time programming error, not a runtime condition worth
	// handling gracefully.
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(reloaderv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.Parse()

	ctrl.SetLogger(ctrlZapLogger())

	// ctrl.NewManager is the top-level object controller-runtime is built
	// around: it owns the shared informer cache (so multiple watches on
	// the same Kind share one underlying List+Watch to the API server
	// instead of each opening their own), the leader-election machinery
	// (disabled here — see below), and the lifecycle of every controller
	// registered against it.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		// LeaderElection is the mechanism that lets multiple replicas of a
		// controller run for high availability while guaranteeing only
		// one is actively reconciling at a time. It's explicitly false
		// here: config/manager/manager.yaml runs replicas: 1, so there's
		// no second instance to coordinate with — turning this on would
		// be inert complexity, not a safety net.
		LeaderElection: false,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// healthz.Ping is controller-runtime's simplest built-in check — "is
	// this process alive and able to respond at all." These are what
	// config/manager/manager.yaml's readinessProbe/livenessProbe
	// (GET /readyz, GET /healthz) actually call; without registering them
	// here, those HTTP paths would 404 and the probes would always fail.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Construct the reconciler with the manager's own client (which reads
	// through the shared cache mentioned above, not a fresh API call per
	// Get/List) and a namespaced event recorder, then hand it back to the
	// manager via SetupWithManager — see reloadtrigger_controller.go for
	// what that wires up.
	reconciler := &controllers.ReloadTriggerReconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("config-reloader"),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up ReloadTrigger controller")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	// mgr.Start blocks until the process receives SIGTERM/SIGINT (handled
	// by ctrl.SetupSignalHandler), running the reconcile loop the whole
	// time. This is the only blocking call in main — everything above it
	// is one-time setup.
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
