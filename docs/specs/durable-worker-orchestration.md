# Durable Worker Orchestration Spec

This spec describes the target architecture for splitting Agent Machine execution
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
- Keep SQLite as the local source of truth for Agent Machine decisions while
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

Agent Machine should have one central orchestration model and multiple worker
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

It must not mutate Linear, code-host state, workspaces, artifacts, or worker tasks beyond
its own process task, lease, heartbeat, and event records.

### Implementation worker

Owns workspace preparation, prompt writing, AgentRuntime preflight/execution,
usage capture, Needs Info detection, validation hooks, and raw/debug artifact
exports for implementation attempts.

It must not create or update code-host PRs/MRs as the final handoff action, approve
merge eligibility, or move issues to Done.

### Review worker

Owns deterministic PR/check/scope evidence refresh and semantic review
execution. It records review status, classification, output pointers, and merge
eligibility blockers.

It must not silently rerun implementation. If checks are pending, missing, or
conflicting, it records waiting/reconciliation state for a later review task.
Inline review writes a durable `review_pending` worker payload ref plus a
bounded review payload before semantic review side effects, then re-reads that
payload for evidence collection and review execution. Progress output is a
compatibility/export artifact for operators. Review resume after
`review_not_ready` is discovered from SQLite attempt/PR state once current
code-host checks are successful, then uses the same payload execution boundary
before handing the review result back to the caller. The selected `review`
process first claims existing `review_pending` refs through SQLite and the run
lease, then claims queued `review:<issue>:<attempt>:resume` worker tasks for
SQLite-discovered review-not-ready resumes. Review-resume tasks refresh
Linear/code-host/reconciliation facts before acquiring the run lease and executing
the same review payload boundary. A review worker that completes non-terminal
review queues `handoff_pending` output for the handoff worker.

### Handoff worker

Owns commit/push/PR create-update, PR URL validation, deterministic PR and
Linear handoff comments, and movement to Human Review or Needs Info.

It must not run implementation or semantic review. It consumes durable attempt,
validation, review, and PR facts. The inline runner writes a durable
PR handoff intent/result row, a `pr_handoff_pending` worker payload ref, and a
bounded PR handoff payload before commit, push, PR create/update, and PR
validation side effects, then re-reads that payload for PR handoff execution and
completes the typed intent with the resulting PR URL or error. After review, the
inline runner writes a durable `handoff_pending` worker payload ref plus a
bounded handoff payload before final handoff side effects, then re-reads that
payload for final handoff execution. Progress output remains
compatibility/export evidence. The dedicated `handoff` worker claims
`pr_handoff_pending` refs before final `handoff_pending` refs and completes the
same typed PR handoff intent/result row. After standalone PR handoff succeeds,
it queues `review_pending` when review is configured, otherwise it queues final
`handoff_pending`. Final handoff side effects still run from the same persisted
handoff payload boundary.

### Merge worker

Owns merge-gate evaluation, merge attempts, branch deletion, Linear Done
transition, and merge-result events.

It must refresh GitHub and Linear facts immediately before externally visible
actions and must respect active blockers, leases, and reconciliation-needed
state.

The selected `merge` process must run merge-domain work through its
process-owned SQLite store. It must not run the cleanup worker's Done-issue
refresh or workspace cleanup prepass.

Continuous merge dispatch is task-backed: the scheduler creates stable
`merge:<issue>:<pr>` worker tasks from fresh open Agent Machine PR metadata and the
current Linear handoff state. The merge lane claims one queued merge task,
refreshes open code-host PR/MR metadata plus Linear/SQLite reconciliation facts before
acting, and records a worker result for the claimed task. A missing closed PR is
completed as processed stale work rather than merged from stale scheduler input.

Merge-gate evaluation emits the shared deterministic gate-result shape: domain,
subject, status, blocker codes, reason text, next action, and bounded metadata.
Worker events and read models should preserve that shape so status and explain do
not derive separate blocker vocabularies.

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

