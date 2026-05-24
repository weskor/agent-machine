# SQLite Orchestration State Contract

This spec defines the intended behavior contract for CAG-49: moving Pi Symphony runner orchestration state to a local SQLite store. It is a planning contract only; CAG-59 does not implement SQLite storage or change current runner behavior. Current observable behavior remains governed by [Harness Behavior Spec](./harness-behavior.md) until implementation tickets deliberately update it.

## Goals

- Make orchestration decisions durable across daemon restarts, workspace deletion, stale local artifacts, and partial external-system failures.
- Preserve Linear and GitHub ownership boundaries while giving the runner one local source for scheduling, retry, handoff, merge, and cleanup decisions.
- Keep JSON and Markdown workspace artifacts useful as audit/evidence exports rather than primary coordination state.

## Source of truth model

- SQLite is the source of truth for runner orchestration decisions made by Pi Symphony: which issue attempt is active, which PR belongs to it, whether review passed, whether feedback is new, whether merge is eligible, which cleanup action is pending, which lease is held, and what terminal outcome was reached.
- SQLite also owns durable worker tasks when runner responsibilities are split
  across scheduler, implementation, review, handoff, merge, Linear status,
  reconciliation, and cleanup workers. Worker tasks describe which role should
  act next, but the worker must still re-read current-state rows and fresh
  external facts before mutating local or external state.
- Linear remains the external system of record for issue identity, issue workflow state, comments, assignee/ownership where configured, labels, priority, and Done/Needs Info/Human Review state transitions.
- GitHub remains the external system of record for PR identity, PR state, review decision, mergeability, checks, branch metadata, authorship, and merge result.
- Workspace JSON/Markdown files, including `.pi-symphony-run.json`, `.pi-symphony-evaluation.json`, PR bodies, and deterministic comments, are audit and evidence exports. They may be backfill inputs during migration or repair, but after SQLite adoption they must not silently override newer database state.
- SQLite current-state projections and event payloads carry explicit projection schema metadata. Run-artifact projection payloads include the SQLite projection schema version plus artifact schema version/source so status, repair, and future migrations can distinguish current exports from legacy unversioned artifacts.
- Backfill from unversioned `.pi-symphony-run.json` or `.pi-symphony-evaluation.json` treats those files as schema version 1 with legacy provenance; unsupported or malformed explicit schema versions are reconciliation failures rather than silently inferred state.
- When local SQLite state conflicts with Linear or GitHub, the runner must reconcile using explicit rules in this spec rather than assuming either side is always current.

## Authority matrix

This matrix is the implementation-ready precedence contract for later SQLite adoption tickets. It defines the authority for each runner decision without changing current runner behavior.

