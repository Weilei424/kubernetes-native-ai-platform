#!/usr/bin/env python3
# infra/docker/model-loader/loader.py
"""
model-loader: fetch a model artifact from MinIO and lay it out for Triton.

Required env vars:
  ARTIFACT_URI      e.g. "mlflow-artifacts:/resnet50/1/model/"
  MINIO_ENDPOINT    e.g. "http://minio:9000"
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  MODEL_NAME        used as the Triton model directory name
  MODEL_VERSION     Triton version directory, typically "1"

Output layout (written to /model-repo):
  /model-repo/<MODEL_NAME>/<MODEL_VERSION>/<files>
  /model-repo/<MODEL_NAME>/config.pbtxt
"""

import os
import sys
import pathlib
import boto3
from botocore.client import Config


def parse_artifact_uri(uri: str):
    """
    Parse an MLflow artifact URI into (bucket, prefix).

    Handles two forms:
      mlflow-artifacts:/bucket/path/to/artifact/
      s3://bucket/path/to/artifact/
    """
    if uri.startswith("mlflow-artifacts:/"):
        # Strip scheme; remainder is /bucket/prefix...
        path = uri[len("mlflow-artifacts:/"):]
    elif uri.startswith("s3://"):
        path = uri[len("s3://"):]
    else:
        raise ValueError(f"Unsupported artifact URI scheme: {uri!r}")

    parts = path.lstrip("/").split("/", 1)
    bucket = parts[0]
    prefix = parts[1] if len(parts) > 1 else ""
    return bucket, prefix


def download_prefix(s3, bucket: str, prefix: str, dest: pathlib.Path):
    """Download all objects under prefix to dest, preserving relative paths."""
    paginator = s3.get_paginator("list_objects_v2")
    found = False
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            rel = key[len(prefix):].lstrip("/")
            if not rel:
                continue
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            print(f"  downloading s3://{bucket}/{key} → {target}")
            s3.download_file(bucket, key, str(target))
            found = True
    if not found:
        print(f"WARNING: no objects found under s3://{bucket}/{prefix}", file=sys.stderr)


def write_config(model_dir: pathlib.Path, model_name: str):
    """Write a minimal Triton config.pbtxt using auto-inference for shapes."""
    config = f'name: "{model_name}"\nplatform: "onnxruntime_onnx"\n'
    config_path = model_dir / "config.pbtxt"
    config_path.write_text(config)
    print(f"  wrote {config_path}")


def main():
    artifact_uri  = os.environ["ARTIFACT_URI"]
    endpoint      = os.environ["MINIO_ENDPOINT"]
    access_key    = os.environ["MINIO_ACCESS_KEY"]
    secret_key    = os.environ["MINIO_SECRET_KEY"]
    model_name    = os.environ["MODEL_NAME"]
    model_version = os.environ.get("MODEL_VERSION", "1")

    print(f"model-loader: fetching {artifact_uri!r}")

    bucket, prefix = parse_artifact_uri(artifact_uri)

    s3 = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(signature_version="s3v4"),
    )

    model_repo = pathlib.Path("/model-repo")
    version_dir = model_repo / model_name / model_version
    version_dir.mkdir(parents=True, exist_ok=True)

    print(f"  bucket={bucket!r} prefix={prefix!r}")
    download_prefix(s3, bucket, prefix, version_dir)

    write_config(model_repo / model_name, model_name)

    print("model-loader: done")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"model-loader: FATAL: {e}", file=sys.stderr)
        sys.exit(1)
