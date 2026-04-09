// operator/internal/reconciler/deployment_reconciler_test.go
package reconciler_test

import (
	"testing"
	"time"

	"github.com/Weilei424/kubernetes-native-ai-platform/operator/internal/reconciler"
	corev1 "k8s.io/api/core/v1"
)

func TestMapPodPhase_Running(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodRunning)
	if got != "running" {
		t.Fatalf("expected running, got %q", got)
	}
}

func TestMapPodPhase_Failed(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodFailed)
	if got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
}

func TestMapPodPhase_Pending(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodPending)
	if got != "provisioning" {
		t.Fatalf("expected provisioning, got %q", got)
	}
}

func TestMapPodPhase_Unknown(t *testing.T) {
	got := reconciler.MapPodPhase(corev1.PodUnknown)
	if got != "provisioning" {
		t.Fatalf("expected provisioning for unknown, got %q", got)
	}
}

func TestTritonPodName(t *testing.T) {
	name := reconciler.TritonPodName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestTritonServiceName(t *testing.T) {
	name := reconciler.TritonServiceName("abc-123")
	if name != "triton-abc-123" {
		t.Fatalf("expected triton-abc-123, got %q", name)
	}
}

func TestServingEndpoint(t *testing.T) {
	ep := reconciler.ServingEndpoint("abc-123", "default")
	if ep != "triton-abc-123.default.svc.cluster.local:8000" {
		t.Fatalf("unexpected endpoint: %q", ep)
	}
}

func TestDefaultInterval(t *testing.T) {
	if reconciler.DefaultPollInterval != 10*time.Second {
		t.Fatalf("expected 10s, got %v", reconciler.DefaultPollInterval)
	}
}