| Decision class | Authority | Fresh external facts required | Artifact role | Fail-closed behavior |
| --- | --- | --- | --- | --- |
| Candidate eligibility and claim | SQLite for active attempts, leases, retry blocks, and reconciliation blockers; fresh Linear for issue identity, current workflow state, labels, assignee/ownership, priority, and ticket text | Linear must be read before claim; SQLite lease must commit before state movement or workspace mutation | Historical run files may explain prior attempts only | Do not claim, move Linear, or create/mutate workspace when SQLite is unavailable, a lease cannot commit, Linear cannot verify eligibility, or artifacts/DB/external facts conflict |
| Linear state movement | SQLite for the local decision that a transition should be attempted; fresh Linear for allowed/current workflow state and transition result | Linear must be read immediately before transition and after transition attempt when possible | Deterministic comments and run records are evidence exports | Do not move Linear from stale DB state, stale artifacts, or unverified workflow state; record reconciliation-needed or fail closed |
| Workspace mutation | SQLite for attempt identity, expected path, branch, active workspace lease, and mutation phase | Git/GitHub facts may be refreshed when validating branch/PR invariants | Workspace files are outputs of the active attempt, not coordination locks | Do not clone, reset, checkout, delete, or write attempt artifacts unless the SQLite attempt and workspace lease are current |
| Retry decision | SQLite for retry count, budget, processed feedback hash, previous terminal/failure state, and chosen next action; fresh GitHub/Linear for new feedback or issue state | GitHub review/comment facts and Linear state must be current before deciding feedback-driven retries | Prior run/evaluation files may seed repair/backfill only | Do not retry from artifacts alone, duplicate a processed feedback retry, or retry a Done/Needs Info/Human Review issue without explicit operator/Linear evidence |
| PR handoff | SQLite for attempt identity, typed PR handoff intent/result, PR mapping, review classification, merge eligibility blocker state, and handoff decision; fresh GitHub for PR identity and branch/base/ownership; fresh Linear for target handoff state | GitHub PR details and Linear issue state must be current before handoff | PR body, deterministic comments, run/evaluation files are evidence exports | Do not report success or move to Human Review when PR validation, review classification, or DB commit is missing/ambiguous |
| Merge | SQLite for merge blockers, review classification, Symphony ownership expectations, processed feedback state, and merge lane lease; fresh GitHub for PR state, mergeability, checks, reviews, authorship, branch metadata, and merge result; fresh Linear for Done transition result | GitHub must be read immediately before merge; Linear must be read before Done transition | Artifacts may explain why a PR was created but cannot approve merge | Do not merge when SQLite is unavailable, ownership is unproven, current GitHub gates fail, Linear state is incompatible, or any reconciliation-needed blocker exists |
| Cleanup | SQLite for cleanup eligibility, active leases, terminal outcome, retained artifact pointers, deletion decision, and deletion result; fresh Linear/GitHub for Done/PR status when those facts gate cleanup | Linear/GitHub must be refreshed for cleanup that depends on issue Done or PR merged/closed state | Workspace artifacts are retained/debug exports and may be deleted only according to the cleanup decision | Do not delete workspaces or branches from stale artifacts, missing DB terminal state, active leases, open/unverified PRs, or reconciliation-needed rows |
| Repair and backfill | Operator input starts repair; SQLite records the repaired current state and synthetic migration/reconciliation events; fresh Linear/GitHub resolve identity conflicts | External facts must be refreshed before repair changes scheduling, retry, merge, or cleanup eligibility | Artifacts are compatibility inputs and evidence pointers | Do not silently repair by preferring artifacts over SQLite or external facts; unresolved conflicts remain reconciliation-needed |
| Status and diagnostics | SQLite current-state rows for decision status, event log for explanation, fresh Linear/GitHub only when the status mode explicitly refreshes external facts | Optional unless status claims current external state | Artifacts may be displayed as evidence/debug exports | Degraded read-only output is allowed, but status must not imply a decision was made when SQLite cannot be read or facts conflict |
| Worker dispatch | SQLite worker tasks for queued/claimed/completed role work, SQLite leases for mutation ownership, and current-state tables for policy decisions | Fresh Linear/GitHub facts are required by the worker before externally visible actions in that domain | Artifacts may be task payload references or evidence exports only | Do not run a worker task when task state, lease state, current-state rows, or required external facts conflict; mark reconciliation-needed or fail closed |

## Source precedence and disagreement handling

When SQLite, fresh external facts, artifacts, and operator input disagree, the runner must use this order:

1. **Safety and ownership invariants win first.** Any uncertain lease, workspace ownership, PR ownership, branch ownership, or schema/transaction state blocks destructive and externally visible actions.
2. **Fresh external systems own their domains.** Linear decides current issue workflow state and available transitions. GitHub decides current PR, branch, review, check, mergeability, authorship, and merge result facts.
3. **SQLite owns Pi Symphony decisions.** Candidate claims, local attempt status, retry/no-retry decisions, processed feedback hashes, review classifications, merge blockers, cleanup decisions, leases, heartbeats, and terminal outcomes come from current-state SQLite rows. For Done workspace cleanup, the durable issue-attempt run status is the cleanup-policy status when present; terminal/evaluation outcome rows remain diagnostic context and do not override a present run status. If that run status is non-terminal, a durable SQLite PR mapping plus fresh GitHub confirmation that the expected issue branch PR is merged may serve as terminal cleanup evidence; missing PR mappings, wrong branches, closed-unmerged PRs, or failed GitHub refreshes must remain reconciliation-needed.
4. **The SQLite event log explains committed local decisions.** It may support reconciliation or verified repair, but normal scheduling, retry, merge, and cleanup read current-state rows first.
5. **Workspace artifacts are evidence exports.** They can seed initial migration, compatibility repair, diagnostics, or missing export regeneration; they must not override newer SQLite rows or fresh external facts.
6. **Operator input is required for policy choices or unsafe ambiguity.** Operators may approve repair, answer Needs Info, or change project configuration, but operator input must be recorded in SQLite before it affects later automated decisions.