Continuous cleanup dispatch is task-backed: the scheduler creates stable
`cleanup:<workspace>` worker tasks from current workspace directories without
deciding deletion. The cleanup lane claims one queued cleanup task, refreshes
current Done issue identifiers, re-checks SQLite cleanup facts, workspace
dirtiness, and safety paths, then records a worker result for the claimed task.
Cleanup decisions are projected into the same deterministic gate-result shape so
delete, keep, and reconciliation-needed outcomes share one status/blocker
vocabulary with merge and future deterministic gates.

## Worker task model

A durable worker task is the unit of work that lets workers run independently.
It is not the source of policy by itself; it points at the current-state rows
and facts a worker must re-read before acting.

Each task should include:

- stable `task_key` for idempotent enqueue/update;
- `role`, using the worker role vocabulary above;
- optional Linear issue key/ID and attempt number;
- task `status`, at minimum `queued`, `claimed`, `completed`, `failed`, or
  `canceled`; a stale claimed task that cannot be proven safe to continue is
  moved to `reconciliation_needed` instead of being silently re-queued;
- priority and `available_at` for backoff and scheduling;
- lease name required before mutation;
- compact JSON payload with deterministic, non-secret parameters;
- created/updated timestamps.

Payloads must not include secrets, raw command output, full review transcripts,
large diffs, or environment values. Use artifact pointers and hashes instead.

When a worker task is claimed, the worker must acquire the task lease before
mutating local or external state, record an active-task heartbeat that names the
task key, role, and lease name, and renew the lease while the task is running.
Each renewal also refreshes the claimed task timestamp so stale recovery can
distinguish active long-running work from abandoned claims. When the task
finishes, the active-task heartbeat is cleared by a later lane/process heartbeat.
If lease renewal or task supervision fails while work is running, the worker
cancels the task-scoped execution context, records a failed worker result with a
supervision reason, and fails closed instead of continuing under uncertain
ownership.
Worker-domain entry points must accept and honor that task-scoped context for
claiming queued tasks, code-host/Linear calls with runner timeouts, AgentRuntime
preflight/execution, workspace setup hooks, scope-guard git reads, semantic
review evidence polling, semantic review execution, PR handoff git/GitHub
commands, merge cleanup, and final handoff completion.
When the scheduler requeues a previously failed worker task, it must apply the
role-specific retry backoff to `available_at` from the latest failed worker
result. Failed implementation/work tasks back off longer than review, handoff,
merge, cleanup, scheduler, status, plan, Linear status, and reconciliation tasks.

Each worker task also writes a durable worker result row when it reaches a
terminal task status. The result row is the latest typed read model for worker
status and includes task key, role, lane name, issue/attempt identity when
known, terminal status, `did_work`, reason code, error text when present,
started/finished timestamps, and bounded non-secret payload. Status, explain,
and scheduler diagnostics should read this row before reconstructing latest
worker state from event logs, stdout, or progress files. Orchestration events
remain the append-only evidence trail.

Implementation, review, and handoff paths must persist typed attempt results
directly into SQLite before exporting run/evaluation artifacts. The attempt
result row owns current status, PR mapping, review state, retry decision, and
terminal outcome. Artifact mirroring remains a compatibility/readback path and
may add artifact references, but workers and the scheduler should not require a
JSON artifact read to know the latest attempt decision.

Review-resume and implementation dispatch write issue-specific durable worker
tasks before acquiring the run lease or mutating the workspace. The scheduler
lane may enqueue `review:<issue>:<attempt>:resume` tasks for SQLite
`review_not_ready` attempts whose current code-host checks are successful. The selected
implementation worker first claims queued `implementation:<issue>:<attempt>`
tasks from SQLite and refreshes Linear/code-host/reconciliation facts before taking
the run lease. When no queued implementation task exists during the scheduler
transition, the implementation lane may select the next runnable candidate and
enqueue that task, then immediately claim it. A currently claimed task blocks
duplicate implementation dispatch for that issue; completed or failed task rows
may be re-queued only after the candidate/retry/reconciliation policy selects
the issue again from current SQLite and external facts.

