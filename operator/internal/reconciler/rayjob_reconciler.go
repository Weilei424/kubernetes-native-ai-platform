// operator/internal/reconciler/rayjob_reconciler.go
package reconciler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/observability"
)

// RayJobReconciler watches RayJob objects and reports status back to the
// control plane via the internal status API.
type RayJobReconciler struct {
	client.Client
	KubeClient      *kubernetes.Clientset // used to read Ray head pod logs
	ControlPlaneURL string                // base URL of the control plane internal port, e.g. "http://control-plane:8081"
	HTTPClient      *http.Client
}

// Reconcile is called by controller-runtime whenever a RayJob changes.
func (r *RayJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	result, err := r.reconcile(ctx, req)
	observability.ReconcileDuration.WithLabelValues("rayjob").Observe(time.Since(start).Seconds())
	if err != nil {
		observability.ReconcileErrors.WithLabelValues("rayjob").Inc()
	}
	return result, err
}

func (r *RayJobReconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	var mlflowRunID string
	if platformStatus == "SUCCEEDED" {
		mlflowRunID = r.extractMLflowRunID(ctx, obj)
	}

	if err := r.reportStatus(ctx, jobID, platformStatus, failureReason, mlflowRunID); err != nil {
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

func (r *RayJobReconciler) reportStatus(ctx context.Context, jobID, status string, failureReason *string, mlflowRunID string) error {
	payload := map[string]interface{}{
		"status": status,
	}
	if failureReason != nil {
		payload["failure_reason"] = *failureReason
	}
	if mlflowRunID != "" {
		payload["mlflow_run_id"] = mlflowRunID
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

// ParseMLflowRunID scans log text for a line matching "MLFLOW_RUN_ID=<value>"
// and returns the value. Returns "" if not found.
// Exported so that the external test package can call it directly.
func ParseMLflowRunID(logs string) string {
	scanner := bufio.NewScanner(strings.NewReader(logs))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "MLFLOW_RUN_ID=") {
			return strings.TrimPrefix(line, "MLFLOW_RUN_ID=")
		}
	}
	return ""
}

// extractMLflowRunID reads the Ray head pod logs for the given RayJob and
// returns the mlflow_run_id printed by the training script.
// Returns "" if the log cannot be read or the marker line is absent.
func (r *RayJobReconciler) extractMLflowRunID(ctx context.Context, obj *unstructured.Unstructured) string {
	if r.KubeClient == nil {
		return ""
	}
	clusterName, found, _ := unstructured.NestedString(obj.Object, "status", "rayClusterName")
	if !found || clusterName == "" {
		return ""
	}
	namespace := obj.GetNamespace()

	pods, err := r.KubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "ray.io/cluster=" + clusterName + ",ray.io/node-type=head",
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}

	req := r.KubeClient.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	return ParseMLflowRunID(sb.String())
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