Disagreements that cannot be resolved by the authority above must create or keep an explicit reconciliation-needed state. The runner must not guess based on newest file mtime, branch name alone, stale comments, or partial logs.

## Artifact export boundary

After SQLite adoption, these files remain evidence/debug exports: `.pi-symphony-run.json`, `.pi-symphony-evaluation.json`, deterministic PR comments, deterministic Linear comments, PR bodies, capped debug logs under `<workspace-root>/.symphony/debug/`, validation summaries, and future status/report exports. They may contain hashes, pointers, summaries, URLs, and behavior-contract evidence suitable for audit.

Run-attempt writers that own a command-scoped Store must commit the attempt projection and derived evaluation classification to SQLite before writing `.pi-symphony-run.json` or `.pi-symphony-evaluation.json`. If that SQLite transaction cannot commit, the runner must fail closed for that terminal path and must not publish clean success artifacts. If a JSON export fails after the SQLite commit, the durable attempt outcome remains authoritative and the export failure is recorded as artifact evidence for diagnostics/reconciliation.

These file-based coordination mechanisms are deprecated or compatibility-only once the corresponding SQLite decision class is implemented:

- run records as the source for active attempt status, retry status, terminal outcome, cleanup eligibility, or PR mapping;
- evaluation artifacts as the source for review pass/fail, review classification, merge eligibility, or next action;
- workspace directory existence as proof that an attempt is active or inactive;
- branch names or workspace paths as sufficient issue/PR identity without Linear/GitHub anchors;
- on-disk run locks as durable ownership without a SQLite lease and heartbeat;
- deterministic comments or PR body text as the source for processed feedback, handoff state, or merge approval.

Compatibility readers may use these artifacts only for migration, repair, diagnostics, or export regeneration, and must label any artifact-derived state as backfilled or reconstructed until SQLite and fresh external facts verify it.

For run claims, the SQLite lease is authoritative once the mutating runner has opened a command-scoped Store. JSON run-lock files, when written, are compatibility/debug exports: they may be refreshed or removed after SQLite lease acquisition, but they must not block a healthy SQLite lease claim or release a healthy SQLite lease by themselves.

## Rollout phases

Later issues should wire one decision class at a time in this order, preserving behavior unless the ticket updates the behavior spec:

1. **Schema and event baseline:** create versioned current-state tables, event log, read-only status support, and fail-closed startup/schema checks.
2. **Candidate claim and leases:** move active attempt detection, claim leases, workspace mutation leases, heartbeats, stale lease handling, and reconciliation-needed blockers into SQLite.
3. **Attempt, PR mapping, and handoff:** persist attempt lifecycle, PR mapping, review classification, handoff decisions, terminal outcomes, and artifact export pointers.
4. **Retry and feedback:** persist processed feedback hashes, retry budgets, retry/no-retry decisions, and Needs Info/Human Review routing.
5. **Merge gates:** persist merge blockers and merge lane leases while continuing to refresh GitHub/Linear facts for every merge/Done decision.
6. **Cleanup:** persist cleanup eligibility, deletion decisions/results, retained artifact pointers, and workspace/branch cleanup blockers.
7. **Repair, backfill, and artifact compatibility removal:** convert artifacts into explicit backfill/repair inputs, remove file-based coordination paths, and keep exports as audit/debug outputs only.

