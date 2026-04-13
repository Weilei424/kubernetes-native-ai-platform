# sdk/python/platformclient/specs.py
from __future__ import annotations
from dataclasses import dataclass, field, asdict


@dataclass
class RuntimeSpec:
    image: str
    command: list[str] = field(default_factory=list)
    args: list[str] = field(default_factory=list)
    env: dict[str, str] = field(default_factory=dict)


@dataclass
class ResourceSpec:
    num_workers: int = 1
    worker_cpu: str = "1"
    worker_memory: str = "2Gi"
    head_cpu: str = "1"
    head_memory: str = "2Gi"


@dataclass
class TrainingSpec:
    """Spec for POST /v1/jobs — mirrors jobs.JobSubmitRequest."""
    name: str
    project_id: str
    runtime: RuntimeSpec
    resources: ResourceSpec = field(default_factory=ResourceSpec)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class DeploymentSpec:
    """Spec for POST /v1/deployments — mirrors deployments.CreateDeploymentRequest."""
    name: str
    model_name: str
    model_version: int
    namespace: str = "aiplatform"
    replicas: int = 1

    def to_dict(self) -> dict:
        return asdict(self)
