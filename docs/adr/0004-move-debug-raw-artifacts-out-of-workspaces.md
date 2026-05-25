# Move debug raw artifacts out of workspaces

## Status

Accepted

## Context

Agent Machine currently writes capped raw implementation/review output to
`.pi-symphony-debug/*-raw.log` inside each issue workspace when
`AM_DEBUG_RAW_OUTPUT=1`.

Because cleanup and merge safety checks still use `git status`-style workspace
cleanliness, those files can keep a completed workspace "dirty" and block deletion.
This creates manual cleanup debt and weakens the operator workflow for the
cleanup lane.

The repo is also pursuing the broader artifact boundary principle from
`docs/specs/sqlite-orchestration-state.md`: run records and evaluation artifacts
are evidence exports and not coordination state, so moving volatile debug traces
out of the workspace is consistent with durable evidence handling.

## Decision

Store raw agent debug artifacts outside issue workspace directories, while keeping
run/evaluation artifacts in their existing workspace locations.

Specifically:

- Raw implementation/review traces are written to:
  - `<workspace-root>/.symphony/debug/<issue>/implementation-raw.log`
  - `<workspace-root>/.symphony/debug/<issue>/review-raw.log`
- `<workspace-root>` means the configured daemon/workspace root (for example
  `.symphony/workspaces`).
- The `<issue>` segment is derived from the workspace basename, e.g.
  `CAG-86`.
- `captureAgentOutput` (or its wrapper) must accept/resolve an artifact export
  path that is outside the workspace; by default it should use the pattern above.
- Raw artifact writes keep the existing truncation/capping behavior and non-fatal
  failure semantics (log warning, continue run).
- The primary run log must still print the artifact pointer path.

## Consequences

- Cleanup and merge safety checks can treat completed workspaces as empty unless they
  contain real diff/untracked evidence.
- Debug artifacts become auditable and persistent beyond workspace lifecycle.
- Existing scripts and tests that inspect `.pi-symphony-debug` inside workspaces
  must be updated to the new path convention.
- Legacy `.pi-symphony-debug` directories left from prior runs should not be
  treated as a hard block for cleanup during migration.

## Alternatives considered

- Keep debug files in-workspace and expand ignored paths to include
  `.pi-symphony-debug`.
  This preserves local file locality but leaves workspace cleanup semantics coupled
  to the debug collector and weakens workspace-signal clarity.
- Always keep all evidence out-of-workspace (including `.pi-symphony-run.json` and
  `.pi-symphony-evaluation.json`).
  This is a larger behavior migration than needed for the current cleanup leak and
  would be addressed only after DB-backed decision classes are authoritative.

