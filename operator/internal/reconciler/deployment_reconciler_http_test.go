// operator/internal/reconciler/deployment_reconciler_http_test.go
// Internal test file (package reconciler) so private methods are accessible.
package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestReconciler(serverURL string) *DeploymentReconciler {
	return &DeploymentReconciler{
		ControlPlaneURL: serverURL,
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
	}
}

func TestListPendingDeployments_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/deployments" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"deployments": []map[string]interface{}{
				{
					"id": "dep-1", "name": "resnet50-prod", "namespace": "default",
					"status": "pending", "artifact_uri": "s3://bucket/model", "model_name": "resnet50",
				},
			},
		})
	}))
	defer srv.Close()

	r := newTestReconciler(srv.URL)
	deps, err := r.listPendingDeployments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(deps))
	}
	if deps[0].ID != "dep-1" {
		t.Errorf("expected ID dep-1, got %q", deps[0].ID)
	}
	if deps[0].ArtifactURI != "s3://bucket/model" {
		t.Errorf("expected ArtifactURI s3://bucket/model, got %q", deps[0].ArtifactURI)
	}
}

func TestListPendingDeployments_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	r := newTestReconciler(srv.URL)
	_, err := r.listPendingDeployments(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestReportStatus_Success(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/deployments/dep-1/status" || r.Method != http.MethodPatch {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "running"})
	}))
	defer srv.Close()

	r := newTestReconciler(srv.URL)
	endpoint := "triton-dep-1.default.svc.cluster.local:8000"
	if err := r.reportStatus(context.Background(), "dep-1", "running", endpoint, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("could not parse request body: %v", err)
	}
	if payload["status"] != "running" {
		t.Errorf("expected status running in body, got %q", payload["status"])
	}
	if payload["serving_endpoint"] != endpoint {
		t.Errorf("expected serving_endpoint %q, got %q", endpoint, payload["serving_endpoint"])
	}
}

func TestReportStatus_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"bad transition"}`))
	}))
	defer srv.Close()

	r := newTestReconciler(srv.URL)
	err := r.reportStatus(context.Background(), "dep-1", "running", "", "")
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
}
