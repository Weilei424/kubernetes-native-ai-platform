// operator/internal/reconciler/deployment_reconciler.go
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/observability"
)

// DefaultPollInterval is how often the reconciler polls the control plane.
const DefaultPollInterval = 10 * time.Second

// deploymentRecord mirrors the control plane's Deployment JSON for operator use.
type deploymentRecord struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Status          string `json:"status"`
	DesiredReplicas int    `json:"desired_replicas"`
	ArtifactURI     string `json:"artifact_uri"`
	ModelName       string `json:"model_name"`
}

// statusUpdatePayload is sent to PATCH /internal/v1/deployments/:id/status.
type statusUpdatePayload struct {
	Status          string `json:"status"`
	ServingEndpoint string `json:"serving_endpoint,omitempty"`
}

// DeploymentReconciler polls the control plane for pending deployments and
// reconciles the desired serving state in Kubernetes. It implements
// manager.Runnable so it integrates with the controller-runtime manager lifecycle.
type DeploymentReconciler struct {
	Client          client.Client
	ControlPlaneURL string
	HTTPClient      *http.Client
	MinioEndpoint   string
	PollInterval    time.Duration
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration
}

// Start implements manager.Runnable. It is called by the manager after the
// cache is synced and runs until ctx is cancelled.
func (r *DeploymentReconciler) Start(ctx context.Context) error {
	interval := r.PollInterval
	if interval == 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("deployment reconciler started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

func (r *DeploymentReconciler) reconcileAll(ctx context.Context) {
	start := time.Now()
	deps, err := r.listPendingDeployments(ctx)
	if err != nil {
		observability.ReconcileErrors.WithLabelValues("deployment").Inc()
		slog.Error("deployment reconciler: list pending", "error", err)
		return
	}
	for _, dep := range deps {
		if err := r.reconcileOne(ctx, dep); err != nil {
			observability.ReconcileErrors.WithLabelValues("deployment").Inc()
			slog.Error("deployment reconciler: reconcile", "id", dep.ID, "error", err)
		}
	}
	observability.ReconcileDuration.WithLabelValues("deployment").Observe(time.Since(start).Seconds())
}

func (r *DeploymentReconciler) reconcileOne(ctx context.Context, dep *deploymentRecord) error {
	// Handle user-initiated deletion: clean up Kubernetes resources and mark deleted.
	if dep.Status == "deleting" {
		return r.reconcileDelete(ctx, dep)
	}

	podName := TritonPodName(dep.ID)
	svcName := TritonServiceName(dep.ID)

	// Ensure pod exists.
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{Name: podName, Namespace: dep.Namespace}
	err := r.Client.Get(ctx, podKey, pod)
	if apierrors.IsNotFound(err) {
		if createErr := r.Client.Create(ctx, r.buildPod(dep)); createErr != nil {
			return fmt.Errorf("create pod: %w", createErr)
		}
		pod = nil // just created; skip phase check this tick
	} else if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}

	// Ensure service exists.
	svc := &corev1.Service{}
	svcKey := client.ObjectKey{Name: svcName, Namespace: dep.Namespace}
	if svcErr := r.Client.Get(ctx, svcKey, svc); apierrors.IsNotFound(svcErr) {
		if createErr := r.Client.Create(ctx, r.buildService(dep)); createErr != nil {
			return fmt.Errorf("create service: %w", createErr)
		}
	} else if svcErr != nil {
		return fmt.Errorf("get service: %w", svcErr)
	}

	// Map pod phase → platform status and report if changed.
	if pod == nil {
		// Pod was just created; report provisioning.
		return r.reportStatus(ctx, dep.ID, "provisioning", "")
	}

	newStatus := MapPodPhase(pod.Status.Phase)
	if newStatus == dep.Status {
		return nil // no change
	}

	endpoint := ""
	if newStatus == "running" {
		endpoint = ServingEndpoint(dep.ID, dep.Namespace)
	}
	return r.reportStatus(ctx, dep.ID, newStatus, endpoint)
}

// reconcileDelete removes the Kubernetes Pod and Service for a deployment that
// the user has requested to delete, then marks the deployment as "deleted".
func (r *DeploymentReconciler) reconcileDelete(ctx context.Context, dep *deploymentRecord) error {
	podKey := client.ObjectKey{Name: TritonPodName(dep.ID), Namespace: dep.Namespace}
	pod := &corev1.Pod{}
	if err := r.Client.Get(ctx, podKey, pod); err == nil {
		if err := r.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get pod for deletion: %w", err)
	}

	svcKey := client.ObjectKey{Name: TritonServiceName(dep.ID), Namespace: dep.Namespace}
	svc := &corev1.Service{}
	if err := r.Client.Get(ctx, svcKey, svc); err == nil {
		if err := r.Client.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service: %w", err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get service for deletion: %w", err)
	}

	return r.reportStatus(ctx, dep.ID, "deleted", "")
}

