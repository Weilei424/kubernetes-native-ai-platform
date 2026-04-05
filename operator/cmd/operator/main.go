// operator/cmd/operator/main.go
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
)

func main() {
	scheme := runtime.NewScheme()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		slog.Error("unable to start manager", "error", err)
		os.Exit(1)
	}

	controlPlaneURL := os.Getenv("CONTROL_PLANE_INTERNAL_URL")
	if controlPlaneURL == "" {
		controlPlaneURL = "http://control-plane:8081"
	}

	r := &reconciler.RayJobReconciler{
		Client:          mgr.GetClient(),
		ControlPlaneURL: controlPlaneURL,
		HTTPClient:      &http.Client{Timeout: 10 * time.Second},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		slog.Error("unable to set up reconciler", "error", err)
		os.Exit(1)
	}

	slog.Info("operator starting", "control_plane_url", controlPlaneURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
