// control-plane/internal/jobs/statemachine_test.go
package jobs_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
)

func TestValidateTransition_ValidPaths(t *testing.T) {
	cases := []struct{ from, to string }{
		{"PENDING", "QUEUED"},
		{"PENDING", "CANCELLED"},
		{"QUEUED", "RUNNING"},
		{"QUEUED", "CANCELLED"},
		{"RUNNING", "SUCCEEDED"},
		{"RUNNING", "FAILED"},
		{"RUNNING", "CANCELLED"},
	}
	for _, c := range cases {
		if err := jobs.ValidateTransition(c.from, c.to); err != nil {
			t.Errorf("%s → %s: unexpected error: %v", c.from, c.to, err)
		}
	}
}

func TestValidateTransition_InvalidPaths(t *testing.T) {
	cases := []struct{ from, to string }{
		{"PENDING", "RUNNING"},
		{"PENDING", "SUCCEEDED"},
		{"PENDING", "FAILED"},
		{"QUEUED", "SUCCEEDED"},
		{"QUEUED", "FAILED"},
		{"RUNNING", "PENDING"},
		{"RUNNING", "QUEUED"},
		{"SUCCEEDED", "RUNNING"},
		{"SUCCEEDED", "FAILED"},
		{"FAILED", "RUNNING"},
		{"FAILED", "SUCCEEDED"},
	}
	for _, c := range cases {
		if err := jobs.ValidateTransition(c.from, c.to); err == nil {
			t.Errorf("%s → %s: expected error, got nil", c.from, c.to)
		}
	}
}

func TestValidateTransition_UnknownStatus(t *testing.T) {
	if err := jobs.ValidateTransition("UNKNOWN", "RUNNING"); err == nil {
		t.Error("expected error for unknown from-status, got nil")
	}
}