Candidate reconciliation implementation note: when the SQLite state store exists, candidate selection, `--status`, and `--explain` read the latest durable issue attempt, PR mapping, retry decision, terminal outcome, cleanup row, and active run lease without implicit artifact fallback. Fresh Linear candidate facts and fresh GitHub open-PR facts are refreshed before externally visible or mutating decisions. Stale workspace artifacts may explain or seed explicit reconciliation/repair, but newer SQLite rows and current GitHub facts take precedence; unresolved DB/external/artifact conflicts are surfaced as reconciliation-needed only by modes that explicitly pass artifact evidence into reconciliation.

Cleanup implementation note: mutating workspace cleanup (`--cleanup-workspaces --apply`) fails closed when the SQLite state store cannot be opened. When SQLite is available, cleanup reads the latest durable issue attempt, PR mapping, terminal outcome, and cleanup row before choosing delete, dry-run, keep, failure, or reconciliation-needed. Missing durable attempt rows remain non-destructive reconciliation-needed keeps. Dirty and unsafe workspaces remain non-destructive keeps. Missing, insufficient, or stale run artifacts do not override terminal SQLite cleanup facts; artifacts remain retained/debug evidence for diagnostics and repair.

## State domains to persist

The SQLite model should persist enough data to make each daemon cycle idempotent and explainable:

- Issue attempts: Linear issue ID/key, attempt number, workspace path, branch name, base branch, timestamps, prompt inputs hash, validation command summary, and attempt lifecycle status.
- Issue/PR mapping: expected repository, expected branch, base branch, PR number/URL, PR head/base metadata, and whether the PR is Symphony-owned.
- PR handoff intents/results: issue/attempt identity, workspace path, expected branch, advisory Agent PR URL, bounded payload pointer, pending/completed/failed status, resulting PR URL, error text, and timestamps so inline and standalone handoff workers share one idempotent handoff boundary.
- Run status: pending, claimed, running, handoff, failed, needs-info, review-failed, merge-blocked, merged, cleaned, abandoned, or other terminal status needed by implementation tickets.
- Review status and classification: review command status, pass/fail marker, failure classification such as behavior/spec blocker versus missing-evidence-only, review output hash or artifact pointer, and merge eligibility result derived from review.
- Feedback hash/status: latest processed PR review/comment feedback hash, whether feedback has been incorporated into a retry, whether feedback is stale, and the next action chosen.
- Merge eligibility and blockers: current deterministic gate result with domain, subject, status, blocker codes, last checked external PR/check state, ownership invariants, and reason text suitable for status output or deterministic comments.
- Cleanup status: workspace existence, cleanup eligibility, cleanup decision, deterministic gate result, deletion result, retained artifact pointers, and reasons cleanup is blocked or reconciliation-needed.
- Locks and leases: issue-level claim lease, workspace mutation lease, merge-lane lease if needed, lease owner, acquisition time, expiry, renewal time, and release reason.
- Worker tasks: stable task key, role, status, priority, availability time, lease
  name, issue/attempt identity, compact payload, and timestamps so independently
  runnable workers can coordinate through SQLite instead of process-local state.
  Claimed tasks refresh their timestamp during long-running work when the owner
  renews the task lease. Failed tasks are requeued by applying role-specific
  retry backoff to `available_at` from the latest failed worker result.
- Daemon heartbeat: process identity, lane name, config path, cycle number,
  last successful cycle time, last error, whether recovery is required, and the
  active task key, role, lease name, and task start time when a lane/process is
  currently executing a claimed worker task.
- Retry decisions: retry count, retry budget state, reason for retry/no-retry, feedback or failure input that triggered the decision, and handoff to Needs Info or Human Review when retries stop.
- Terminal outcomes: Done/merged, Needs Info, Human Review handoff, abandoned due to missing external record, failed closed due to local state error, and cleaned workspace outcome.

## Durable orchestration event log

