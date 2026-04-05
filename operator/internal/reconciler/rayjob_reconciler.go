// operator/internal/reconciler/rayjob_reconciler.go
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RayJobReconciler watches RayJob objects and reports status back to the
// control plane via the internal status API.
type RayJobReconciler struct {
	client.Client
	ControlPlaneURL string // base URL of the control plane internal port, e.g. "http://control-plane:8081"
	HTTPClient      *http.Client
}

// Reconcile is called by controller-runtime whenever a RayJob changes.
func (r *RayJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(rayJobGVK())
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only process platform-managed RayJobs
	labels := obj.GetLabels()
	jobID, ok := labels["platform.io/job-id"]
	if !ok {
		return ctrl.Result{}, nil
	}

	rayStatus, _, _ := unstructured.NestedString(obj.Object, "status", "jobDeploymentStatus")
	platformStatus, mapped := MapRayJobStatus(rayStatus)
	if !mapped {
		return ctrl.Result{}, nil
	}

	var failureReason *string
	if platformStatus == "FAILED" {
		msg, found, _ := unstructured.NestedString(obj.Object, "status", "message")
		if found && msg != "" {
			failureReason = &msg
		}
	}

	if err := r.reportStatus(ctx, jobID, platformStatus, failureReason); err != nil {
		slog.Error("reconciler: report status", "job_id", jobID, "status", platformStatus, "error", err)
		return ctrl.Result{}, err
	}

	slog.Info("reconciler: status reported", "job_id", jobID, "status", platformStatus)
	return ctrl.Result{}, nil
}

// MapRayJobStatus converts a KubeRay JobDeploymentStatus string to a platform
// status string. Returns (status, true) on a known mapping, ("", false) if the
// status should be ignored.
func MapRayJobStatus(rayStatus string) (string, bool) {
	switch rayStatus {
	case "Running":
		return "RUNNING", true
	case "Complete":
		return "SUCCEEDED", true
	case "Failed":
		return "FAILED", true
	default:
		return "", false
	}
}

func (r *RayJobReconciler) reportStatus(ctx context.Context, jobID, status string, failureReason *string) error {
	payload := map[string]interface{}{
		"status": status,
	}
	if failureReason != nil {
		payload["failure_reason"] = *failureReason
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/internal/v1/jobs/%s/status", r.ControlPlaneURL, jobID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// Already in terminal state — idempotent, not an error
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}
	return nil
}

func rayJobGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "ray.io",
		Version: "v1",
		Kind:    "RayJob",
	}
}

// SetupWithManager registers the reconciler with the controller-runtime manager.
func (r *RayJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(rayJobGVK())
	return ctrl.NewControllerManagedBy(mgr).
		For(obj).
		Complete(r)
}