func (r *DeploymentReconciler) buildPod(dep *deploymentRecord) *corev1.Pod {
	minioEndpoint := r.MinioEndpoint
	if minioEndpoint == "" {
		minioEndpoint = "http://minio:9000"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TritonPodName(dep.ID),
			Namespace: dep.Namespace,
			Labels: map[string]string{
				"platform.ai/deployment-id": dep.ID,
				"app":                       "triton",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			InitContainers: []corev1.Container{
				{
					Name:  "model-loader",
					Image: "localhost:5000/model-loader:latest",
					Env: []corev1.EnvVar{
						{Name: "ARTIFACT_URI", Value: dep.ArtifactURI},
						{Name: "MINIO_ENDPOINT", Value: minioEndpoint},
						{Name: "MODEL_NAME", Value: dep.ModelName},
						{Name: "MODEL_VERSION", Value: "1"},
						{
							Name: "MINIO_ACCESS_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "minio-credentials"},
									Key:                  "access-key",
								},
							},
						},
						{
							Name: "MINIO_SECRET_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "minio-credentials"},
									Key:                  "secret-key",
								},
							},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "model-repo", MountPath: "/model-repo"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "triton",
					Image: "nvcr.io/nvidia/tritonserver:24.01-py3",
					Args: []string{
						"tritonserver",
						"--model-repository=/model-repo",
						"--strict-model-config=false",
					},
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8000},
						{Name: "grpc", ContainerPort: 8001},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "model-repo", MountPath: "/model-repo"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         "model-repo",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
		},
	}
}

func (r *DeploymentReconciler) buildService(dep *deploymentRecord) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TritonServiceName(dep.ID),
			Namespace: dep.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"platform.ai/deployment-id": dep.ID,
			},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8000},
				{Name: "grpc", Port: 8001},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func (r *DeploymentReconciler) listPendingDeployments(ctx context.Context) ([]*deploymentRecord, error) {
	url := r.ControlPlaneURL + "/internal/v1/deployments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain to allow connection reuse
		return nil, fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}

	var body struct {
		Deployments []*deploymentRecord `json:"deployments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return body.Deployments, nil
}

func (r *DeploymentReconciler) reportStatus(ctx context.Context, id, status, endpoint string) error {
	payload := statusUpdatePayload{Status: status, ServingEndpoint: endpoint}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/internal/v1/deployments/%s/status", r.ControlPlaneURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain to allow connection reuse
		return fmt.Errorf("unexpected status %d from internal API", resp.StatusCode)
	}
	return nil
}

// MapPodPhase maps a Kubernetes PodPhase to a platform deployment status string.
func MapPodPhase(phase corev1.PodPhase) string {
	switch phase {
	case corev1.PodRunning:
		return "running"
	case corev1.PodFailed:
		return "failed"
	default:
		// Pending, Succeeded (shouldn't happen for long-running server), Unknown
		return "provisioning"
	}
}

// TritonPodName returns the Kubernetes Pod name for a deployment ID.
func TritonPodName(deploymentID string) string {
	return "triton-" + deploymentID
}

// TritonServiceName returns the Kubernetes Service name for a deployment ID.
// Pod and Service intentionally share the same name — they are different API resources
// in Kubernetes and can coexist under one name within a namespace.
func TritonServiceName(deploymentID string) string {
	return "triton-" + deploymentID
}

// ServingEndpoint returns the in-cluster DNS address for a deployment.
func ServingEndpoint(deploymentID, namespace string) string {
	return fmt.Sprintf("triton-%s.%s.svc.cluster.local:8000", deploymentID, namespace)
}