The scheduler performs a stale-claim recovery pass before enqueueing new
cleanup, merge, review-resume, or implementation work. Claimed worker tasks
whose update timestamp is past the stale threshold and whose lease is missing,
released, or expired without a fresh owner heartbeat are marked
`reconciliation_needed`. The transition writes a worker result row and a
`reconciliation_needed` event with the task key, role, prior status, reason, and
lease name when present. A `reconciliation_needed` worker task blocks duplicate
dispatch until an operator or repair flow resolves it; normal enqueue logic must
not overwrite it.

Merge dispatch follows the same durable-task shape for externally visible merge
work: the scheduler may enqueue `merge:<issue>:<pr>` tasks only from current
GitHub open PR metadata and current Linear handoff state, and the merge worker
must re-read current code-host/Linear/SQLite facts before merge, branch deletion,
Linear Done transition, or conflict feedback side effects.

Cleanup dispatch uses durable `cleanup:<workspace>` tasks so filesystem
discovery does not also authorize deletion. The cleanup worker must refresh Done
issue identifiers and re-run the cleanup decision against current SQLite and
workspace facts before deleting, keeping, or reporting reconciliation-needed.

Implementation-lane candidate selection, repair retries, and merge gates read
durable SQLite attempt, PR, review classification, retry, and lease facts. When
SQLite is available, terminal run artifacts and evaluation files may create
reconciliation-needed evidence, but they do not directly select, skip, retry, or
merge work.

Pending cross-role continuations use durable worker payload refs. A payload ref
names the worker role, pending phase, issue/attempt identity, workspace, branch,
PR URL when known, payload path, and status. Workers discover pending
`review_pending`, `pr_handoff_pending`, and `handoff_pending` continuations from
these refs. The payload file remains the bounded execution input, but SQLite is
the queue and progress files are operator-visible evidence.

## Process boundary

Running workers in separate OS processes is allowed only after the worker task,
lease, heartbeat, and reconciliation contracts are implemented for the role
being separated. A split process must be behaviorally equivalent to the same
worker running inside the current daemon.

Safe process separation requires:

- one SQLite database for the project config;
- per-worker process identity and heartbeat;
- task claiming that is atomic with lease acquisition or blocked until the lease
  is acquired;
- idempotent external writes, including deterministic comments and PR updates;
- no shared in-memory state as required coordination.

The initial separate-process rollout started with non-destructive `status` and
`plan` worker roles. The supported `--worker` roles are now `status`, `plan`,
`cleanup`, `merge`, `reconciliation`, `review`, `implementation`, `handoff`,
`linear-status`, and `work`. Each role runs through a durable worker task and
SQLite lease, records a process heartbeat, and exits after one completed task.
The `reconciliation` process refreshes Linear candidates, open Agent Machine PRs,
workspace artifacts, and SQLite facts, then records reconciliation-needed or
quarantine evidence as SQLite events without repairing or mutating external
systems. Continuous mode runs cleanup and merge as separate lanes: cleanup
claims queued workspace cleanup tasks and refreshes Done issues before applying
cleanup, while merge claims queued merge tasks and runs merge-approved
processing through the shared continuous SQLite store without a cleanup prepass.
The `review` process only claims existing SQLite
`review_pending` payload refs before resuming SQLite `review_not_ready`
attempts whose current code-host checks are successful. The `implementation`
process claims fresh runnable attempts and skips review-ready resumes owned by
`review`; the
compatibility `work` process is constrained to the same implementation-domain
batch execution and does not run review, handoff, merge, cleanup, or Linear
status work. The `handoff` process claims SQLite `pr_handoff_pending` refs
before `handoff_pending` refs through the run lease, executes PR handoff from
the persisted PR handoff payload, and completes final handoff side effects from
the persisted handoff payload. The `linear-status` process claims queued Linear
transition intents and applies workflow moves after refreshing workflow states.
Mutating roles use existing worker modules and lane behavior after their
fresh-fact, lease, and fail-closed contracts are covered by focused tests.

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
