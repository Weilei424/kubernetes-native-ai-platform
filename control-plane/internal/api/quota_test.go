// control-plane/internal/api/quota_test.go
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/api"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func setupQuotaAPITest(t *testing.T) (http.Handler, string) {
	t.Helper()
	pool := testutil.SetupDB(t)
	ctx := context.Background()

	var tenantID string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('quota-tenant') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	plaintext := "quotatoken-xxxx"
	prefix := plaintext[:8]
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO api_tokens (tenant_id, token_hash, token_prefix) VALUES ($1, $2, $3)`,
		tenantID, string(hash), prefix); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	store := jobs.NewPostgresJobStore(pool)
	pub := &events.NoOpPublisher{}
	eventStore := events.NewEventStore(pool)
	handler := api.NewRouter(pool, store, pub, nil, nil, eventStore)
	return handler, plaintext
}

func TestQuotaHandler_GetQuota(t *testing.T) {
	handler, token := setupQuotaAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, field := range []string{"cpu_quota", "memory_quota", "cpu_used", "memory_used", "running_jobs"} {
		if !strings.Contains(body, field) {
			t.Errorf("response missing field %q; body: %s", field, body)
		}
	}

	var resp map[string]any
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// running_jobs should be 0 with no active runs
	runningJobs, ok := resp["running_jobs"].(float64)
	if !ok {
		t.Fatalf("running_jobs field missing or wrong type: %v", resp["running_jobs"])
	}
	if runningJobs != 0 {
		t.Errorf("expected running_jobs=0, got %v", runningJobs)
	}

	// cpu_quota and memory_quota should be present as strings
	if _, ok := resp["cpu_quota"].(string); !ok {
		t.Errorf("expected cpu_quota to be a string, got %T", resp["cpu_quota"])
	}
	if _, ok := resp["memory_quota"].(string); !ok {
		t.Errorf("expected memory_quota to be a string, got %T", resp["memory_quota"])
	}
}

func TestQuotaHandler_RequiresAuth(t *testing.T) {
	handler, _ := setupQuotaAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}
}
