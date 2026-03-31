# AGENTS.md

This file provides guidance to Codex when working in this repository.

## Role

- **Claude** is the primary implementor for this project.
- **Codex** is the reviewer by default.
- Unless the user explicitly asks Codex to write code, operate in a review-first mode over Claude's implementation.

## Reviewer Workflow

When reviewing implementation work:

1. Start with the planning docs and repository instructions before judging code.
2. Review against the intended phase or milestone, not against speculative future work.
3. Prioritize findings about correctness, regression risk, architecture drift, missing tests, and lifecycle violations.
4. Call out concrete file references and residual risks.
5. Do not weaken the platform lifecycle to make an implementation seem acceptable.

## Project Overview

This repository is a Kubernetes-native AI/ML platform built as a portfolio project focused on AI infrastructure and ML platform engineering. The required lifecycle is:

`train -> track -> register -> promote -> deploy -> infer`

Do not bypass or collapse this flow in either implementation or review guidance.

## Core Stack

- Control plane API: Go
- Metadata store: PostgreSQL
- Event bus: Kafka
- Artifact store: MinIO locally, S3 in cloud-oriented variants
- Training runtime: Ray + KubeRay
- Experiment tracking and registry: MLflow
- Serving: Triton Inference Server
- Observability: Prometheus + Grafana
- Secrets: Vault
- Packaging: Helm + Terraform
- Optional GitOps: ArgoCD

## Source Of Truth

Read these first when they exist:

- `CLAUDE.md`
- `docs/planning/IMPLEMENTATION_PLAN.md`
- `docs/planning/ARCHITECTURE_NOTES.md`
- `docs/planning/BACKLOG.md`

Use the planning docs to determine intended scope, sequencing, and acceptance criteria for a phase review.

## Review Priorities

Review implementations for:

- correctness and behavioral regressions
- phase alignment and scope discipline
- control-plane ownership staying in Go
- MLflow registry integration remaining mandatory
- Triton remaining the serving layer
- metadata consistency across API, persistence, and reconciliation flows
- idempotency and operation ordering
- observability and correlation IDs where new flows are introduced
- missing unit, integration, and end-to-end coverage

## Phase Order

Work is expected to progress in this order:

1. Phase 0: repo bootstrap, local infra stack, control plane skeleton, health endpoints, auth skeleton, base DB schema
2. Phase 1: training job API, metadata persistence, queueing and status model, RayJob submission, run reconciliation
3. Phase 2: MLflow run linkage, model registration, promotion workflow, alias-based resolution
4. Phase 3: deployment API, Triton model packaging, deployment controller, endpoint and status tracking
5. Phase 4: dashboards, retries, failure handling, deployment revisions, logs and event visibility
6. Phase 5: CLI polish, examples, runbooks, scripted demo, docs cleanup

Flag architecture drift or premature work that skips required earlier phases.

## Design Guardrails

- The control plane stays in Go.
- Triton is the inference serving runtime.
- MLflow model registry is required.
- The v1 scheduler handles admission, quota, FIFO, and placement hints only.
- Local development is CPU-first.
- Auth in v1 is simple token-based auth.
- The SDK stays thin.
- Secrets must not be committed.

## Verification Expectations

Expect coverage for:

- unit tests for validation, state transitions, scheduler logic, promotion rules, and rendering
- integration tests for API to PostgreSQL persistence and lifecycle orchestration
- end-to-end coverage for the full lifecycle path
- failure-path coverage for invalid specs, quota failures, missing artifacts, and serving readiness failures

If tests are missing, say so explicitly in the review.

## Skills

This repository includes Codex-local skills under `.codex/skills/`, including:

- `review-on-step`
- `architecture-diagram-creator`

Prefer the review-oriented workflow for implementation checkpoints unless the user asks for direct coding work.
