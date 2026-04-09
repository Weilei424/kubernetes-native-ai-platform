// control-plane/internal/mlflow/client_test.go
package mlflow_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/mlflow"
)

func TestCreateRegisteredModel_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/mlflow/registered-models/create" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"registered_model": map[string]string{"name": "tenant1-resnet50"},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	created, err := c.CreateRegisteredModel(context.Background(), "tenant1-resnet50")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true for new model")
	}
}

func TestCreateRegisteredModel_AlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_ALREADY_EXISTS",
			"message":    "Model already exists",
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	// RESOURCE_ALREADY_EXISTS must be silently ignored; created must be false.
	created, err := c.CreateRegisteredModel(context.Background(), "tenant1-resnet50")
	if err != nil {
		t.Fatalf("expected nil for RESOURCE_ALREADY_EXISTS, got: %v", err)
	}
	if created {
		t.Error("expected created=false when model already existed")
	}
}

func TestDeleteRegisteredModel_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/2.0/mlflow/registered-models/delete" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.DeleteRegisteredModel(context.Background(), "tenant1-resnet50"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateModelVersion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/mlflow/model-versions/create" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model_version": map[string]string{
				"name":             "tenant1-resnet50",
				"version":          "3",
				"source":           "runs:/abc123/model/",
				"storage_location": "mlflow-artifacts:/mlflow-bucket/abc123/artifacts/model/",
			},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	vNum, artifactURI, err := c.CreateModelVersion(context.Background(), "tenant1-resnet50", "runs:/abc123/model/", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vNum != 3 {
		t.Errorf("expected version 3, got %d", vNum)
	}
	// storage_location should be preferred over source
	if artifactURI != "mlflow-artifacts:/mlflow-bucket/abc123/artifacts/model/" {
		t.Errorf("unexpected artifact URI: %s", artifactURI)
	}
}

func TestSetModelAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"registered_model_alias": map[string]string{"alias": "production", "version": "3"},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.SetModelAlias(context.Background(), "tenant1-resnet50", "production", 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteModelAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.DeleteModelAlias(context.Background(), "tenant1-resnet50", "production"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetModelVersionByAlias_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/mlflow/registered-models/alias" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("name") != "tenant1-resnet50" || r.URL.Query().Get("alias") != "production" {
			t.Errorf("unexpected query params: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model_version": map[string]string{
				"name":    "tenant1-resnet50",
				"version": "3",
			},
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	vNum, err := c.GetModelVersionByAlias(context.Background(), "tenant1-resnet50", "production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vNum != 3 {
		t.Errorf("expected version 3, got %d", vNum)
	}
}

func TestGetModelVersionByAlias_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_DOES_NOT_EXIST",
			"message":    "alias not found",
		})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	_, err := c.GetModelVersionByAlias(context.Background(), "tenant1-resnet50", "production")
	if err == nil {
		t.Fatal("expected error for not found alias, got nil")
	}
	if !mlflow.IsNotFound(err) {
		t.Errorf("expected IsNotFound to be true, got false for err: %v", err)
	}
}

func TestDeleteModelVersion_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/2.0/mlflow/model-versions/delete" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	c := mlflow.New(srv.URL)
	if err := c.DeleteModelVersion(context.Background(), "tenant1-resnet50", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
