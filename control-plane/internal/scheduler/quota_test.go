// control-plane/internal/scheduler/quota_test.go
package scheduler_test

import (
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func TestCheckQuota_Unlimited(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 0, MemoryBytes: 0}
	active := []scheduler.JobResources{}
	req := scheduler.JobResources{NumWorkers: 10, WorkerCPU: "8", WorkerMemory: "32Gi", HeadCPU: "2", HeadMemory: "4Gi"}
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("unlimited quota should always pass: %v", err)
	}
}

func TestCheckQuota_WithinLimit(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 8000, MemoryBytes: 16 * 1024 * 1024 * 1024} // 8 cores, 16Gi
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"}, // 3 cores, 6Gi in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"} // 3 more = 6 total
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("6 cores within 8-core quota: %v", err)
	}
}

func TestCheckQuota_CPUExceeded(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 4000, MemoryBytes: 0} // 4 cores CPU quota, unlimited memory
	active := []scheduler.JobResources{
		{NumWorkers: 2, WorkerCPU: "1", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"}, // 3 cores in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"} // 3 more = 6 > 4
	if err := scheduler.CheckQuota(quota, active, req); err == nil {
		t.Fatal("expected quota exceeded error")
	}
}

func TestCheckQuota_MemoryExceeded(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 0, MemoryBytes: 8 * 1024 * 1024 * 1024} // unlimited CPU, 8Gi memory
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "2Gi"}, // 6Gi in use
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "4Gi", HeadCPU: "1", HeadMemory: "1Gi"} // 5 more = 11 > 8
	if err := scheduler.CheckQuota(quota, active, req); err == nil {
		t.Fatal("expected memory quota exceeded error")
	}
}

func TestCheckQuota_ExactLimit(t *testing.T) {
	quota := scheduler.TenantQuota{CPUMillicores: 4000, MemoryBytes: 0}
	active := []scheduler.JobResources{
		{NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "1Gi", HeadCPU: "1", HeadMemory: "1Gi"}, // 3 cores
	}
	req := scheduler.JobResources{NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi", HeadCPU: "0", HeadMemory: "0"}
	// total = 3+1 = 4 = quota exactly → should pass
	if err := scheduler.CheckQuota(quota, active, req); err != nil {
		t.Fatalf("exact limit should pass: %v", err)
	}
}
