// control-plane/internal/jobs/rayjob_test.go
package jobs_test

import (
	"strings"
	"testing"

	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/jobs"
	"github.com/Weilei424/kubernetes-native-ai-platform/control-plane/internal/scheduler"
)

func sampleJob() *jobs.TrainingJob {
	return &jobs.TrainingJob{
		ID:           "abcdef12-0000-0000-0000-000000000000",
		TenantID:     "tenant-uuid-1111",
		Name:         "resnet-run-1",
		Image:        "ghcr.io/org/ray-torch:2.9",
		Command:      []string{"python", "train.py"},
		Args:         []string{"--epochs", "10"},
		Env:          map[string]string{"LR": "0.001"},
		NumWorkers:   2,
		WorkerCPU:    "2",
		WorkerMemory: "4Gi",
		HeadCPU:      "1",
		HeadMemory:   "2Gi",
	}
}

func TestBuildRayJob_Name(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	name, _, _ := unstructuredString(obj.Object, "metadata", "name")
	if !strings.HasPrefix(name, "rayjob-") {
		t.Fatalf("expected name to start with rayjob-, got %q", name)
	}
	if name != "rayjob-abcdef12" {
		t.Fatalf("expected rayjob-abcdef12, got %q", name)
	}
}

func TestBuildRayJob_Namespace(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	ns, _, _ := unstructuredString(obj.Object, "metadata", "namespace")
	if ns != "platform-jobs" {
		t.Fatalf("expected platform-jobs namespace, got %q", ns)
	}
}

func TestBuildRayJob_Labels(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	labels, _, _ := unstructuredStringMap(obj.Object, "metadata", "labels")
	if labels["platform.io/job-id"] != sampleJob().ID {
		t.Fatalf("missing or wrong platform.io/job-id label")
	}
	if labels["platform.io/tenant-id"] != sampleJob().TenantID {
		t.Fatalf("missing or wrong platform.io/tenant-id label")
	}
}

func TestBuildRayJob_APIVersion(t *testing.T) {
	obj := jobs.BuildRayJob(sampleJob(), scheduler.Hints())
	if obj.GetAPIVersion() != "ray.io/v1" {
		t.Fatalf("expected ray.io/v1, got %q", obj.GetAPIVersion())
	}
	if obj.GetKind() != "RayJob" {
		t.Fatalf("expected RayJob, got %q", obj.GetKind())
	}
}

// helpers for reading nested unstructured fields in tests
func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			v, ok := cur[f].(string)
			return v, ok, nil
		}
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}

func unstructuredStringMap(obj map[string]interface{}, fields ...string) (map[string]string, bool, error) {
	cur := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			raw, ok := cur[f].(map[string]interface{})
			if !ok {
				return nil, false, nil
			}
			result := make(map[string]string)
			for k, v := range raw {
				result[k], _ = v.(string)
			}
			return result, true, nil
		}
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return nil, false, nil
}