The SQLite store must include an append-only orchestration event log alongside the current-state tables. Current-state tables answer "what should the runner do next?"; the event log answers "what durable orchestration facts led here?" Implementation tickets may choose exact table and index names, but the schema must represent the fields and policy below without ambiguity.

### Event identity and ordering

Each event must have:

- A stable event identity generated by the writer. The identity must be unique for one project database and safe to reference from diagnostics, reconciliation output, and future artifact exports.
- A durable ordering key assigned by SQLite at insert time, such as an integer sequence. Ordering must be monotonic within one project database and must be the primary tie-breaker when events share the same timestamp.
- An event timestamp recorded as UTC. The timestamp records when the runner observed or made the orchestration decision, not when Linear or GitHub originally created an external object unless the event explicitly stores that external time inside its payload.
- Project identity sufficient to distinguish databases or configured config paths if future implementations store multiple projects together.
- Linear issue identity: issue ID when known, issue identifier/key when known, and enough nullable fields to record pre-claim candidate skips or degraded status events that may not yet have a complete local attempt row.
- Attempt number when the event is scoped to a run attempt. Attempt may be null for daemon, migration, status, or candidate-scan events that are not yet tied to one attempt.
- Source component, using a constrained vocabulary such as `work-lane`, `merge-lane`, `status`, `repair-artifacts`, `migration`, `reconciliation`, `github`, `linear`, `cleanup`, or `review`.
- Event type, using the taxonomy below. Event type names must be stable API-like values; display text belongs in payload or status output, not in the type name.

Writers must append events in the same SQLite transaction as the state mutation they explain when both occur locally. If the state mutation cannot commit, its explanatory event must not appear as committed. Observation-only events that do not change current state may commit independently, but must not imply that an orchestration decision was made.

### Payload policy

Event payloads are structured JSON objects. Payloads must be small, deterministic, and safe to keep for the retention period.

- Payloads may include stable identifiers, old/new status values, gate/blocker codes, retry decisions, cleanup decisions, validation summaries, artifact pointers, external URLs, external timestamps, hashes, and concise reason strings suitable for status output.
- Payloads must not include secrets, API tokens, GitHub App private keys, `.env.local` values, raw Pi JSONL streams, full review transcripts, full command output, or large diffs. Store an artifact pointer and hash instead.
- Payload keys should be version-tolerant: additive keys are allowed, consumers must ignore unknown keys, and required semantic changes must use a new event type or explicit payload version field.
- Payloads should include `old` and `new` values when an event records a state transition, and should include the external source and observed external state when an event records a Linear or GitHub observation that affected a decision.

### Event taxonomy

The first implementation must define events for these orchestration facts at minimum:

- Run attempts: candidate claimed, attempt created, attempt started, prompt prepared, pre-run validation started/finished, implementation command started/finished, PR URL detected, review started/finished/classified, handoff started/finished, attempt failed, attempt terminal outcome recorded, and run lock released.
- Merge gates: PR mapping observed or repaired, merge gate evaluation started/finished, merge blocker recorded, merge approved by gates, merge attempted, merge succeeded, merge failed, branch deletion attempted/finished, and Linear Done transition attempted/finished.
- Cleanup: cleanup scan started/finished, cleanup candidate found, cleanup blocked, cleanup deletion attempted, cleanup deletion succeeded, cleanup deletion failed, and cleanup artifact retention recorded.
- Candidate skips: issue skipped because it is not runnable, already locked, already has an active attempt, lacks required ticket contract sections, violates scope/preflight requirements, is blocked by reconciliation-needed state, or is outside project ownership.
- Errors and recovery: daemon lane error, command timeout, budget failure, SQLite open/migration/schema/lock/transaction failure, reconciliation-needed created, stale lease detected, lease reclaimed, heartbeat missing/stale, external Linear/GitHub lookup failed, and fail-closed decision recorded.

Implementation tickets may add more specific event types, but they must not collapse the required events into an unstructured generic log message when the event affects scheduling, retry, handoff, merge, cleanup, or fail-closed behavior.

Baseline implementation notes:

