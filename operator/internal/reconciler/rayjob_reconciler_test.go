// operator/internal/reconciler/rayjob_reconciler_test.go
package reconciler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMapStatus_Running(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Running")
	if !ok {
		t.Fatal("Running should map to a platform status")
	}
	if got != "RUNNING" {
		t.Fatalf("expected RUNNING, got %q", got)
	}
}

func TestMapStatus_Complete(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Complete")
	if !ok {
		t.Fatal("Complete should map to a platform status")
	}
	if got != "SUCCEEDED" {
		t.Fatalf("expected SUCCEEDED, got %q", got)
	}
}

func TestMapStatus_Failed(t *testing.T) {
	got, ok := reconciler.MapRayJobStatus("Failed")
	if !ok {
		t.Fatal("Failed should map to a platform status")
	}
	if got != "FAILED" {
		t.Fatalf("expected FAILED, got %q", got)
	}
}

func TestMapStatus_Suspended(t *testing.T) {
	_, ok := reconciler.MapRayJobStatus("Suspended")
	if ok {
		t.Fatal("Suspended should not map (ignored in Phase 1)")
	}
}

func TestMapStatus_Unknown(t *testing.T) {
	_, ok := reconciler.MapRayJobStatus("SomeFutureStatus")
	if ok {
		t.Fatal("unknown status should not map")
	}
}

// TestRayJobReconciler_BadImage_PropagatesFailureReason verifies the bad-image failure
// path end-to-end: when a RayJob's jobDeploymentStatus is "Failed" and the status
// message contains an image-pull error, the reconciler reports status=FAILED to the
// control plane and includes the failure reason in the request body.
func TestRayJobReconciler_BadImage_PropagatesFailureReason(t *testing.T) {
	const jobID = "bad-img-job-001"
	var captured map[string]interface{}

	fakeCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			json.NewDecoder(r.Body).Decode(&captured) //nolint:errcheck
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "FAILED"}) //nolint:errcheck
	}))
	defer fakeCP.Close()

	// Build a fake RayJob with the ImagePullBackOff failure message that KubeRay
	// would set when a worker node cannot pull the training image.
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayJob"})
	obj.SetName("rayjob-bad-img")
	obj.SetNamespace("default")
	obj.SetLabels(map[string]string{"platform.io/job-id": jobID})
	_ = unstructured.SetNestedField(obj.Object, "Failed", "status", "jobDeploymentStatus")
	_ = unstructured.SetNestedField(obj.Object, `ImagePullBackOff: Back-off pulling image "bad:latest"`, "status", "message")

	fakeClient := fake.NewFakeClient()
	if err := fakeClient.Create(context.Background(), obj); err != nil {
		t.Fatalf("pre-create RayJob in fake client: %v", err)
	}

	rjr := &reconciler.RayJobReconciler{
		Client:          fakeClient,
		ControlPlaneURL: fakeCP.URL,
		HTTPClient:      fakeCP.Client(),
	}

	_, err := rjr.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rayjob-bad-img", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if captured == nil {
		t.Fatal("expected PATCH to control plane, none received")
	}
	if status, _ := captured["status"].(string); status != "FAILED" {
		t.Errorf("expected status=FAILED in PATCH body, got %q", status)
	}
	reason, _ := captured["failure_reason"].(string)
	if !strings.Contains(reason, "ImagePullBackOff") {
		t.Errorf("expected failure_reason to mention 'ImagePullBackOff', got %q", reason)
	}
}

func TestExtractMLflowRunIDFromLogs(t *testing.T) {
	logs := "epoch 1 loss=0.5\nMLFLOW_RUN_ID=abc123\nepoch 2 loss=0.4\n"
	got := reconciler.ParseMLflowRunID(logs)
	if got != "abc123" {
		t.Fatalf("expected abc123, got %q", got)
	}
}

func TestExtractMLflowRunIDFromLogs_NotFound(t *testing.T) {
	got := reconciler.ParseMLflowRunID("no run id here\n")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
