// control-plane/internal/jobs/rayjob.go
package jobs

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RayJobGVR is the GroupVersionResource for RayJob CRDs.
var RayJobGVR = schema.GroupVersionResource{
	Group:    "ray.io",
	Version:  "v1",
	Resource: "rayjobs",
}

// RayJobNamespace is the Kubernetes namespace where platform RayJobs are created.
const RayJobNamespace = "platform-jobs"

// BuildRayJob constructs an unstructured RayJob object from a TrainingJob and
// placement hints. The returned object is ready for submission via the dynamic client.
func BuildRayJob(job *TrainingJob, hints map[string]string) *unstructured.Unstructured {
	name := rayJobName(job.ID)
	entrypoint := buildEntrypoint(job.Command, job.Args)
	runtimeEnv := buildRuntimeEnvYAML(job.Env)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "ray.io/v1",
			"kind":       "RayJob",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": RayJobNamespace,
				"labels": map[string]interface{}{
					"platform.io/job-id":    job.ID,
					"platform.io/tenant-id": job.TenantID,
				},
			},
			"spec": map[string]interface{}{
				"entrypoint":    entrypoint,
				"runtimeEnvYAML": runtimeEnv,
				"rayClusterSpec": map[string]interface{}{
					"headGroupSpec": map[string]interface{}{
						"replicas": int64(1),
						"template": podTemplate(job.Image, job.HeadCPU, job.HeadMemory, hints),
					},
					"workerGroupSpecs": []interface{}{
						map[string]interface{}{
							"groupName": "workers",
							"replicas":  int64(job.NumWorkers),
							"template":  podTemplate(job.Image, job.WorkerCPU, job.WorkerMemory, hints),
						},
					},
				},
			},
		},
	}
	return obj
}

func rayJobName(jobID string) string {
	short := jobID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("rayjob-%s", short)
}

func buildEntrypoint(command, args []string) string {
	parts := append(command, args...)
	return strings.Join(parts, " ")
}

func buildRuntimeEnvYAML(env map[string]string) string {
	if len(env) == 0 {
		return "env_vars: {}"
	}
	var sb strings.Builder
	sb.WriteString("env_vars:\n")
	for k, v := range env {
		fmt.Fprintf(&sb, "  %s: %q\n", k, v)
	}
	return sb.String()
}

func podTemplate(image, cpu, memory string, hints map[string]string) map[string]interface{} {
	nodeSelector := make(map[string]interface{})
	for k, v := range hints {
		nodeSelector[k] = v
	}
	return map[string]interface{}{
		"spec": map[string]interface{}{
			"nodeSelector": nodeSelector,
			"containers": []interface{}{
				map[string]interface{}{
					"name":  "ray-worker",
					"image": image,
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{
							"cpu":    cpu,
							"memory": memory,
						},
						"limits": map[string]interface{}{
							"cpu":    cpu,
							"memory": memory,
						},
					},
				},
			},
		},
	}
}
