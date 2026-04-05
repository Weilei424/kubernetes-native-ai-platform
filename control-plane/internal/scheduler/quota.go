// control-plane/internal/scheduler/quota.go
package scheduler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TenantQuota holds the resource limits for a tenant.
// 0 means unlimited for that dimension.
type TenantQuota struct {
	CPUMillicores int64
	MemoryBytes   int64
}

// JobResources describes the resource shape of one job.
type JobResources struct {
	NumWorkers   int
	WorkerCPU    string
	WorkerMemory string
	HeadCPU      string
	HeadMemory   string
}

// CheckQuota returns nil if the requested resources fit within the tenant quota
// given the currently active (QUEUED + RUNNING) jobs. Returns an error if any
// quota dimension is exceeded.
func CheckQuota(quota TenantQuota, active []JobResources, req JobResources) error {
	usedCPU, usedMem, err := sumResources(active)
	if err != nil {
		return fmt.Errorf("sum active resources: %w", err)
	}

	reqCPU, reqMem, err := jobMillicoresAndBytes(req)
	if err != nil {
		return fmt.Errorf("parse requested resources: %w", err)
	}

	if quota.CPUMillicores > 0 && usedCPU+reqCPU > quota.CPUMillicores {
		return fmt.Errorf("CPU quota exceeded: %dm used + %dm requested > %dm limit",
			usedCPU, reqCPU, quota.CPUMillicores)
	}
	if quota.MemoryBytes > 0 && usedMem+reqMem > quota.MemoryBytes {
		return fmt.Errorf("memory quota exceeded: %d used + %d requested > %d limit",
			usedMem, reqMem, quota.MemoryBytes)
	}
	return nil
}

func sumResources(jobs []JobResources) (cpuMillicores, memBytes int64, err error) {
	for _, j := range jobs {
		cpu, mem, err := jobMillicoresAndBytes(j)
		if err != nil {
			return 0, 0, err
		}
		cpuMillicores += cpu
		memBytes += mem
	}
	return
}

func jobMillicoresAndBytes(j JobResources) (cpuMillicores, memBytes int64, err error) {
	wCPU, err := parseMillicores(j.WorkerCPU)
	if err != nil {
		return 0, 0, fmt.Errorf("worker_cpu: %w", err)
	}
	hCPU, err := parseMillicores(j.HeadCPU)
	if err != nil {
		return 0, 0, fmt.Errorf("head_cpu: %w", err)
	}
	wMem, err := parseBytes(j.WorkerMemory)
	if err != nil {
		return 0, 0, fmt.Errorf("worker_memory: %w", err)
	}
	hMem, err := parseBytes(j.HeadMemory)
	if err != nil {
		return 0, 0, fmt.Errorf("head_memory: %w", err)
	}

	cpuMillicores = int64(j.NumWorkers)*wCPU + hCPU
	memBytes = int64(j.NumWorkers)*wMem + hMem
	return
}

func parseMillicores(s string) (int64, error) {
	if s == "0" || s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return q.MilliValue(), nil
}

func parseBytes(s string) (int64, error) {
	if s == "0" || s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return q.Value(), nil
}
