# training-runtime/base-images/Dockerfile
# Base image for all platform training jobs.
# CPU-only; no GPU required for local demo.
FROM python:3.11-slim

WORKDIR /app

# System deps for torch CPU build
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgomp1 curl \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    --extra-index-url https://download.pytorch.org/whl/cpu

# Verify critical imports at build time
RUN python -c "import ray, mlflow, torch, torchvision, boto3, onnx; print('imports OK')"
