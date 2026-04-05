// control-plane/internal/api/jobs_test.go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

type apiTestEnv struct {
	handler   http.Handler
	pool      *pgxpool.Pool
	tenantID  string
	projectID string
	token     string
}

func setupAPITest(t *testing.T) *apiTestEnv {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('api-test-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'api-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	plaintext := "testtoken-xxxx-yyyy"
	prefix := plaintext[:8]
	hash, _ := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix)

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	handler := api.NewRouter(pool, store, pub, nil)

	return &apiTestEnv{handler: handler, pool: pool, tenantID: tenantID, projectID: projectID, token: plaintext}
}

func TestSubmitJob_HappyPath(t *testing.T) {
	env := setupAPITest(t)

	body := map[string]interface{}{
		"name":       "test-job",
		"project_id": env.projectID,
		"runtime": map[string]interface{}{
			"image":   "img:1",
			"command": []string{"python", "train.py"},
			"args":    []string{},
			"env":     map[string]string{},
		},
		"resources": map[string]interface{}{
			"num_workers":   1,
			"worker_cpu":    "1",
			"worker_memory": "1Gi",
			"head_cpu":      "1",
			"head_memory":   "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["job_id"] == "" {
		t.Fatal("expected job_id in response")
	}
	if resp["run_id"] == "" {
		t.Fatal("expected run_id in response")
	}
}

func TestSubmitJob_InvalidCPU(t *testing.T) {
	env := setupAPITest(t)

	body := map[string]interface{}{
		"name": "bad-job", "project_id": env.projectID,
		"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "not-valid", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestSubmitJob_Unauthenticated(t *testing.T) {
	env := setupAPITest(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestSubmitJob_QuotaExceeded(t *testing.T) {
	env := setupAPITest(t)
	ctx := context.Background()

	// Set a tight CPU quota on the tenant: 1 core = 1000 millicores
	env.pool.Exec(ctx, `UPDATE tenants SET cpu_quota = 1000 WHERE id = $1`, env.tenantID)

	submitJob := func() int {
		body := map[string]interface{}{
			"name": "quota-job", "project_id": env.projectID,
			"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
			"resources": map[string]interface{}{
				"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
				"head_cpu": "0", "head_memory": "0",
			},
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+env.token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		return rec.Code
	}

	first := submitJob()
	if first != http.StatusAccepted {
		t.Fatalf("first job: expected 202, got %d", first)
	}
	second := submitJob()
	if second != http.StatusTooManyRequests {
		t.Fatalf("second job over quota: expected 429, got %d", second)
	}
}

func TestSubmitJob_CrossTenantProject(t *testing.T) {
	env := setupAPITest(t)
	ctx := context.Background()

	// Create a second tenant with its own project.
	var otherTenantID, otherProjectID string
	env.pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('other-tenant') RETURNING id::text`).Scan(&otherTenantID)
	env.pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'other-proj') RETURNING id::text`, otherTenantID).Scan(&otherProjectID)

	// Authenticated as env.tenantID but referencing a project owned by otherTenantID.
	body := map[string]interface{}{
		"name":       "cross-tenant-job",
		"project_id": otherProjectID,
		"runtime": map[string]interface{}{
			"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{},
		},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant project, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitJob_MissingProject(t *testing.T) {
	env := setupAPITest(t)

	body := map[string]interface{}{
		"name":       "missing-proj-job",
		"project_id": "00000000-0000-0000-0000-000000000000",
		"runtime": map[string]interface{}{
			"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{},
		},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing project, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListJobs(t *testing.T) {
	env := setupAPITest(t)

	// Submit one job first via the API
	body := map[string]interface{}{
		"name": "list-job", "project_id": env.projectID,
		"runtime": map[string]interface{}{"image": "img:1", "command": []string{"run"}, "args": []string{}, "env": map[string]string{}},
		"resources": map[string]interface{}{
			"num_workers": 1, "worker_cpu": "1", "worker_memory": "1Gi",
			"head_cpu": "1", "head_memory": "1Gi",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	env.handler.ServeHTTP(httptest.NewRecorder(), req)

	// List
	listReq := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	listReq.Header.Set("Authorization", "Bearer "+env.token)
	listRec := httptest.NewRecorder()
	env.handler.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(listRec.Body).Decode(&resp)
	jobsList, ok := resp["jobs"].([]interface{})
	if !ok || len(jobsList) == 0 {
		t.Fatal("expected non-empty jobs list")
	}
}
