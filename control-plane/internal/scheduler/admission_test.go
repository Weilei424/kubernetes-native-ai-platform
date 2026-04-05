// control-plane/internal/scheduler/admission_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func validRequest() scheduler.AdmissionRequest {
	return scheduler.AdmissionRequest{
		Image:        "ghcr.io/org/ray-torch:2.9",
		NumWorkers:   2,
		WorkerCPU:    "2",
		WorkerMemory: "4Gi",
		HeadCPU:      "1",
		HeadMemory:   "2Gi",
	}
}

func TestAdmit_Valid(t *testing.T) {
	if err := scheduler.Admit(validRequest()); err != nil {
		t.Fatalf("expected valid request to pass: %v", err)
	}
}

func TestAdmit_EmptyImage(t *testing.T) {
	r := validRequest()
	r.Image = ""
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestAdmit_ZeroWorkers(t *testing.T) {
	r := validRequest()
	r.NumWorkers = 0
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for zero workers")
	}
}

func TestAdmit_NegativeWorkers(t *testing.T) {
	r := validRequest()
	r.NumWorkers = -1
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for negative workers")
	}
}

func TestAdmit_InvalidCPU(t *testing.T) {
	r := validRequest()
	r.WorkerCPU = "not-a-cpu"
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for invalid CPU quantity")
	}
}

func TestAdmit_InvalidMemory(t *testing.T) {
	r := validRequest()
	r.WorkerMemory = "not-memory"
	if err := scheduler.Admit(r); err == nil {
		t.Fatal("expected error for invalid memory quantity")
	}
}

func TestAdmit_MillicoreCPU(t *testing.T) {
	r := validRequest()
	r.WorkerCPU = "500m"
	if err := scheduler.Admit(r); err != nil {
		t.Fatalf("500m should be valid CPU: %v", err)
	}
}
