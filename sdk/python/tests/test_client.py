# sdk/python/tests/test_client.py
import pytest
import responses as resp_mock
from platformclient.client import PlatformClient


BASE = "http://localhost:8080"


@resp_mock.activate
def test_submit_job_posts_and_sets_auth():
    resp_mock.add(resp_mock.POST, f"{BASE}/v1/jobs",
                  json={"job_id": "j1", "run_id": "r1"}, status=202)
    c = PlatformClient(host=BASE, token="tok")
    out = c.submit_job({"name": "t"})
    assert out["job_id"] == "j1"
    assert resp_mock.calls[0].request.headers["Authorization"] == "Bearer tok"


@resp_mock.activate
def test_get_job_returns_job_and_run():
    resp_mock.add(resp_mock.GET, f"{BASE}/v1/jobs/j1",
                  json={"job": {"id": "j1", "status": "RUNNING"}, "run": {"id": "r1"}})
    c = PlatformClient(host=BASE, token="tok")
    out = c.get_job("j1")
    assert out["job"]["status"] == "RUNNING"


@resp_mock.activate
def test_register_model():
    resp_mock.add(resp_mock.POST, f"{BASE}/v1/models",
                  json={"version": {"version_number": 1, "status": "candidate"}}, status=201)
    c = PlatformClient(host=BASE, token="tok")
    out = c.register_model(run_id="r1", model_name="resnet50")
    assert out["version"]["version_number"] == 1


@resp_mock.activate
def test_promote_model():
    resp_mock.add(resp_mock.POST, f"{BASE}/v1/models/resnet50/versions/1/promote",
                  json={"status": "ok"})
    c = PlatformClient(host=BASE, token="tok")
    out = c.promote_model("resnet50", 1, "production")
    assert out["status"] == "ok"


@resp_mock.activate
def test_create_deployment():
    resp_mock.add(resp_mock.POST, f"{BASE}/v1/deployments",
                  json={"deployment": {"id": "d1", "status": "pending"}}, status=201)
    c = PlatformClient(host=BASE, token="tok")
    out = c.create_deployment({"name": "resnet-prod"})
    assert out["deployment"]["id"] == "d1"


@resp_mock.activate
def test_http_error_raises():
    resp_mock.add(resp_mock.GET, f"{BASE}/v1/jobs/missing",
                  json={"error": "not found"}, status=404)
    c = PlatformClient(host=BASE, token="tok")
    with pytest.raises(Exception):
        c.get_job("missing")