- Schema version 2 adds `orchestration_events` as an append-only table with writer-generated `event_id`, SQLite `sequence`, UTC `occurred_at`, issue key/id, nullable attempt, run id, source, event type, and compact JSON payload.
- The store API exposes standalone append plus read APIs for recent global events and filtered reads by issue key, issue ID, attempt, and event type. Readers return matching events in append order after applying the recent limit.
- The initial production emission records run-attempt artifact mirror updates as `attempt_started` or `attempt_finished` evidence from source `runner.run_attempt`.
- Status consumes the event log only as a count in the SQLite health summary. Existing lifecycle decisions continue to use current tables and artifacts as before.

### Source precedence and replay expectations

Current-state SQLite tables remain the source of truth for runner decisions. The durable event log is authoritative evidence of committed local orchestration decisions, but it is not the primary decision index during normal operation.

- If current-state rows and event rows conflict, the runner must enter reconciliation-needed or fail closed unless a migration or repair routine can prove which side is incomplete from transaction/version metadata.
- Run locks and leases remain concurrency controls, not historical truth. Their acquisition, renewal, release, expiry, and reclaim decisions must be reflected in current state and appended as events when durable orchestration state is available.
- Workspace artifacts remain audit/evidence exports and backfill inputs. They must not override newer current-state rows or event-log facts without explicit reconciliation.
- Status and repair commands may use the event log to explain how state was reached, to rebuild missing diagnostic artifacts, or to support a future verified replay tool. Normal scheduling, merge, and cleanup decisions must read current-state tables and then append new events for decisions made.

### Retention, migration, and compatibility

- Event rows must be retained at least as long as their associated attempt, PR mapping, cleanup record, or terminal outcome can affect retry, handoff, merge, cleanup, audit, or reconciliation decisions.
- Cleanup may compact old terminal attempts only after preserving the current terminal outcome, PR mapping, retained artifact pointers, and enough event evidence to explain merge, Done/Needs Info/Human Review, cleanup, and fail-closed decisions.
- Retention must be deterministic and configurable or documented before any destructive pruning is implemented. The first implementation should prefer retaining all events rather than guessing a pruning horizon.
- Schema migrations must preserve event identity, ordering, timestamps, issue identity, attempt number, source component, event type, and payload semantics. Migrations may add columns, indexes, source components, event types, or payload keys without rewriting existing event meaning.
- Readers must tolerate older payload versions and unknown additive event types. Writers must not emit events that require a future reader to reinterpret existing event types with incompatible semantics.
- Backfill from pre-event-log artifacts may create synthetic migration events only when clearly marked with source `migration` or `reconciliation`, the original evidence pointer, and the fact that the event was reconstructed rather than observed live.

## Migration and backfill

- The first SQLite implementation must include an explicit schema version and migration path. The runner must not operate against an unknown, partially migrated, or future schema.
- Initial backfill may read existing workspace artifacts and configured config paths to seed attempts, PR mappings, review classifications, evaluation summaries, and cleanup candidates.
- Backfill must treat Linear issue ID/key and GitHub PR URL/number as stable identity anchors when present. Branch names and workspace paths are supporting evidence, not sufficient identity by themselves.
- If multiple artifacts describe the same issue attempt, the newest internally consistent run record should seed the row, while older records remain evidence only.
- If artifacts conflict and no external system can resolve the conflict, the row should be marked reconciliation-needed or blocked rather than guessed.
- Backfill must be safe to re-run: inserting or updating rows should be idempotent and should not create duplicate active attempts for the same Linear issue and branch.

## Reconciliation rules

