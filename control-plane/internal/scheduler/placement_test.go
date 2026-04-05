// control-plane/internal/scheduler/placement_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func TestHints_DefaultCPU(t *testing.T) {
	hints := scheduler.Hints()
	got, ok := hints["kubernetes.io/arch"]
	if !ok {
		t.Fatal("expected kubernetes.io/arch hint")
	}
	if got != "amd64" {
		t.Fatalf("expected amd64, got %q", got)
	}
}

func TestHints_ReturnsNewMapEachCall(t *testing.T) {
	h1 := scheduler.Hints()
	h2 := scheduler.Hints()
	h1["extra"] = "val"
	if _, ok := h2["extra"]; ok {
		t.Fatal("hints maps should be independent")
	}
}
