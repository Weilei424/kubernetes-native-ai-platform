// control-plane/internal/scheduler/admission.go
package scheduler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// AdmissionRequest contains the fields validated at job submission time.
type AdmissionRequest struct {
	Image        string
	NumWorkers   int
	WorkerCPU    string
	WorkerMemory string
	HeadCPU      string
	HeadMemory   string
}

// Admit validates that a job spec is executable. Returns nil on success.
// Called synchronously in the HTTP handler before persisting the job.
func Admit(req AdmissionRequest) error {
	if req.Image == "" {
		return fmt.Errorf("image is required")
	}
	if req.NumWorkers < 1 {
		return fmt.Errorf("num_workers must be at least 1, got %d", req.NumWorkers)
	}
	if err := parseQuantity("worker_cpu", req.WorkerCPU); err != nil {
		return err
	}
	if err := parseQuantity("worker_memory", req.WorkerMemory); err != nil {
		return err
	}
	if err := parseQuantity("head_cpu", req.HeadCPU); err != nil {
		return err
	}
	if err := parseQuantity("head_memory", req.HeadMemory); err != nil {
		return err
	}
	return nil
}

func parseQuantity(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if _, err := resource.ParseQuantity(value); err != nil {
		return fmt.Errorf("%s %q is not a valid Kubernetes resource quantity: %w", field, value, err)
	}
	return nil
}
