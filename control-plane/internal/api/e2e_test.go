// control-plane/internal/api/e2e_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/deployments"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/models"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

// TestE2E_FullLifecycle drives the full metadata chain:
//
//	submit job → simulate SUCCEEDED (via internal API) → register model →
//	promote to staging + production → create deployment → simulate RUNNING →
//	verify serving endpoint
//
// Uses a real PostgreSQL container (testcontainers) + mock MLflow server.
// No Kubernetes, Ray, or Triton required.
func TestE2E_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testutil.SetupDB(t)

	// ── Seed tenant + project + token ──────────────────────────────────────
	var tenantID, projectID string
	pool.QueryRow(ctx,
		`INSERT INTO tenants (name, cpu_quota, memory_quota)
		 VALUES ('e2e-tenant', 32000, 68719476736) RETURNING id::text`,
	).Scan(&tenantID)
	pool.QueryRow(ctx,
		`INSERT INTO projects (tenant_id, name) VALUES ($1, 'e2e-proj') RETURNING id::text`,
		tenantID,
	).Scan(&projectID)

	plaintext := "e2e-token-1234abcd"
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx,
		`INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), plaintext[:8],
	)

	// ── Mock MLflow server ─────────────────────────────────────────────────
	versionCounter := 0
	mlflowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "registered-models/create") && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"registered_model": map[string]string{"name": "resnet50"},
			})
		case strings.Contains(path, "model-versions/create") && r.Method == http.MethodPost:
			versionCounter++
			json.NewEncoder(w).Encode(map[string]interface{}{
				"model_version": map[string]interface{}{
					"version": fmt.Sprintf("%d", versionCounter),
					"source":  "s3://models/test/model.onnx",
				},
			})
		case strings.Contains(path, "registered-models/alias") && r.Method == http.MethodPatch:
			// SetModelAlias
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		case strings.Contains(path, "registered-models/alias") && r.Method == http.MethodGet:
			// GetModelVersionByAlias
			json.NewEncoder(w).Encode(map[string]interface{}{
				"model_version": map[string]interface{}{
					"version": fmt.Sprintf("%d", versionCounter),
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	defer mlflowSrv.Close()

	// ── Wire services ──────────────────────────────────────────────────────
	jobStore := jobs.NewPostgresJobStore(pool)
	publisher := &events.NoOpPublisher{}
	mlflowClient := mlflow.New(mlflowSrv.URL)
	modelStore := models.NewPostgresModelStore(pool)
	modelsSvc := models.NewService(modelStore, jobStore, mlflowClient)
	deployStore := deployments.NewPostgresDeploymentStore(pool)
	deploymentsSvc := deployments.NewService(deployStore, modelStore)
	eventStore := events.NewEventStore(pool)

	pubHandler := api.NewRouter(pool, jobStore, publisher, modelsSvc, deploymentsSvc, eventStore)
	intHandler := api.NewInternalRouter(jobStore, publisher, deployStore, eventStore)

	pubSrv := httptest.NewServer(pubHandler)
	defer pubSrv.Close()
	intSrv := httptest.NewServer(intHandler)
	defer intSrv.Close()

	// ── helpers ────────────────────────────────────────────────────────────
	doPublic := func(t *testing.T, method, path string, body interface{}) *http.Response {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, pubSrv.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("public %s %s: %v", method, path, err)
		}
		return resp
	}
	doInternal := func(t *testing.T, method, path string, body interface{}) *http.Response {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, intSrv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("internal %s %s: %v", method, path, err)
		}
		return resp
	}
	decode := func(t *testing.T, resp *http.Response, v interface{}) {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			var e map[string]string
			json.NewDecoder(resp.Body).Decode(&e)
			t.Fatalf("unexpected HTTP %d: %v", resp.StatusCode, e)
		}
		json.NewDecoder(resp.Body).Decode(v)
	}

	// ── 1. Submit job ──────────────────────────────────────────────────────
	t.Log("1. submit job")
	var submitOut struct {
		JobID string `json:"job_id"`
		RunID string `json:"run_id"`
	}
	decode(t, doPublic(t, "POST", "/v1/jobs", map[string]interface{}{
		"name": "e2e-job", "project_id": projectID,
		"runtime": map[string]interface{}{
			"image": "platform/minimal-trainer:latest", "command": []string{"python", "train.py"},
		},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "1", "worker_memory": "2Gi",
			"head_cpu": "1", "head_memory": "2Gi",
		},
	}), &submitOut)
	if submitOut.JobID == "" || submitOut.RunID == "" {
		t.Fatal("expected job_id and run_id")
	}
	jobID, runID := submitOut.JobID, submitOut.RunID
	t.Logf("   job=%s run=%s", jobID, runID)

	// ── 2. Drive job: PENDING → QUEUED → RUNNING → SUCCEEDED ──────────────
	t.Log("2. drive job to SUCCEEDED via internal API")
	for _, s := range []string{"QUEUED", "RUNNING"} {
		resp := doInternal(t, "PATCH", "/internal/v1/jobs/"+jobID+"/status", map[string]string{"status": s})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("transition to %s: HTTP %d", s, resp.StatusCode)
		}
	}
	// SUCCEEDED with mlflow_run_id
	mlflowRunID := "mlflow-e2e-run-abc"
	resp := doInternal(t, "PATCH", "/internal/v1/jobs/"+jobID+"/status", map[string]interface{}{
		"status": "SUCCEEDED", "mlflow_run_id": mlflowRunID,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transition to SUCCEEDED: HTTP %d", resp.StatusCode)
	}

	// Verify mlflow_run_id is stored.
	// TrainingJob and TrainingRun have no json struct tags, so they serialize
	// with Go's default PascalCase field names.
	var statusOut struct {
		Job struct {
			Status string `json:"Status"`
		} `json:"job"`
		Run struct {
			MLflowRunID *string `json:"MLflowRunID"`
		} `json:"run"`
	}
	decode(t, doPublic(t, "GET", "/v1/jobs/"+jobID, nil), &statusOut)
	if statusOut.Job.Status != "SUCCEEDED" {
		t.Fatalf("expected SUCCEEDED, got %s", statusOut.Job.Status)
	}
	if statusOut.Run.MLflowRunID == nil || *statusOut.Run.MLflowRunID == "" {
		t.Fatal("mlflow_run_id not set on run")
	}
	t.Logf("   status=%s mlflow_run_id=%s", statusOut.Job.Status, *statusOut.Run.MLflowRunID)

	// ── 3. Register model ──────────────────────────────────────────────────
	t.Log("3. register model")
	var regOut struct {
		Version struct {
			VersionNumber int    `json:"version_number"`
			Status        string `json:"status"`
		} `json:"version"`
	}
	decode(t, doPublic(t, "POST", "/v1/models", map[string]string{
		"run_id": runID, "model_name": "resnet50",
	}), &regOut)
	if regOut.Version.VersionNumber == 0 {
		t.Fatal("expected version_number > 0")
	}
	if regOut.Version.Status != "candidate" {
		t.Fatalf("expected candidate, got %s", regOut.Version.Status)
	}
	ver := regOut.Version.VersionNumber
	t.Logf("   version=%d status=%s", ver, regOut.Version.Status)

	// ── 4. Promote: staging → production ──────────────────────────────────
	t.Log("4. promote to staging then production")
	for _, alias := range []string{"staging", "production"} {
		r := doPublic(t, "POST",
			fmt.Sprintf("/v1/models/resnet50/versions/%d/promote", ver),
			map[string]string{"alias": alias},
		)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("promote to %s: HTTP %d", alias, r.StatusCode)
		}
	}

	// Verify production alias resolves
	var aliasOut struct {
		Version struct {
			Status string `json:"status"`
		} `json:"version"`
	}
	decode(t, doPublic(t, "GET", "/v1/models/resnet50/alias/production", nil), &aliasOut)
	if aliasOut.Version.Status != "production" {
		t.Fatalf("expected production, got %s", aliasOut.Version.Status)
	}
	t.Log("   production alias verified")

	// ── 5. Create deployment ───────────────────────────────────────────────
	t.Log("5. create deployment")
	var depOut struct {
		Deployment struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"deployment"`
	}
	decode(t, doPublic(t, "POST", "/v1/deployments", map[string]interface{}{
		"name": "resnet50-prod", "model_name": "resnet50",
		"model_version": ver, "namespace": "aiplatform", "replicas": 1,
	}), &depOut)
	if depOut.Deployment.ID == "" {
		t.Fatal("expected deployment id")
	}
	if depOut.Deployment.Status != "pending" {
		t.Fatalf("expected pending, got %s", depOut.Deployment.Status)
	}
	depID := depOut.Deployment.ID
	t.Logf("   deployment=%s status=%s", depID, depOut.Deployment.Status)

	// ── 6. Drive deployment to running ────────────────────────────────────
	t.Log("6. drive deployment to running via internal API")
	endpoint := "resnet50-" + depID + ".aiplatform.svc.cluster.local:8000"
	r2 := doInternal(t, "PATCH", "/internal/v1/deployments/"+depID+"/status", map[string]string{
		"status": "provisioning",
	})
	r2.Body.Close()
	r3 := doInternal(t, "PATCH", "/internal/v1/deployments/"+depID+"/status", map[string]interface{}{
		"status": "running", "serving_endpoint": endpoint,
	})
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("running transition: HTTP %d", r3.StatusCode)
	}

	// Verify
	var depStatusOut struct {
		Deployment struct {
			Status          string `json:"status"`
			ServingEndpoint string `json:"serving_endpoint"`
		} `json:"deployment"`
	}
	decode(t, doPublic(t, "GET", "/v1/deployments/"+depID, nil), &depStatusOut)
	if depStatusOut.Deployment.Status != "running" {
		t.Fatalf("expected running, got %s", depStatusOut.Deployment.Status)
	}
	if depStatusOut.Deployment.ServingEndpoint == "" {
		t.Fatal("expected serving_endpoint to be set")
	}
	t.Logf("   status=%s endpoint=%s", depStatusOut.Deployment.Status, depStatusOut.Deployment.ServingEndpoint)

	t.Log("E2E PASSED: train → register → promote → deploy lifecycle complete")
	_ = ctx
	_ = runID
}
