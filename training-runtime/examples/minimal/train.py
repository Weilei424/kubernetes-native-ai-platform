# training-runtime/examples/minimal/train.py
"""
Minimal synthetic MLP trainer for platform E2E demo (DEMO_MODE=fast).
Trains a 2-layer MLP on random data for 5 epochs.
Prints MLFLOW_RUN_ID=<id> to stdout on completion for operator pickup.
"""
import os
import tempfile

import boto3
import mlflow
import ray
import torch
import torch.nn as nn


@ray.remote
def train_shard(rank: int, epochs: int = 5):
    """One training shard — runs on each Ray worker."""
    torch.manual_seed(42 + rank)
    model = nn.Sequential(nn.Linear(64, 32), nn.ReLU(), nn.Linear(32, 10))
    opt = torch.optim.SGD(model.parameters(), lr=0.01)
    criterion = nn.CrossEntropyLoss()
    losses = []
    for _ in range(epochs):
        x = torch.randn(32, 64)
        y = torch.randint(0, 10, (32,))
        opt.zero_grad()
        loss = criterion(model(x), y)
        loss.backward()
        opt.step()
        losses.append(loss.item())
    # Return state_dict as plain dict (serialisable across Ray)
    return {k: v.tolist() for k, v in model.state_dict().items()}, losses


def _upload(local_path: str, bucket: str, key: str) -> str:
    s3 = boto3.client(
        "s3",
        endpoint_url=os.environ.get("MINIO_ENDPOINT", "http://minio.aiplatform.svc.cluster.local:9000"),
        aws_access_key_id=os.environ.get("MINIO_ACCESS_KEY", "minio"),
        aws_secret_access_key=os.environ.get("MINIO_SECRET_KEY", "minio123"),
    )
    # Create bucket if needed
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

    ray.init()

    with mlflow.start_run() as run:
        run_id = run.info.run_id

        num_workers = int(os.environ.get("NUM_WORKERS", "2"))
        futures = [train_shard.remote(rank=i) for i in range(num_workers)]
        results = ray.get(futures)

        # Worker 0's model is canonical; log average losses
        state_dict_lists, losses = results[0]
        for epoch, loss in enumerate(losses):
            mlflow.log_metric("train_loss", loss, step=epoch)

        # Reconstruct model for ONNX export
        model = nn.Sequential(nn.Linear(64, 32), nn.ReLU(), nn.Linear(32, 10))
        model.load_state_dict({k: torch.tensor(v) for k, v in state_dict_lists.items()})
        model.eval()

        with tempfile.TemporaryDirectory() as tmpdir:
            onnx_path = f"{tmpdir}/model.onnx"
            torch.onnx.export(
                model,
                torch.randn(1, 64),
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

        mlflow.log_param("model_type", "minimal_mlp")
        mlflow.log_param("num_workers", num_workers)

    ray.shutdown()

    # Signal mlflow run ID to the platform operator (reads head pod logs)
    print(f"MLFLOW_RUN_ID={run_id}", flush=True)


if __name__ == "__main__":
    main()
