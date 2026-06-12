// The MyceDrive operator embeds the Migration Coordinator: it reconciles
// MigratableWorkload and Migration custom resources while serving the REST
// API that the Execution Agents (go-agent) talk to.
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
	"github.com/paulosouzajr/mycedrive-k8s/operator/internal/controller"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/history"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/restapi"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mycedrivev1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		restAddr             string
		defaultNamespace     string
		enableLeaderElection bool
		historyEnabled       bool
		historyLimit         int
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to ('0' disables it).")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.StringVar(&restAddr, "rest-bind-address", ":8080", "The address the Migration Coordinator REST API binds to.")
	flag.StringVar(&defaultNamespace, "default-namespace", "mig-ready", "Namespace used for Migrations created through the legacy REST API.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.BoolVar(&historyEnabled, "history-enabled", true, "Enable the migration history & metrics module (also toggleable at runtime via the REST API).")
	flag.IntVar(&historyLimit, "history-limit", history.DefaultLimit, "Maximum number of migrations kept in the in-memory history.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "operator.mycedrive.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	reg := registry.New()
	hist := history.NewStore(historyEnabled, historyLimit)
	// Restore agent registrations mirrored into MigratableWorkload statuses
	// and coarse migration history from Migration CRs so both survive
	// operator restarts. Best effort: a missing CRD (first install) is not
	// fatal.
	{
		seedCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		directClient, cerr := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
		if cerr != nil {
			setupLog.Error(cerr, "unable to create seed client; starting with an empty registry")
		} else {
			if serr := controller.SeedRegistry(seedCtx, directClient, reg); serr != nil {
				setupLog.Info("registry seeding skipped", "reason", serr.Error())
			}
			if historyEnabled {
				if serr := controller.SeedHistory(seedCtx, directClient, hist); serr != nil {
					setupLog.Info("history seeding skipped", "reason", serr.Error())
				}
			}
		}
		cancel()
	}

	if err := (&controller.MigrationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: reg,
		History:  hist,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Migration")
		os.Exit(1)
	}
	if err := (&controller.MigratableWorkloadReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: reg,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MigratableWorkload")
		os.Exit(1)
	}

	if err := mgr.Add(&restapi.Server{
		Client:           mgr.GetClient(),
		Registry:         reg,
		Addr:             restAddr,
		DefaultNamespace: defaultNamespace,
		Log:              ctrl.Log.WithName("restapi"),
		History:          hist,
	}); err != nil {
		setupLog.Error(err, "unable to add REST API server")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
