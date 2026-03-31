---
name: plan-on-step
description: Use when the user explicitly says `plan on step x` or `plan on phase x`. Do not activate on loose planning language.
---

# Plan On Step

## Overview

Planning-only workflow for any software project. Discover repo context, run setup if required docs are missing, run the design workflow, update planning artifacts, and stop before any implementation begins.

## Doc Location Discovery

**Always at root** (framework conventions, never moved):
- `CLAUDE.md` / `AGENTS.md` / `GEMINI.md`

**Discoverable docs** (`BACKLOG.md`, `IMPLEMENTATION_PLAN.md`, `ARCHITECTURE_NOTES.md`):

Apply this lookup order for each doc, and record the resolved path for use throughout the rest of the workflow:

1. Check root (e.g. `./BACKLOG.md`)
2. Check `docs/planning/` (e.g. `docs/planning/BACKLOG.md`)
3. If found in either location → use that path; do not move or duplicate it
4. If not found → doc is missing; handle in Step 0

**If docs are split across locations** (some at root, some in `docs/planning/`): follow the majority. If evenly split, ask the user which location to treat as canonical before proceeding.

**For new docs** (created in Step 0): default to `docs/planning/` to keep root clean. Match the location of existing discoverable docs if any are already present.

---

## Workflow

### 0. Setup / init

Run doc location discovery (above) before doing anything else.

Scan for the required planning docs using the resolved paths:

| Doc | Purpose | Required |
|-----|---------|----------|
| `CLAUDE.md` / `AGENTS.md` / `GEMINI.md` | Project instructions | Required |
| `BACKLOG.md` | Phase/task checklist | Required |
| `IMPLEMENTATION_PLAN.md` | Phase roadmap | Required |
| `ARCHITECTURE_NOTES.md` | Architecture reference | Recommended |

**If one or more required docs are missing:**

1. List the missing files (with their expected paths) to the user.
2. Ask whether to generate them now before proceeding.
3. If yes, create them in `docs/planning/` (or the project's canonical location if already established) using the stubs at the bottom of this skill. Adapt to the project's existing naming and style conventions.
4. After creating the missing files, continue with Step 1.

**If only `ARCHITECTURE_NOTES.md` is absent** and no architecture decisions are needed for this step, skip creation silently. Create it in Step 6 only if planning produces architecture decisions worth recording.

**If all required docs are present**, skip this step and proceed to Step 1.

### 1. Confirm the trigger is exact

Activate only for:
- `plan on step x`
- `plan on phase x`

Do not activate for loose forms like `plan the next step`, `planning for ACL`, or `phase 3 planning`. If unclear, ask the user to clarify.

### 2. Normalize `step` and `phase`

Treat `step` and `phase` as aliases by default. When reading repo docs:
- Match the requested number or name directly first.
- If docs use only one term, map the other to it.
- If both terms appear with conflicting meanings, stop and clarify before planning.

### 3. Read repo context first

Before proposing any design, read (using the resolved paths from discovery):
- The project instructions file (`CLAUDE.md`, `AGENTS.md`, or `GEMINI.md`)
- `IMPLEMENTATION_PLAN.md`
- `ARCHITECTURE_NOTES.md`
- `BACKLOG.md`
- Existing matching files in the project's spec/plans directories (check `docs/superpowers/specs/`, `docs/planning/`, or wherever specs live)
- Relevant source code and tests for the requested step or phase

Use the current codebase state, not only the backlog wording, to define the planning boundary.

### 4. Invoke brainstorming

After reading repo context, use the `brainstorming` skill for the requested planning target.

- Treat the step or phase as the planning target.
- Ask one clarifying question at a time when needed.
- Propose 2–3 approaches with a recommendation.
- Present the design in sections and get approval.
- Keep the design bounded to the requested step or phase.
- Do not start implementation during brainstorming.

### 5. Reuse or create planning artifacts

After design approval:
- Update an existing spec if one matches; otherwise create a new one in the project's spec directory (default: `docs/superpowers/specs/`).
- Update an existing plan if one matches; otherwise create a new one in the project's plans directory (default: `docs/superpowers/plans/`).
- Prefer updating over creating duplicates.
- Use filenames specific to the target and date.

### 6. Update project planning docs

Update `BACKLOG.md` at its resolved path as part of the planning workflow.

- For numbered phases use `### Phase N Execution Checklist` format.
- For other targets add or update the closest equivalent checklist without disrupting existing phase sections.

Update `IMPLEMENTATION_PLAN.md` and `ARCHITECTURE_NOTES.md` only when planning materially changes the phase map, architecture defaults, or system responsibilities. Do not churn those docs for local target-level details.

If `ARCHITECTURE_NOTES.md` does not exist and this step produces architecture decisions worth recording, create it at the canonical planning location using the stub below.

### 7. Invoke writing-plans

After design approval, use the `writing-plans` skill to produce the implementation plan.

The plan should:
- match the approved design
- use exact file paths
- be broken into small execution tasks
- include tests and verification commands
- stay bounded to the requested step or phase

### 8. Stop for execution confirmation

After updating spec, plan, and backlog, stop and ask whether to begin implementation.

Do not start coding, enter implementation mode, or run execution skills automatically.

The handoff must identify:
- the updated spec path
- the updated plan path
- whether `BACKLOG.md` was updated (and its path)
- that implementation is waiting on user confirmation

---

## Boundaries

Keep this skill planning-only. Do not use it to implement code, execute plan tasks, infer planning intent from approximate wording, or rewrite unrelated planning docs without a concrete architectural reason.

---

## Doc Stubs (for Step 0 init)

Use these as starting points when creating missing files. Adapt headings and sections to match any existing conventions in the project.

### BACKLOG.md stub

```markdown
# Backlog

## Status Legend
- [ ] Not started
- [x] Complete
- [~] In progress

<!-- Add phase checklists below as phases are planned. -->
```

### IMPLEMENTATION_PLAN.md stub

```markdown
# Implementation Plan

## Overview

<!-- Brief description of the project and its delivery strategy. -->

## Phases

<!-- Each phase should have a goal, key deliverables, and success criteria. -->

### Phase 1 — [Name]

**Goal:** ...
**Deliverables:** ...
**Success criteria:** ...
```

### ARCHITECTURE_NOTES.md stub

```markdown
# Architecture Notes

## Stack

<!-- Languages, frameworks, databases, infrastructure. -->

## Key Decisions

<!-- Record significant architecture choices and the reasoning behind them. -->

## Component Responsibilities

<!-- What each major component owns. -->

## Design Constraints

<!-- Non-negotiable constraints that shape the architecture. -->
```
