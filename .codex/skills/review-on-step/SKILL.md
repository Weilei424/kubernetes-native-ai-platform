---
name: review-on-step
description: Use when the user explicitly asks to review a named project phase, step, milestone, or similar planned slice of work. Resolve the project's planning docs, initialize missing planning docs if needed, then produce a reviewer-first assessment with findings, gaps, and verification coverage.
---

# Review On Step

## Overview

Use this skill only for explicit requests to review a named or numbered project step, phase, milestone, or equivalent planning unit.

This is a reviewer workflow, not a planning or implementation workflow.

## Trigger

Activate this skill only when the user explicitly asks for a review of a step or phase, for example:

- `review step 3`
- `review phase 2`
- `review milestone 4`
- `code review for phase 4`

Do not activate it for loose requests like:

- `what should we do next`
- `plan phase 3`
- `implement step 2`

## Workflow

### 1. Normalize the target

Treat `step`, `phase`, `milestone`, and similar planning labels as the same target by default unless the project documents distinguish them materially.

If the project clearly uses multiple labels with conflicting meanings, stop and clarify with the user.

### 2. Resolve project review context

Always check for project instructions first:

- `AGENTS.md`
- `CLAUDE.md`
- `GEMINI.md`
- `.codex/project-instructions.md`

Then resolve the core planning docs:

- `IMPLEMENTATION_PLAN.md`
- `ARCHITECTURE_NOTES.md`
- `BACKLOG.md`

For each core planning doc, use this path policy:

1. Check the project root first.
2. Check `docs/planning/` second.
3. If neither exists, create it in `docs/planning/`.
4. Do not relocate an existing root-level document.

This keeps existing projects stable while giving new projects a conventional home for planning docs.

If matching specs or plans exist under project planning folders, review them too. Check the existing project convention first. Typical locations include:

- `docs/planning/`
- `docs/specs/`
- `docs/superpowers/plans/`
- `docs/superpowers/specs/`

### 3. Initialize missing planning docs when needed

If one or more core planning docs are missing, initialize them before continuing the review.

Create `docs/planning/` if needed, then create only the missing files there with minimal starter structure.

Use these starter headings:

- `IMPLEMENTATION_PLAN.md`
  - `# Implementation Plan`
  - `## Summary`
  - `## Current State`
  - `## Phase Plan`
  - `## Definition of Done`
- `ARCHITECTURE_NOTES.md`
  - `# Architecture Notes`
  - `## System Overview`
  - `## Responsibilities`
  - `## Data Flow`
  - `## Risks and Constraints`
- `BACKLOG.md`
  - `# Backlog`
  - `## In Progress`
  - `## Next Up`
  - `## Later`
  - `## Risks / Follow-Ups`

State clearly when the review is proceeding against newly initialized planning docs instead of established project docs. Treat conclusions as lower-confidence in that case.

### 4. Read review context

Always read:

- any project instruction files that exist
- the resolved planning docs
- relevant code and tests for the requested target
- any matching spec or plan docs in the project's existing planning folders

### 5. Evaluate the implementation

Review for:

- correctness and regression risk
- architecture drift from intended responsibilities
- schema and persistence consistency
- ACL coverage
- operation ordering and idempotency guarantees
- Redis and Kafka responsibility boundaries
- missing tests and verification gaps

Adapt the architecture-specific checks to the project at hand. Do not assume Spring Boot, Redis, Kafka, or any specific stack unless the project docs or codebase establish them.

### 6. Produce reviewer output

Return:

- findings first, ordered by severity
- file references when possible
- open questions or assumptions
- a short summary of coverage and residual risk

Do not start implementation unless the user explicitly asks Codex to make changes.

## Boundaries

Do not use this skill to:

- plan a new phase
- write an implementation plan
- start coding automatically
- infer a review request from approximate wording
