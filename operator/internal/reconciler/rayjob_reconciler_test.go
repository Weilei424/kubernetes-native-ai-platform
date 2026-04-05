// operator/internal/reconciler/rayjob_reconciler_test.go
package reconciler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
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
