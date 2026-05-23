# Durable Worker Orchestration Spec

This spec describes the target architecture for splitting Pi Symphony execution
into independently runnable workers while preserving one authoritative
orchestration state machine.

It extends, but does not replace, the authority model in
[SQLite Orchestration State Contract](./sqlite-orchestration-state.md). Current
observable CLI behavior remains governed by
[Harness Behavior Spec](./harness-behavior.md) until implementation tickets
explicitly update behavior.

## Goals

- Break the large single-run orchestration path into independently testable and
  restartable worker responsibilities.
- Allow future workers to run in separate OS processes without inventing
  separate state machines.
- Keep SQLite as the local source of truth for Pi Symphony decisions while
  Linear and GitHub remain authoritative for their external domains.
- Make each worker idempotent, lease-protected, and recoverable after crashes or
  daemon restarts.

## Non-goals

- Giving each worker its own independent orchestration policy.
- Moving Linear, GitHub, review, merge, cleanup, or retry policy into protocol
  Adapters or prompts.
- Replacing current CLI modes before the durable worker state and reconciliation
  rules are in place.
- Running multiple workers against the same issue, PR, or workspace without an
  explicit lease and current-state row.

## Architecture

Pi Symphony should have one central orchestration model and multiple worker
roles. Workers are execution boundaries, not authorities.

Each worker:

- reads a durable worker task or current-state row from SQLite;
- refreshes external facts for the domain it is about to mutate;
- acquires the required lease before mutating local files or external systems;
- writes current-state changes and explanatory events in the same transaction
  where possible;
- treats repeated execution of the same task as safe and idempotent;
- records reconciliation-needed instead of guessing when facts conflict.

The scheduler may run these roles in one process, separate goroutines, or future
separate OS processes. The correctness contract is the same because SQLite,
leases, current-state rows, and event logs are the coordination layer.

## Worker roles

### Scheduler / claimant

Owns candidate scans and task creation. It reads fresh Linear candidate facts,
SQLite active attempts, retry state, reconciliation blockers, and leases. It
creates durable tasks for implementation, review, handoff, merge, cleanup, or
reconciliation work.

It must not mutate a workspace, create PRs, run agents, merge PRs, or move
Linear workflow state except where a future ticket explicitly defines a
claim-time transition.

### Plan worker

Owns read-only orchestration planning/explain output. It refreshes candidate,
PR, cleanup, and local state facts and reports the next planned actions without
claiming implementation, review, handoff, merge, cleanup, or Linear status work.

It must not mutate Linear, GitHub, workspaces, artifacts, or worker tasks beyond
its own process task, lease, heartbeat, and event records.

### Implementation worker

Owns workspace preparation, prompt writing, AgentRuntime preflight/execution,
usage capture, Needs Info detection, validation hooks, and raw/debug artifact
exports for implementation attempts.

It must not create or update GitHub PRs as the final handoff action, approve
merge eligibility, or move issues to Done.

### Review worker

Owns deterministic PR/check/scope evidence refresh and semantic review
execution. It records review status, classification, output pointers, and merge
eligibility blockers.

It must not silently rerun implementation. If checks are pending, missing, or
conflicting, it records waiting/reconciliation state for a later review task.
Inline review writes `review_pending` progress plus a bounded review payload
before semantic review side effects, then re-reads that payload for evidence
collection and review execution. Review resume after `review_not_ready` uses the
same payload execution boundary before handing the review result back to the
caller. The selected `review` process first claims existing `review_pending`
records through the run lease, executes the same review payload boundary, and
queues `handoff_pending` output for the handoff worker when review is
non-terminal.

### Handoff worker

Owns commit/push/PR create-update, PR URL validation, deterministic PR and
Linear handoff comments, and movement to Human Review or Needs Info.

It must not run implementation or semantic review. It consumes durable attempt,
validation, review, and PR facts. The inline runner writes `handoff_pending`
progress plus a bounded handoff payload before final handoff side effects, then
re-reads that payload for final handoff execution. The dedicated `handoff` worker
claims pending records through the run lease and finishes the same handoff side
effects from the same persisted payload boundary.

