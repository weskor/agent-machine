# SQLite Orchestration State Contract

This spec defines the intended behavior contract for CAG-49: moving Pi Symphony runner orchestration state to a local SQLite store. It is a planning contract only; CAG-59 does not implement SQLite storage or change current runner behavior. Current observable behavior remains governed by [Harness Behavior Spec](./harness-behavior.md) until implementation tickets deliberately update it.

## Goals

- Make orchestration decisions durable across daemon restarts, workspace deletion, stale local artifacts, and partial external-system failures.
- Preserve Linear and GitHub ownership boundaries while giving the runner one local source for scheduling, retry, handoff, merge, and cleanup decisions.
- Keep JSON and Markdown workspace artifacts useful as audit/evidence exports rather than primary coordination state.

## Source of truth model

- SQLite is the source of truth for runner orchestration decisions made by Pi Symphony: which issue attempt is active, which PR belongs to it, whether review passed, whether feedback is new, whether merge is eligible, which cleanup action is pending, which lease is held, and what terminal outcome was reached.
- Linear remains the external system of record for issue identity, issue workflow state, comments, assignee/ownership where configured, labels, priority, and Done/Needs Info/Human Review state transitions.
- GitHub remains the external system of record for PR identity, PR state, review decision, mergeability, checks, branch metadata, authorship, and merge result.
- Workspace JSON/Markdown files, including `.pi-symphony-run.json`, `.pi-symphony-evaluation.json`, PR bodies, and deterministic comments, are audit and evidence exports. They may be backfill inputs during migration or repair, but after SQLite adoption they must not silently override newer database state.
- When local SQLite state conflicts with Linear or GitHub, the runner must reconcile using explicit rules in this spec rather than assuming either side is always current.

## State domains to persist

The SQLite model should persist enough data to make each daemon cycle idempotent and explainable:

- Issue attempts: Linear issue ID/key, attempt number, workspace path, branch name, base branch, timestamps, prompt inputs hash, validation command summary, and attempt lifecycle status.
- Issue/PR mapping: expected repository, expected branch, base branch, PR number/URL, PR head/base metadata, and whether the PR is Symphony-owned.
- Run status: pending, claimed, running, handoff, failed, needs-info, review-failed, merge-blocked, merged, cleaned, abandoned, or other terminal status needed by implementation tickets.
- Review status and classification: review command status, pass/fail marker, failure classification such as behavior/spec blocker versus missing-evidence-only, review output hash or artifact pointer, and merge eligibility result derived from review.
- Feedback hash/status: latest processed PR review/comment feedback hash, whether feedback has been incorporated into a retry, whether feedback is stale, and the next action chosen.
- Merge eligibility and blockers: current gate result, blocker codes, last checked external PR/check state, ownership invariants, and reason text suitable for status output or deterministic comments.
- Cleanup status: workspace existence, cleanup eligibility, cleanup decision, deletion result, retained artifact pointers, and reasons cleanup is blocked.
- Locks and leases: issue-level claim lease, workspace mutation lease, merge-lane lease if needed, lease owner, acquisition time, expiry, renewal time, and release reason.
- Daemon heartbeat: process identity, lane name, workflow path, cycle number, last successful cycle time, last error, and whether recovery is required.
- Retry decisions: retry count, retry budget state, reason for retry/no-retry, feedback or failure input that triggered the decision, and handoff to Needs Info or Human Review when retries stop.
- Terminal outcomes: Done/merged, Needs Info, Human Review handoff, abandoned due to missing external record, failed closed due to local state error, and cleaned workspace outcome.

## Migration and backfill

- The first SQLite implementation must include an explicit schema version and migration path. The runner must not operate against an unknown, partially migrated, or future schema.
- Initial backfill may read existing workspace artifacts and configured workflow paths to seed attempts, PR mappings, review classifications, evaluation summaries, and cleanup candidates.
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
- Done Linear issue: do not start or retry work. If DB or artifacts show an active attempt, mark it terminal after verifying whether an associated PR is merged or closed.
- Open PR: keep the issue/PR mapping active until merged, closed, or explicitly abandoned. Merge gates must use current GitHub state rather than cached eligibility alone.
- Closed unmerged PR: mark merge-ineligible, block auto-merge, and choose retry, Human Review, Needs Info, or terminal failure according to the stored retry decision and current Linear state.
- Stale locks/leases: an expired lease may be reclaimed only after heartbeat evidence shows the owner is gone or inactive beyond the configured stale threshold. Reclaim must be recorded with the old owner, new owner, and reason.
- Heartbeat recovery: after daemon restart, lanes must resume from persisted statuses and leases. Work in `running` or claimed states without a live heartbeat becomes reconciliation-needed before any destructive action.

## Failure mode expectations

- The runner must fail closed when SQLite cannot be opened, migrated, schema-checked, or locked safely.
- Fail closed means no issue claiming, Linear state movement, workspace mutation, retry, cleanup deletion, or PR merge should proceed from uncertain local orchestration state.
- Read-only status commands may report the SQLite failure and any external state they can safely inspect, but must label the result degraded and avoid implying decisions were made.
- If SQLite locking fails or a transaction cannot commit, the runner must assume the orchestration decision did not happen unless a later reconciliation proves it did.
- Migration failure must leave the previous database file recoverable or backed up; implementation tickets should define the exact backup/rollback mechanics before writing migrations.

## Behavior compatibility

- Candidate ordering, lane timing, handoff requirements, review classification semantics, merge gates, cleanup eligibility, and artifact fields from `harness-behavior.md` are preserved unless a future implementation ticket updates that spec.
- SQLite adoption should initially replace where decisions are remembered, not what decisions are made.
- Any future ticket that changes observable scheduling, merge, cleanup, retry, status, lock, or artifact behavior must update the relevant behavior spec and cite the ADR for this decision.
