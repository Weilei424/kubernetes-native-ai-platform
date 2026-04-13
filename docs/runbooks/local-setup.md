# Local Stack Setup

## Prerequisites

- Docker Desktop (with Kubernetes disabled — kind manages K8s)
- `kind` CLI: `brew install kind` or https://kind.sigs.k8s.io/
- `helm` CLI: `brew install helm`
- `kubectl` CLI: `brew install kubectl`
- Go 1.22+, Python 3.11+

## 1. Start the full local stack

```bash
make local-up
```

This creates the `aiplatform-local` kind cluster, deploys all services as Kubernetes workloads
(PostgreSQL, Kafka, MinIO, MLflow, Prometheus, Grafana), and installs the KubeRay operator.
Takes ~5 minutes on first run.

## 2. Build and load demo training images

```bash
make -C infra/local demo-images
```

Builds `platform/minimal-trainer` and `platform/resnet50-exporter`, loads them into kind,
and pre-pulls the Triton image. Run once before the first demo.

## 3. Run database migrations and start control plane

```bash
cd control-plane && go run ./cmd/server
```

In a second terminal:
```bash
cd operator && go run ./cmd/operator
```

## 4. Seed a demo tenant, project, and token

```bash
kubectl exec -n aiplatform deploy/postgres -- psql -U aiplatform aiplatform <<'SQL'
INSERT INTO tenants (name, cpu_quota, memory_quota)
VALUES ('demo', 32000, 68719476736)
ON CONFLICT DO NOTHING;

INSERT INTO projects (tenant_id, name)
SELECT id, 'vision-demo' FROM tenants WHERE name='demo'
ON CONFLICT DO NOTHING;
SQL
```

The token must start with exactly the 8 characters used as `token_prefix`.
Use `demo-tok` as the prefix — so your token must start with `demo-tok`:

```bash
# Generate a bcrypt hash for the token "demo-tok-abc123" (replace with your own suffix)
DEMO_TOKEN="demo-tok-abc123"
HASH=$(htpasswd -bnBC 10 "" "$DEMO_TOKEN" | tr -d ':\n' | sed 's/^\$apr1\$/\$2y\$/')
# Alternatively with Python:
# HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$DEMO_TOKEN', bcrypt.gensalt()).decode())")

kubectl exec -n aiplatform deploy/postgres -- psql -U aiplatform aiplatform \
  -c "INSERT INTO api_tokens (tenant_id, token_hash, token_prefix)
      SELECT id, '$HASH', 'demo-tok' FROM tenants WHERE name='demo';"
```

Export the project UUID — required for the demo script:

```bash
export PROJECT_ID=$(kubectl exec -n aiplatform deploy/postgres -- \
  psql -U aiplatform aiplatform -t -A \
  -c "SELECT id FROM projects WHERE name='vision-demo' LIMIT 1")
echo "PROJECT_ID=$PROJECT_ID"
```

## 5. Build the CLI

```bash
cd cli && go build -o platformctl . && export PATH=$PWD:$PATH
```

## 6. Set environment and verify

```bash
export PLATFORMCTL_HOST=http://localhost:8080
export PLATFORMCTL_TOKEN=demo-tok-abc123   # must match what you hashed above

curl http://localhost:8080/healthz   # {"status":"ok"}
platformctl train --help
```

## Stop the stack

```bash
make local-down
```