- Missing DB row with live workspace artifacts: create a reconciliation-needed row from artifacts, then verify Linear and GitHub before scheduling, retrying, merging, or deleting.
- Missing DB row with open Symphony-owned PR: create or repair the issue/PR mapping from GitHub and Linear evidence before applying merge gates or feedback retry decisions.
- Missing DB row with no workspace and no open PR: do not recreate an attempt solely from stale comments; leave the issue eligible only if Linear candidate selection says it is runnable.
- Stale artifacts with newer DB state: keep artifacts as evidence and refresh exports when a future implementation writes them; do not regress DB status from stale files.
- Deleted workspace with open PR: preserve the DB mapping, mark cleanup complete for the workspace path, and continue using GitHub/Linear data for review, feedback, and merge decisions. Recreating the workspace requires an explicit retry or repair action.
- Deleted workspace with no open PR and non-terminal issue: mark the attempt abandoned or reconciliation-needed, then let candidate selection decide whether a new attempt may be claimed.
- Done Linear issue: do not start or retry work. If DB or artifacts show an active attempt, verify whether an associated PR is merged or closed. A merged PR for the expected issue branch can make the workspace cleanup-eligible even when the last local run artifact is non-terminal; closed-unmerged, missing, or conflicting PR facts keep the workspace for reconciliation.
- Open PR: keep the issue/PR mapping active until merged, closed, or explicitly abandoned. Merge gates must use current GitHub state rather than cached eligibility alone.
- Closed unmerged PR: mark merge-ineligible, block auto-merge, and choose retry, Human Review, Needs Info, or terminal failure according to the stored retry decision and current Linear state.
- Stale locks/leases: an expired lease may be reclaimed only after heartbeat evidence shows the owner is gone or inactive beyond the configured stale threshold. Reclaim must be recorded with the old owner, new owner, and reason.
- Heartbeat recovery: after daemon restart, lanes must resume from persisted statuses and leases. Work in `running` or claimed states without a live heartbeat becomes reconciliation-needed before any destructive action. For durable worker tasks, the owner must renew the task lease and active-task heartbeat while the task runs. If renewal fails during execution, the task-scoped context is canceled and the worker records a failed supervision result. Stale claimed rows are marked `reconciliation_needed`, with a worker result and event, when their lease is missing, released, or expired and the lease owner has no fresh heartbeat; normal scheduling must not overwrite that row.
- Worker context propagation: durable worker execution must pass the task-scoped context through queued task claim, AgentRuntime implementation/review execution, handoff completion, merge/cleanup/Linear status entry points, and timeout-bound external clients so ownership loss can interrupt work below the scheduler wrapper.
- Worker task repair: an operator may requeue one explicit `reconciliation_needed` worker task through the repair command after verifying it is safe to retry. The repair updates the task status through SQLite and records a repair event with old/new status and reason; it must not bulk-clear reconciliation-needed rows or infer repair from artifacts.

## Failure mode expectations

- The runner must fail closed when SQLite cannot be opened, migrated, schema-checked, or locked safely.
- Fail closed means no issue claiming, Linear state movement, workspace mutation, retry, cleanup deletion, or PR merge should proceed from uncertain local orchestration state.
- Read-only status commands may report the SQLite failure and any external state they can safely inspect, but must label the result degraded and avoid implying decisions were made.
- If SQLite locking fails or a transaction cannot commit, the runner must assume the orchestration decision did not happen unless a later reconciliation proves it did.
- Migration failure must leave the previous database file recoverable or backed up; implementation tickets should define the exact backup/rollback mechanics before writing migrations.

## Behavior compatibility

- Candidate ordering, lane timing, handoff requirements, review classification semantics, merge gates, cleanup eligibility, and artifact fields from `harness-behavior.md` are preserved unless a future implementation ticket updates that spec.
- SQLite adoption should initially replace where decisions are remembered, not what decisions are made.
- During the additive rollout, mutating command modes should acquire a command-scoped SQLite store once at the orchestration boundary and pass it to mirror helpers where practical. If that store is unavailable, the command reports degraded mirroring and preserves the current non-blocking artifact/JSON behavior until an authority ticket flips the relevant decision class to fail closed.
- The event-log baseline is additive: artifacts remain compatible and authoritative where they are authoritative today, and existing lifecycle decisions are unchanged.
- Any future ticket that changes observable scheduling, merge, cleanup, retry, status, lock, or artifact behavior must update the relevant behavior spec and cite the ADR for this decision.