### Merge worker

Owns merge-gate evaluation, merge attempts, branch deletion, Linear Done
transition, and merge-result events.

It must refresh GitHub and Linear facts immediately before externally visible
actions and must respect active blockers, leases, and reconciliation-needed
state.

### Linear status worker

Owns Linear workflow transitions and comments that are not already performed
inside a narrower transaction by another worker. It reads SQLite transition
intent plus fresh Linear workflow state before updating Linear.

It must not decide lifecycle policy from Linear alone.

The initial `linear-status` process consumes queued transition intents only.
Comment intents remain inline until a deterministic comment-idempotency contract
exists for standalone processing.

### Reconciliation worker

Owns repairable disagreement detection across SQLite, Linear, GitHub, workspace
artifacts, leases, and operator input. It marks reconciliation-needed rows and
records the evidence required for safe repair.

It must not repair by preferring stale artifacts over newer SQLite or fresh
external facts.

### Cleanup worker

Owns workspace and branch cleanup after terminal evidence proves cleanup is
safe. It must respect active leases, open PRs, non-terminal attempts, missing DB
rows, and reconciliation blockers.

## Worker task model

A durable worker task is the unit of work that lets workers run independently.
It is not the source of policy by itself; it points at the current-state rows
and facts a worker must re-read before acting.

Each task should include:

- stable `task_key` for idempotent enqueue/update;
- `role`, using the worker role vocabulary above;
- optional Linear issue key/ID and attempt number;
- task `status`, at minimum `queued`, `claimed`, `completed`, `failed`, or
  `canceled`;
- priority and `available_at` for backoff and scheduling;
- lease name required before mutation;
- compact JSON payload with deterministic, non-secret parameters;
- created/updated timestamps.

Payloads must not include secrets, raw command output, full review transcripts,
large diffs, or environment values. Use artifact pointers and hashes instead.

## Process boundary

Running workers in separate OS processes is allowed only after the worker task,
lease, heartbeat, and reconciliation contracts are implemented for the role
being separated. A split process must be behaviorally equivalent to the same
worker running inside the current daemon.

Safe process separation requires:

- one SQLite database for the workflow;
- per-worker process identity and heartbeat;
- task claiming that is atomic with lease acquisition or blocked until the lease
  is acquired;
- idempotent external writes, including deterministic comments and PR updates;
- no shared in-memory state as required coordination.

The initial separate-process rollout started with non-destructive `status` and
`plan` worker roles. The supported `--worker` roles are now `status`, `plan`,
`cleanup`, `merge`, `review`, `implementation`, `handoff`, `linear-status`, and
`work`. Each role runs through a durable worker task and SQLite lease, records a
process heartbeat, and exits after one completed task. The `review` process only
claims existing `review_pending` payloads before falling back to review-not-ready
attempts whose current GitHub checks are successful. The `implementation`
process claims fresh runnable attempts and skips review-ready resumes owned by
`review`. The `handoff` process claims `handoff_pending` progress through the run
lease and completes side effects from the persisted handoff payload. The
`linear-status` process claims queued Linear transition intents and applies
workflow moves after refreshing workflow states. Mutating roles use existing
worker modules and lane behavior after their fresh-fact, lease, and fail-closed
contracts are covered by focused tests.

## Rollout

1. Specify worker roles and add durable worker-task schema/API.
2. Teach status to display queued/claimed/failed worker task counts.
3. Convert current continuous lanes to enqueue and consume worker tasks in the
   same process, preserving current behavior.
4. Split implementation, review, handoff, merge, reconciliation, Linear status,
   and cleanup responsibilities into focused worker modules.
5. Allow selected workers to run as separate processes once lease and heartbeat
   recovery is proven by tests.

Each rollout issue should preserve observable behavior unless it deliberately
updates the relevant behavior spec.
