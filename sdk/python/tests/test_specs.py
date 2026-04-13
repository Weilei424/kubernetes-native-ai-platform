# sdk/python/tests/test_specs.py
from platformclient.specs import DeploymentSpec, ResourceSpec, RuntimeSpec, TrainingSpec


def test_training_spec_to_dict_has_required_keys():
    spec = TrainingSpec(
        name="resnet-train", project_id="proj-1",
        runtime=RuntimeSpec(image="ghcr.io/example/trainer:latest"),
    )
    d = spec.to_dict()
    assert d["name"] == "resnet-train"
    assert d["project_id"] == "proj-1"
    assert d["runtime"]["image"] == "ghcr.io/example/trainer:latest"
    assert d["resources"]["num_workers"] == 1


def test_training_spec_runtime_defaults():
    spec = TrainingSpec(name="t", project_id="p", runtime=RuntimeSpec(image="img"))
    d = spec.to_dict()
    assert d["runtime"]["command"] == []
    assert d["runtime"]["args"] == []
    assert d["runtime"]["env"] == {}
    assert d["resources"]["worker_cpu"] == "1"


def test_deployment_spec_to_dict():
    spec = DeploymentSpec(name="resnet-prod", model_name="resnet50", model_version=1, replicas=2)
    d = spec.to_dict()
    assert d["model_name"] == "resnet50"
    assert d["model_version"] == 1
    assert d["replicas"] == 2
    assert d["namespace"] == "aiplatform"
