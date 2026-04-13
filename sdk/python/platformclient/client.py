# sdk/python/platformclient/client.py
from __future__ import annotations
import os
from typing import Any
import requests


class PlatformClient:
    """Thin HTTP client for the kubernetes-native AI/ML platform.

    Args:
        host: Base URL of the control plane.
              Defaults to PLATFORMCTL_HOST env var or http://localhost:8080.
        token: Bearer token.
               Defaults to PLATFORMCTL_TOKEN env var.
    """

    def __init__(self, host: str | None = None, token: str | None = None) -> None:
        self._host = (host or os.getenv("PLATFORMCTL_HOST", "http://localhost:8080")).rstrip("/")
        self._token = token or os.getenv("PLATFORMCTL_TOKEN", "")

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self._token}"} if self._token else {}

    def _url(self, path: str) -> str:
        return f"{self._host}{path}"

    def _raise(self, resp: requests.Response) -> None:
        if resp.status_code >= 400:
            raise requests.HTTPError(f"HTTP {resp.status_code}: {resp.text}", response=resp)

    # ── training jobs ──────────────────────────────────────────────────────

    def submit_job(self, spec: dict[str, Any]) -> dict[str, Any]:
        """POST /v1/jobs → {"job_id": ..., "run_id": ...}"""
        resp = requests.post(self._url("/v1/jobs"), json=spec, headers=self._headers())
        self._raise(resp)
        return resp.json()

    def get_job(self, job_id: str) -> dict[str, Any]:
        """GET /v1/jobs/:id → {"job": {...}, "run": {...}}"""
        resp = requests.get(self._url(f"/v1/jobs/{job_id}"), headers=self._headers())
        self._raise(resp)
        return resp.json()

    def list_jobs(self) -> list[dict[str, Any]]:
        """GET /v1/jobs → list of job dicts"""
        resp = requests.get(self._url("/v1/jobs"), headers=self._headers())
        self._raise(resp)
        return resp.json().get("jobs", [])

    # ── models ────────────────────────────────────────────────────────────

    def register_model(self, run_id: str, model_name: str, artifact_path: str = "") -> dict[str, Any]:
        """POST /v1/models → {"version": {...}}"""
        body: dict[str, Any] = {"run_id": run_id, "model_name": model_name}
        if artifact_path:
            body["artifact_path"] = artifact_path
        resp = requests.post(self._url("/v1/models"), json=body, headers=self._headers())
        self._raise(resp)
        return resp.json()

    def promote_model(self, model_name: str, version: int, alias: str) -> dict[str, Any]:
        """POST /v1/models/:name/versions/:version/promote"""
        resp = requests.post(
            self._url(f"/v1/models/{model_name}/versions/{version}/promote"),
            json={"alias": alias},
            headers=self._headers(),
        )
        self._raise(resp)
        return resp.json()

    def get_model(self, model_name: str) -> dict[str, Any]:
        """GET /v1/models/:name → {"model": {...}, "versions": [...]}"""
        resp = requests.get(self._url(f"/v1/models/{model_name}"), headers=self._headers())
        self._raise(resp)
        return resp.json()

    # ── deployments ───────────────────────────────────────────────────────

    def create_deployment(self, spec: dict[str, Any]) -> dict[str, Any]:
        """POST /v1/deployments → {"deployment": {...}}"""
        resp = requests.post(self._url("/v1/deployments"), json=spec, headers=self._headers())
        self._raise(resp)
        return resp.json()

    def get_deployment(self, deployment_id: str) -> dict[str, Any]:
        """GET /v1/deployments/:id → {"deployment": {...}}"""
        resp = requests.get(self._url(f"/v1/deployments/{deployment_id}"), headers=self._headers())
        self._raise(resp)
        return resp.json()
