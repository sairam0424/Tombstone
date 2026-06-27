// Command tombstone-operator runs the Tombstone Kubernetes operator.
// It registers CRDs for FeatureFlag, FlagEnvironment, and FlagPolicy, then
// starts a controller-runtime Manager with the reconcile loops that mirror
// Kubernetes resources to the Tombstone flag-api.
package main

import (
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (GCP, Azure, OIDC, etc.) so
	// that they are registered and available via kubeconfig.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"go.uber.org/zap"

	v1alpha1 "github.com/sairam0424/Tombstone/services/tombstone-operator/api/v1alpha1"
	"github.com/sairam0424/Tombstone/services/tombstone-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	// CLI flags — keep defaults sane for both in-cluster and local dev.
	var (
		metricsAddr        string
		probeAddr          string
		leaderElect        bool
		syncPeriod         time.Duration
		enableDebugLogging bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8088", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8089", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.DurationVar(&syncPeriod, "sync-period", 10*time.Minute,
		"Minimum frequency at which watched resources are reconciled.")
	flag.BoolVar(&enableDebugLogging, "debug", false, "Enable debug-level logging.")
	flag.Parse()

	// Build a zap logger and wire it into controller-runtime.
	zapOpts := ctrlzap.Options{
		Development: enableDebugLogging,
	}
	// Set controller-runtime global logger (accepts logr.Logger via ctrlzap.New).
	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseFlagOptions(&zapOpts)))

	// Obtain a raw *zap.Logger for passing into reconcilers.
	logger := ctrlzap.NewRaw(ctrlzap.UseFlagOptions(&zapOpts)).Named("tombstone-operator")

	// Read required environment variables.
	apiBase := requireEnv("TOMBSTONE_API_URL", logger)
	token := requireEnv("TOMBSTONE_API_TOKEN", logger)

	logger.Info("starting tombstone-operator",
		zap.String("metricsAddr", metricsAddr),
		zap.String("probeAddr", probeAddr),
		zap.Bool("leaderElect", leaderElect),
		zap.Duration("syncPeriod", syncPeriod),
		zap.String("apiBase", apiBase),
	)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "tombstone-operator-leader",
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
	})
	if err != nil {
		logger.Fatal("unable to start manager", zap.Error(err))
	}

	// Register the FeatureFlag reconciler.
	ffReconciler := controller.NewFeatureFlagReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		logger.Named("featureflag"),
		apiBase,
		token,
	)
	if err := ffReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal("unable to create FeatureFlag controller", zap.Error(err))
	}

	// Register the FlagPolicy reconciler.
	fpReconciler := &controller.FlagPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: logger.Named("flagpolicy"),
	}
	if err := fpReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal("unable to create FlagPolicy controller", zap.Error(err))
	}

	// Health probes.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal("unable to set up health check", zap.Error(err))
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Fatal("unable to set up ready check", zap.Error(err))
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Fatal("problem running manager", zap.Error(err))
	}
}

// requireEnv reads an environment variable, logging a fatal error and
// exiting if it is unset or empty.
func requireEnv(name string, logger *zap.Logger) string {
	v := os.Getenv(name)
	if v == "" {
		logger.Fatal("required environment variable is not set", zap.String("var", name))
	}
	return v
}
