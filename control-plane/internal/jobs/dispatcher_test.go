// control-plane/internal/jobs/dispatcher_test.go
package jobs_test

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/testutil"
)

func TestDispatcher_PromotesPendingToQueued(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed tenant and project
	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('disp-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'disp-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)

	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "disp-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Set up fake dynamic Kubernetes client
	scheme := runtime.NewScheme()
	rayJobGVR := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	fakeClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		rayJobGVR: "RayJobList",
	})

	pub := &events.NoOpPublisher{}
	d := jobs.NewDispatcher(store, fakeClient, pub, 100*time.Millisecond)

	dispCtx, dispCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.Run(dispCtx)
		close(done)
	}()

	// Wait for the job to become QUEUED
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := store.GetJob(ctx, job.ID, tenantID)
		if err == nil && j.Status == "QUEUED" && j.RayJobName != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	dispCancel()
	<-done

	finalJob, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if finalJob.Status != "QUEUED" {
		t.Fatalf("expected QUEUED, got %q", finalJob.Status)
	}
	if finalJob.RayJobName == nil {
		t.Fatal("expected rayjob_name to be set")
	}

	// Verify the RayJob was created in the fake k8s client
	actions := fakeClient.Actions()
	created := false
	for _, a := range actions {
		if a.GetVerb() == "create" && a.GetResource() == rayJobGVR {
			created = true
			break
		}
	}
	if !created {
		t.Fatal("expected a create action on rayjobs GVR")
	}

	// Verify the Kubernetes action list included a "create" for the rayjobs resource
	var createAction k8stesting.CreateAction
	for _, a := range actions {
		if ca, ok := a.(k8stesting.CreateAction); ok && a.GetResource() == rayJobGVR {
			createAction = ca
			break
		}
	}
	if createAction == nil {
		t.Fatal("no create action found for rayjobs")
	}
}

func TestDispatcher_QuotaExceeded_StaysPending(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('quota-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.Exec(ctx, `UPDATE tenants SET cpu_quota = 500 WHERE id = $1`, tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'quota-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)

	// Job requests 2 CPUs (2000 millicores) but quota is only 500 millicores
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "quota-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "2", WorkerMemory: "1Gi",
		HeadCPU: "0", HeadMemory: "0",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create job: %v", err)
	}

	scheme := runtime.NewScheme()
	rayJobGVR := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	fakeClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		rayJobGVR: "RayJobList",
	})

	d := jobs.NewDispatcher(store, fakeClient, &events.NoOpPublisher{}, 50*time.Millisecond)
	dispCtx, dispCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.Run(dispCtx)
		close(done)
	}()

	// Let dispatcher run a few ticks
	time.Sleep(300 * time.Millisecond)
	dispCancel()
	<-done

	finalJob, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if finalJob.Status != "PENDING" {
		t.Fatalf("over-quota job should stay PENDING, got %q", finalJob.Status)
	}
}

func TestDispatcher_RetryQueuedWithoutRayJob(t *testing.T) {
	pool := testutil.SetupDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var tenantID, projectID string
	pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('retry-tenant') RETURNING id::text`).Scan(&tenantID)
	pool.QueryRow(ctx, `INSERT INTO projects (tenant_id, name) VALUES ($1, 'retry-proj') RETURNING id::text`, tenantID).Scan(&projectID)

	store := jobs.NewPostgresJobStore(pool)

	// Create a job that is already QUEUED but has no rayjob_name (simulating a failed CRD submission)
	job := &jobs.TrainingJob{
		TenantID: tenantID, ProjectID: projectID, Name: "retry-job",
		Status: "PENDING", Image: "img:1", Command: []string{"run"},
		Args: []string{}, Env: map[string]string{},
		NumWorkers: 1, WorkerCPU: "1", WorkerMemory: "1Gi",
		HeadCPU: "1", HeadMemory: "1Gi",
	}
	run := &jobs.TrainingRun{TenantID: tenantID, Status: "PENDING"}
	if err := store.CreateJobWithRun(ctx, job, run); err != nil {
		t.Fatalf("create job: %v", err)
	}
	// Manually transition to QUEUED (simulating a previous dispatcher cycle that transitioned but failed CRD)
	if err := store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		t.Fatalf("transition to QUEUED: %v", err)
	}

	scheme := runtime.NewScheme()
	rayJobGVR := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	fakeClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		rayJobGVR: "RayJobList",
	})

	d := jobs.NewDispatcher(store, fakeClient, &events.NoOpPublisher{}, 100*time.Millisecond)
	dispCtx, dispCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.Run(dispCtx)
		close(done)
	}()

	// Wait for rayjob_name to be set
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := store.GetJob(ctx, job.ID, tenantID)
		if err == nil && j.RayJobName != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	dispCancel()
	<-done

	finalJob, err := store.GetJob(ctx, job.ID, tenantID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if finalJob.RayJobName == nil {
		t.Fatal("expected rayjob_name to be set after retry")
	}
	if finalJob.Status != "QUEUED" {
		t.Fatalf("expected QUEUED, got %q", finalJob.Status)
	}
}
