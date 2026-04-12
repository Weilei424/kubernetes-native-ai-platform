// operator/internal/reconciler/deployment_reconciler_test.go
package reconciler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMapPodPhase_Running(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodRunning)
	if got != "running" {
		t.Fatalf("expected running, got %q", got)
	}
}

func TestMapPodPhase_Failed(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodFailed)
	if got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
}

func TestMapPodPhase_Pending(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodPending)
	if got != "provisioning" {
		t.Fatalf("expected provisioning, got %q", got)
	}
}

func TestMapPodPhase_Unknown(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodUnknown)
	if got != "provisioning" {
		t.Fatalf("expected provisioning for unknown, got %q", got)
	}
}

func TestTritonPodName(t *testing.T) {
	name := reconciler.TritonPodName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestTritonServiceName(t *testing.T) {
	name := reconciler.TritonServiceName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestServingEndpoint(t *testing.T) {
	ep := reconciler.ServingEndpoint("abc-123", "default")
	if ep != "triton-abc-123.default.svc.cluster.local:8000" {
		t.Fatalf("unexpected endpoint: %q", ep)
	}
}

func TestDefaultInterval(t *testing.T) {
	if reconciler.DefaultPollInterval != 10*time.Second {
		t.Fatalf("expected 10s, got %v", reconciler.DefaultPollInterval)
	}
}

// TestMapPodPhase_InitError verifies that an init-container error phase string
// (not a standard corev1.PodPhase constant) is mapped to "provisioning" via the
// default branch, since Kubernetes surfaces init errors as a reason string rather
// than a distinct phase value.
func TestMapPodPhase_InitError(t *testing.T) {
	result := reconciler.MapPodPhase(corev1.PodPhase("Init:Error"))
	if result != "provisioning" {
		t.Errorf("expected 'provisioning', got %q", result)
	}
}

// TestBuildPod_HasTritonAppLabel verifies that pods created by the reconciler carry
// the "app=triton" label required by the Prometheus Kubernetes SD scrape config.
func TestBuildPod_HasTritonAppLabel(t *testing.T) {
	podCreated := false
	podLabels := map[string]string{}

	fakeCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"deployments": []map[string]any{{
					"id": "pod-label-test", "name": "test", "namespace": "default",
					"status": "pending", "desired_replicas": 1,
					"artifact_uri": "s3://bucket/model", "model_name": "resnet",
				}},
			})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer fakeCP.Close()

	fakeClient := fake.NewFakeClient()
	dr := &reconciler.DeploymentReconciler{
		Client:          fakeClient,
		ControlPlaneURL: fakeCP.URL,
		HTTPClient:      fakeCP.Client(),
		PollInterval:    20 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = dr.Start(ctx)

	// List pods created in the fake client.
	podList := &corev1.PodList{}
	if err := fakeClient.List(context.Background(), podList); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	for _, p := range podList.Items {
		if p.Name == "triton-pod-label-test" {
			podCreated = true
			podLabels = p.Labels
		}
	}

	if !podCreated {
		t.Fatal("expected Triton pod to be created but none found")
	}
	if podLabels["app"] != "triton" {
		t.Errorf("expected pod label app=triton, got labels: %v", podLabels)
	}
}

// TestDeploymentReconciler_TritonReadinessFail_StaysProvisioning verifies that
// the reconciler does NOT report status="running" when no running pod exists in
// the cluster (simulating Triton failing readiness / 503 / not yet running).
func TestDeploymentReconciler_TritonReadinessFail_StaysProvisioning(t *testing.T) {
	sentRunning := false

	fakeCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Return one provisioning deployment for the reconciler to act on.
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"deployments": []map[string]any{{
					"id": "dep-1", "name": "test", "namespace": "default",
					"status": "provisioning", "desired_replicas": 1,
					"artifact_uri": "s3://bucket/model", "model_name": "resnet",
				}},
			})
		} else if r.Method == http.MethodPatch {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			if body["status"] == "running" {
				sentRunning = true
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer fakeCP.Close()

	dr := &reconciler.DeploymentReconciler{
		Client:          fake.NewFakeClient(),
		ControlPlaneURL: fakeCP.URL,
		HTTPClient:      fakeCP.Client(),
		PollInterval:    20 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = dr.Start(ctx)

	if sentRunning {
		t.Error("reconciler advanced to 'running' despite no running pod in the cluster")
	}
}

// TestDeploymentReconciler_MissingArtifact_ReportsFailed verifies the missing-artifact
// failure path: when the Triton pod enters PodFailed phase with an init container
// that exited non-zero (simulating the model-loader failing to fetch the artifact),
// the reconciler reports status="failed" with an actionable failure_reason containing
// the init container's error message.
func TestDeploymentReconciler_MissingArtifact_ReportsFailed(t *testing.T) {
	var reportedStatus, reportedReason string

	fakeCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"deployments": []map[string]any{{
					"id": "dep-missing-art", "name": "test", "namespace": "default",
					"status": "pending", "desired_replicas": 1,
					"artifact_uri": "s3://bucket/missing-model", "model_name": "resnet",
				}},
			})
		} else if r.Method == http.MethodPatch {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			reportedStatus = body["status"]
			reportedReason = body["failure_reason"]
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer fakeCP.Close()

	// Pre-create the Triton pod in PodFailed phase with an init container that exited
	// non-zero carrying an actionable error message (artifact not found).
	failedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reconciler.TritonPodName("dep-missing-art"),
			Namespace: "default",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "model-loader",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
							Message:  "artifact not found: s3://bucket/missing-model",
						},
					},
				},
			},
		},
	}

	dr := &reconciler.DeploymentReconciler{
		Client:          fake.NewFakeClient(failedPod),
		ControlPlaneURL: fakeCP.URL,
		HTTPClient:      fakeCP.Client(),
		PollInterval:    20 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = dr.Start(ctx)

	if reportedStatus != "failed" {
		t.Errorf("expected reported status 'failed' for PodFailed phase, got %q", reportedStatus)
	}
	if !strings.Contains(reportedReason, "artifact not found") {
		t.Errorf("expected failure_reason to contain 'artifact not found', got %q", reportedReason)
	}
}

