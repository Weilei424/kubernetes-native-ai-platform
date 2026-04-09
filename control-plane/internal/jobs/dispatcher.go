// control-plane/internal/jobs/dispatcher.go
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/events"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/observability"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

// Dispatcher polls for PENDING jobs, enforces quota, submits RayJob CRDs,
// and retries QUEUED jobs that haven't had their CRD submitted yet.
type Dispatcher struct {
	store     Store
	k8s       dynamic.Interface
	publisher events.Publisher
	interval  time.Duration
}

// NewDispatcher creates a Dispatcher. interval is the polling tick duration.
func NewDispatcher(store Store, k8s dynamic.Interface, publisher events.Publisher, interval time.Duration) *Dispatcher {
	return &Dispatcher{
		store:     store,
		k8s:       k8s,
		publisher: publisher,
		interval:  interval,
	}
}

// Run starts the dispatcher loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	// 1. Retry QUEUED jobs that haven't had a RayJob CRD submitted yet.
	d.retryQueuedWithoutRayJob(ctx)

	// 2. Promote the oldest PENDING job per tenant (FIFO, one per tenant per tick).
	tenantIDs, err := d.store.GetTenantIDsWithPendingJobs(ctx)
	if err != nil {
		slog.Error("dispatcher: list pending tenants", "error", err)
		return
	}
	for _, tenantID := range tenantIDs {
		d.promoteOldestPending(ctx, tenantID)
	}

	// Update queue depth metric.
	depth, err := d.store.CountQueuedJobs(ctx)
	if err != nil {
		slog.Warn("dispatcher: count queued jobs for metric", "error", err)
	} else {
		observability.JobQueueDepth.Set(float64(depth))
	}
}

func (d *Dispatcher) promoteOldestPending(ctx context.Context, tenantID string) {
	job, err := d.store.GetOldestPendingJob(ctx, tenantID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("dispatcher: get oldest pending job", "tenant_id", tenantID, "error", err)
		}
		return
	}

	// Quota check
	cpuQuota, memQuota, err := d.store.GetTenantQuota(ctx, tenantID)
	if err != nil {
		slog.Error("dispatcher: get tenant quota", "tenant_id", tenantID, "error", err)
		return
	}
	activeJobs, err := d.store.ListActiveJobs(ctx, tenantID)
	if err != nil {
		slog.Error("dispatcher: list active jobs", "tenant_id", tenantID, "error", err)
		return
	}

	activeResources := make([]scheduler.JobResources, 0, len(activeJobs))
	for _, aj := range activeJobs {
		activeResources = append(activeResources, scheduler.JobResources{
			NumWorkers: aj.NumWorkers, WorkerCPU: aj.WorkerCPU,
			WorkerMemory: aj.WorkerMemory, HeadCPU: aj.HeadCPU, HeadMemory: aj.HeadMemory,
		})
	}
	quota := scheduler.TenantQuota{CPUMillicores: cpuQuota, MemoryBytes: memQuota}
	requested := scheduler.JobResources{
		NumWorkers: job.NumWorkers, WorkerCPU: job.WorkerCPU,
		WorkerMemory: job.WorkerMemory, HeadCPU: job.HeadCPU, HeadMemory: job.HeadMemory,
	}
	if err := scheduler.CheckQuota(quota, activeResources, requested); err != nil {
		slog.Debug("dispatcher: quota exceeded, skipping", "job_id", job.ID, "reason", err)
		return
	}

	// Transition PENDING → QUEUED
	if err := d.store.TransitionJobStatus(ctx, job.ID, "PENDING", "QUEUED", nil); err != nil {
		slog.Error("dispatcher: transition PENDING→QUEUED", "job_id", job.ID, "error", err)
		return
	}
	d.publishEvent(ctx, "platform.job.queued", job.ID, job.TenantID, "QUEUED", nil)

	// Submit RayJob CRD
	d.submitRayJob(ctx, job)
}

func (d *Dispatcher) retryQueuedWithoutRayJob(ctx context.Context) {
	pending, err := d.store.GetQueuedJobsWithoutRayJob(ctx)
	if err != nil {
		slog.Error("dispatcher: list queued without rayjob", "error", err)
		return
	}
	for _, job := range pending {
		d.submitRayJob(ctx, job)
	}
}

func (d *Dispatcher) submitRayJob(ctx context.Context, job *TrainingJob) {
	hints := scheduler.Hints()
	obj := BuildRayJob(job, hints)

	_, err := d.k8s.Resource(RayJobGVR).Namespace(RayJobNamespace).Create(
		ctx, obj, metav1.CreateOptions{},
	)
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			// CRD was previously created but SetRayJobName failed — recover by setting the name now
			slog.Info("dispatcher: RayJob already exists, setting rayjob_name", "job_id", job.ID, "rayjob_name", obj.GetName())
			if setErr := d.store.SetRayJobName(ctx, job.ID, obj.GetName()); setErr != nil {
				slog.Error("dispatcher: set rayjob_name after AlreadyExists", "job_id", job.ID, "error", setErr)
			}
			return
		}
		slog.Error("dispatcher: create RayJob", "job_id", job.ID, "error", err)
		return
	}

	if err := d.store.SetRayJobName(ctx, job.ID, obj.GetName()); err != nil {
		slog.Error("dispatcher: set rayjob_name", "job_id", job.ID, "error", err)
	}
	slog.Info("dispatcher: RayJob submitted", "job_id", job.ID, "rayjob_name", obj.GetName())
}

func (d *Dispatcher) publishEvent(ctx context.Context, topic, jobID, tenantID, status string, failureReason *string) {
	evt := JobEvent{
		JobID:         jobID,
		TenantID:      tenantID,
		Status:        status,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		FailureReason: failureReason,
	}
	if err := d.publisher.Publish(ctx, topic, evt); err != nil {
		slog.Warn("dispatcher: kafka publish failed", "topic", topic, "job_id", jobID, "error", err)
	}
}
