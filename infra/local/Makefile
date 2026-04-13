# infra/local/Makefile
CLUSTER_NAME    := aiplatform-local
NAMESPACE       := aiplatform
KUBERAY_VERSION := 1.3.0

.PHONY: local-up local-down local-status kuberay-up demo-images

local-up:
	@echo "==> Creating kind cluster $(CLUSTER_NAME)..."
	kind create cluster --name $(CLUSTER_NAME) --config kind-config.yaml
	@echo "==> Applying namespace..."
	kubectl apply -f manifests/namespace.yaml
	@echo "==> Deploying PostgreSQL..."
	kubectl apply -f manifests/postgres/
	kubectl rollout status statefulset/postgres -n $(NAMESPACE) --timeout=180s
	@echo "==> Deploying Kafka..."
	kubectl apply -f manifests/kafka/
	kubectl rollout status statefulset/kafka -n $(NAMESPACE) --timeout=180s
	@echo "==> Deploying MinIO..."
	kubectl apply -f manifests/minio/pvc.yaml
	kubectl apply -f manifests/minio/secret.yaml
	kubectl apply -f manifests/minio/deployment.yaml
	kubectl apply -f manifests/minio/service.yaml
	kubectl rollout status deployment/minio -n $(NAMESPACE) --timeout=180s
	kubectl apply -f manifests/minio/bucket-job.yaml
	kubectl wait --for=condition=complete job/minio-create-bucket -n $(NAMESPACE) --timeout=120s
	@echo "==> Deploying MLflow..."
	kubectl apply -f manifests/mlflow/
	kubectl rollout status deployment/mlflow -n $(NAMESPACE) --timeout=180s
	@echo "==> Deploying Prometheus..."
	kubectl apply -f manifests/prometheus/
	kubectl rollout status deployment/prometheus -n $(NAMESPACE) --timeout=120s
	@echo "==> Deploying Grafana..."
	kubectl apply -f manifests/grafana/
	kubectl rollout status deployment/grafana -n $(NAMESPACE) --timeout=120s
	@echo "==> Installing KubeRay operator..."
	$(MAKE) kuberay-up
	@echo ""
	@echo "==> Stack is ready."
	@$(MAKE) local-status

local-down:
	kind delete cluster --name $(CLUSTER_NAME)

kuberay-up:
	@echo "==> Installing KubeRay operator $(KUBERAY_VERSION)..."
	helm repo add kuberay https://ray-project.github.io/kuberay-helm/ --force-update
	helm repo update
	helm upgrade --install kuberay-operator kuberay/kuberay-operator \
		--version $(KUBERAY_VERSION) \
		--namespace $(NAMESPACE) \
		--set image.tag=$(KUBERAY_VERSION)
	kubectl rollout status deployment/kuberay-operator -n $(NAMESPACE) --timeout=120s
	@echo "==> KubeRay operator ready."

demo-images:
	@echo "==> Building training images..."
	docker build -t platform/training-base:latest ../../training-runtime/base-images/
	docker build -t platform/minimal-trainer:latest ../../training-runtime/examples/minimal/
	docker build -t platform/resnet50-exporter:latest ../../training-runtime/examples/resnet50/
	@echo "==> Loading images into kind cluster $(CLUSTER_NAME)..."
	kind load docker-image platform/minimal-trainer:latest --name $(CLUSTER_NAME)
	kind load docker-image platform/resnet50-exporter:latest --name $(CLUSTER_NAME)
	@echo "==> Pre-loading Triton image..."
	docker pull nvcr.io/nvidia/tritonserver:24.01-py3-min || true
	kind load docker-image nvcr.io/nvidia/tritonserver:24.01-py3-min --name $(CLUSTER_NAME) || true
	@echo "==> Demo images ready."

local-status:
	@echo "==> Pods in namespace $(NAMESPACE):"
	kubectl get pods -n $(NAMESPACE)
	@echo ""
	@echo "==> Endpoints:"
	@echo "  MLflow UI:    http://localhost:5000 (via kind NodePort)"
	@echo "  PostgreSQL:   kubectl port-forward svc/postgres -n $(NAMESPACE) 5432:5432"
	@echo "                DSN: postgres://aiplatform:aiplatform@localhost:5432/aiplatform?sslmode=disable"
	@echo "  MinIO API:    kubectl port-forward svc/minio -n $(NAMESPACE) 9000:9000"
	@echo "  Kafka:        kubectl port-forward svc/kafka -n $(NAMESPACE) 9092:9092"
	@echo "  Prometheus:   http://localhost:30090"
	@echo "  Grafana:      http://localhost:30300 (anonymous admin)"
