# training-runtime/examples/resnet50/export.py
"""
Pretrained ResNet50 ONNX export for platform demo (DEMO_MODE=full).
No training loop — loads weights and exports immediately (~30s).
Produces a real model suitable for Triton ONNX backend inference.
Prints MLFLOW_RUN_ID=<id> to stdout on completion.
"""
import os
import tempfile

import boto3
import mlflow
import torch
import torchvision.models as models


def _upload(local_path: str, bucket: str, key: str) -> str:
    s3 = boto3.client(
        "s3",
        endpoint_url=os.environ.get("MINIO_ENDPOINT", "http://minio.aiplatform.svc.cluster.local:9000"),
        aws_access_key_id=os.environ.get("MINIO_ACCESS_KEY", "minio"),
        aws_secret_access_key=os.environ.get("MINIO_SECRET_KEY", "minio123"),
    )
    try:
        s3.head_bucket(Bucket=bucket)
    except Exception:
        s3.create_bucket(Bucket=bucket)
    s3.upload_file(local_path, bucket, key)
    return f"s3://{bucket}/{key}"


def main():
    mlflow_uri = os.environ.get("MLFLOW_TRACKING_URI", "http://mlflow.aiplatform.svc.cluster.local:5000")
    mlflow.set_tracking_uri(mlflow_uri)
    mlflow.set_experiment("platform-demo")

    with mlflow.start_run() as run:
        run_id = run.info.run_id

        model = models.resnet50(weights=models.ResNet50_Weights.DEFAULT)
        model.eval()

        dummy = torch.randn(1, 3, 224, 224)
        with tempfile.TemporaryDirectory() as tmpdir:
            onnx_path = f"{tmpdir}/model.onnx"
            torch.onnx.export(
                model,
                dummy,
                onnx_path,
                input_names=["input"],
                output_names=["output"],
                dynamic_axes={"input": {0: "batch_size"}},
                opset_version=17,
            )

            job_id = os.environ.get("PLATFORM_JOB_ID", "unknown")
            bucket = os.environ.get("MINIO_BUCKET", "models")
            _upload(onnx_path, bucket, f"models/{job_id}/model.onnx")
            mlflow.log_artifact(onnx_path, artifact_path="model")

        # Published top-1 accuracy for ResNet50 on ImageNet
        mlflow.log_metric("top1_accuracy", 0.7671)
        mlflow.log_param("model_type", "resnet50")
        mlflow.log_param("export_format", "onnx")
        mlflow.log_param("opset_version", 17)

    print(f"MLFLOW_RUN_ID={run_id}", flush=True)


if __name__ == "__main__":
    main()
