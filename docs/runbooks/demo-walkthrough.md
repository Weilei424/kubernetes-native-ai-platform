# Demo Walkthrough

Step-by-step guide for all 9 demo requirements (`PROJECT_INSTRUCTION.md §17`).

**Prerequisites:** complete `local-setup.md` first.

```bash
export PLATFORMCTL_HOST=http://localhost:8080
export PLATFORMCTL_TOKEN=demo-tok-abc123   # must start with "demo-tok" — see local-setup.md §4
export PROJECT_ID=$(kubectl exec -n aiplatform deploy/postgres -- \
  psql -U aiplatform aiplatform -t -A \
  -c "SELECT id FROM projects WHERE name='vision-demo' LIMIT 1")
```

## Automated demo

```bash
bash examples/demo-clients/demo.sh
```

Set `DEMO_MODE=full` for the ResNet50 ONNX path (real model, real Triton inference).

## Record for portfolio

```bash
bash examples/demo-clients/record.sh demo.cast
# replay: asciinema play demo.cast
```

## Step-by-step

### 1. Submit distributed training job
```bash
# Patch the project UUID into a temp copy of the spec, then submit
SPEC=$(mktemp --suffix=.yaml)
sed "s|REPLACE_WITH_PROJECT_UUID|$PROJECT_ID|" examples/training-specs/resnet.yaml > "$SPEC"
platformctl train submit -f "$SPEC"
rm -f "$SPEC"
# JOB_ID=<from output>
# RUN_ID=<from output>
```

### 2. Ray workers
```bash
kubectl get pods -n aiplatform -l ray.io/cluster
```

### 3. MLflow run
```bash
platformctl train status $JOB_ID --watch
# MLflow UI: http://localhost:5000
```

### 4. Register model
```bash
platformctl model register --run-id $RUN_ID --name resnet50
# VERSION=<from output>
```

### 5. Promote to production
```bash
platformctl model promote --name resnet50 --version $VERSION --alias staging
platformctl model promote --name resnet50 --version $VERSION --alias production
```

### 6. Deploy to Triton
```bash
platformctl deploy create -f examples/deployment-specs/resnet-prod.yaml
# DEPLOY_ID=<from output>
```

### 7. Inference
```bash
platformctl deploy status $DEPLOY_ID --watch
# once running:
curl -X POST http://<endpoint>/v2/models/resnet50/infer \
  -H "Content-Type: application/json" \
  -d '{"inputs":[{"name":"input","shape":[1,64],"datatype":"FP32","data":[...]}]}'
```

### 8. Metrics
- Prometheus: http://localhost:30090
- Grafana: http://localhost:30300

### 9. Failure scenario + rollback capability

**Failure scenario — quota exceeded (guaranteed rejection):**
```bash
platformctl train submit -f /dev/stdin <<YAML
name: overflow
project_id: $PROJECT_ID
runtime: {image: img}
resources: {num_workers: 100, worker_cpu: "128", worker_memory: "512Gi", head_cpu: "64", head_memory: "256Gi"}
YAML
# Expected: HTTP 422 quota_exceeded — platform rejects at admission, cluster untouched
```

**Rollback capability** (requires 2+ revisions — see note below):
```bash
curl -X POST -H "Authorization: Bearer $PLATFORMCTL_TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  $PLATFORMCTL_HOST/v1/deployments/$DEPLOY_ID/rollback
```

> **Note:** Rollback atomically creates a new revision pointing to the prior artifact_uri and
> clears any stale `failure_reason`. In this single-deployment demo, the response is
> `"already at earliest revision"` — the correct behaviour. In a real workflow where you
> deploy v1, then deploy v2 (which fails), rollback restores v1 and clears the failure state.
