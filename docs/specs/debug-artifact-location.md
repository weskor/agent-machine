# Debug raw artifact location and workspace cleanliness

## Purpose

Volatile raw agent output traces are currently written under the issue workspace.
This is useful for diagnosis but should not make a completed workspace appear
"dirty" during cleanup. This spec defines where capped raw traces are stored and
how this affects cleanup safety.

## Scope

- Applies to `captureAgentOutput` and its raw trace writer used for implementation
  and review phases.
- Does **not** change the required location for
  `.pi-symphony-run.json` or `.pi-symphony-evaluation.json` in this slice.

## Contract

### 1) Default trace location

When `PI_SYMPHONY_DEBUG_RAW_OUTPUT=1`:

- The implementation phase writes to
  `<workspace-root>/.symphony/debug/<issue>/implementation-raw.log`.
- The review phase writes to
  `<workspace-root>/.symphony/debug/<issue>/review-raw.log`.
- `<issue>` is derived from the issue workspace basename (`CAG-XX`).
- The directory is created with secure permissions and the write is capped using
  `PI_SYMPHONY_DEBUG_RAW_OUTPUT_LIMIT_BYTES` (defaulting to
  `1024*1024`) exactly as today.

### 2) Error handling and observability

- If directory creation or write fails, the run continues (same non-fatal behavior as
  today): log a warning and proceed with execution.
- The primary daemon log must always include the resolved artifact path when output
  is written.

### 3) Compatibility and migration

- Legacy paths under `<workspace>/.pi-symphony-debug/*-raw.log` may exist from
  earlier runs.
- During this migration, those legacy paths must not prevent cleanup.

### 4) Workspace change detection

- `workspaceHasChanges` / `internal/workspace.HasChanges` continues to ignore only
  expected evidence files required by existing behavior (`.pi-symphony-run.json`,
  `.pi-symphony-evaluation.json`, `.pi-symphony-prompt.md`, `.pi-symphony-review-prompt.md`).
- Operator-side review subagents can leave a top-level `false` scratch marker
  when their output file option is disabled. That marker is not domain evidence
  and must not block cleanup. Only an exact top-level regular file named `false`
  is ignored, and only when it is zero bytes or bounded reviewer-output text with
  the known subagent scratch signature. Non-matching non-empty files, nested
  paths, symlinks, and all other untracked files remain dirty.
- Since new raw traces are not written under the workspace, they do not affect
  cleanup readiness.

## Acceptance criteria

- With `PI_SYMPHONY_DEBUG_RAW_OUTPUT=1`, a successful run creates implementation
  and review debug artifacts under `.symphony/debug/<issue>/` at workspace-root.
- The primary logs include the artifact path and truncation note when capped.
- Completed workspaces containing only expected evidence files plus old
  `implementation-raw.log`/`review-raw.log` in legacy locations can still be cleaned
  by the cleanup lane.
- Completed workspaces containing only expected evidence files plus a matching
  top-level `false` scratch marker can still be cleaned by the cleanup lane.
- No existing run/evaluation artifact behavior is changed in this slice.

