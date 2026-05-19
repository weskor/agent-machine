# Introduce local SQLite orchestration state

## Status

Accepted for future implementation planning. CAG-59 records the decision and behavior contract only; it does not implement SQLite storage.

## Context

Pi Symphony currently coordinates Linear, GitHub, workspaces, JSON artifacts, comments, and daemon lanes with state spread across external systems and local files. That file-oriented model is easy to inspect, but orchestration decisions become harder to recover after daemon restarts, deleted workspaces, stale artifacts, interrupted handoffs, or partial merge/review failures.

CAG-49 needs a durable local state contract before implementation can be split safely. The current observable runner behavior is documented in `docs/specs/harness-behavior.md`; the new SQLite contract is documented in `docs/specs/sqlite-orchestration-state.md`.

## Decision

Introduce a local SQLite orchestration state store for future Pi Symphony runner implementations.

SQLite will be the local source of truth for runner decisions such as attempts, issue/PR mapping, review classification, feedback processing, merge eligibility, cleanup, leases, heartbeat recovery, retry decisions, and terminal outcomes. Linear and GitHub remain external systems of record for their own resources. Workspace JSON/Markdown files remain audit and evidence exports and may seed migration/backfill, but they should not be the primary coordination mechanism after SQLite adoption.

## Consequences

- The runner can recover decisions across daemon restarts and missing/stale workspace artifacts more deterministically.
- Implementation tickets must add schema versioning, migrations, reconciliation, and fail-closed behavior before SQLite-backed decisions are used.
- Status and repair behavior can become more explainable because blockers and reconciliation-needed states have durable local records.
- The design adds local database complexity and migration risk compared with file-only state.
- Operators lose some simplicity of inspecting one JSON file per attempt, so future implementations should keep evidence exports readable and deterministic.
- SQLite failures become orchestration blockers: when the database cannot be opened, migrated, or locked safely, the runner must not claim issues, move workflow state, mutate workspaces, clean up, retry, or merge.

## Alternatives considered

- Continue file-only state: simpler and transparent, but weak for cross-lane coordination, reconciliation, and recovery when workspaces or artifacts are missing or stale.
- Use Linear/GitHub only: avoids a local database, but forces orchestration-only concepts such as leases, retry decisions, feedback hashes, and cleanup status into systems that are not authoritative for local runner decisions.
- Use a server database: better for multi-host coordination, but exceeds the current local runner scope and adds operational dependencies that CAG-49 does not require.
