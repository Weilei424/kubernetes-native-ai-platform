// control-plane/internal/scheduler/placement.go
package scheduler

// Hints returns a map[string]string of Kubernetes node selector labels
// to attach to both the head and worker groups of the RayJob.
// Phase 1: CPU-only default. Extend here for GPU workloads in Phase 4+.
func Hints() map[string]string {
	return map[string]string{
		"kubernetes.io/arch": "amd64",
	}
}
