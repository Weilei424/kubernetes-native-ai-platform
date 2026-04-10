// operator/cmd/operator/main.go
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	opcfg "github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/config"
	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func main() {
	configPath := flag.String("config", "", "Path to operator config YAML file")
	flag.Parse()

	cfg, err := opcfg.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}
	slog.Info("operator config loaded", "env", cfg.Env, "namespace", cfg.Namespace,
		"leader_election", cfg.LeaderElection)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		slog.Error("unable to add client-go scheme", "error", err)
		os.Exit(1)
	}

	mgrOpts := ctrl.Options{
		Scheme: scheme,
	}
	if cfg.LeaderElection {
		mgrOpts.LeaderElection = true
		mgrOpts.LeaderElectionID = "ai-platform-operator-leader"
		mgrOpts.LeaderElectionNamespace = cfg.Namespace
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		slog.Error("unable to start manager", "error", err)
		os.Exit(1)
	}

	controlPlaneURL := os.Getenv("CONTROL_PLANE_INTERNAL_URL")
	if controlPlaneURL == "" {
		controlPlaneURL = "http://control-plane:8081"
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	// RayJob reconciler (event-driven via controller-runtime watch).
	rjr := &reconciler.RayJobReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      httpClient,
	}
	if err := rjr.SetupWithManager(mgr); err != nil {
		slog.Error("unable to set up rayjob reconciler", "error", err)
		os.Exit(1)
	}

	// Deployment reconciler (poll-based goroutine via manager.Runnable).
	dr := &reconciler.DeploymentReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      httpClient,
		MinioEndpoint:   os.Getenv("MINIO_ENDPOINT"),
		PollInterval:    cfg.PollInterval,
	}
	if err := mgr.Add(dr); err != nil {
		slog.Error("unable to add deployment reconciler", "error", err)
		os.Exit(1)
	}

	slog.Info("operator starting", "control_plane_url", controlPlaneURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
