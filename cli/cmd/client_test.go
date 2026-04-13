// cli/cmd/client_test.go
package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildURL_NormalPath(t *testing.T) {
	got := buildURL("http://localhost:8080", "/v1/jobs")
	if got != "http://localhost:8080/v1/jobs" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildURL_TrailingSlash(t *testing.T) {
	got := buildURL("http://localhost:8080/", "/v1/jobs")
	if got != "http://localhost:8080/v1/jobs" {
		t.Fatalf("got %q", got)
	}
}

func TestDoRequest_SetsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, err := doRequest("GET", srv.URL+"/v1/jobs", "mytoken", nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer mytoken" {
		t.Fatalf("expected 'Bearer mytoken', got %q", gotAuth)
	}
}

func TestDoRequest_Returns4xxAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	_, err := doRequest("GET", srv.URL+"/v1/jobs/missing", "tok", nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 in error, got: %v", err)
	}
}

func TestPrintTable_Output(t *testing.T) {
	// printTable writes to stdout; just verify it doesn't panic
	printTable([][2]string{{"Status", "RUNNING"}, {"Job ID", "abc"}})
}
