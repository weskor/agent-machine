# Harness Behavior Spec

This spec captures current observable Pi Symphony runner behavior. Update it when a ticket intentionally changes runner behavior; cite it when a refactor only moves code.

Future SQLite-backed orchestration state work is specified in [SQLite Orchestration State Contract](./sqlite-orchestration-state.md). That contract is for CAG-49 implementation planning and does not change the current behavior described here until an implementation ticket updates this spec.

CAG-105 adds the SQLite authority matrix and rollout plan to that contract. Current file-based behavior in this spec remains the observable contract until a later implementation ticket wires a specific decision class to SQLite and updates this spec.

## Configuration loading

- The CLI defaults to `WORKFLOW.md` unless another workflow path is supplied.
- The runner loads `.env.local` from the current directory, then the nearest `.env.local` for the workflow path.
- `LINEAR_API_KEY` is required.
- `tracker.project_slug` and `workspace.root` are required in the workflow.
- GitHub repository context is configured from the workflow before GitHub API use.
- Budget settings from the workflow control command, Pi, review, merge, GitHub, token, cost, and wall-clock limits.
- `agent.max_concurrent_agents` and `agent.max_turns` are parsed from workflow YAML, defaulting to `1` when omitted.
- `agent.max_retry_backoff_ms` is parsed as a non-negative integer millisecond duration, defaulting to `300000`.
- Invalid values are handled per parser behavior:
  - `max_concurrent_agents` / `max_turns`: missing, malformed, or negative values fall back to `1` without failing CLI startup.
  - `max_retry_backoff_ms`: missing values default to `300000`; malformed or negative values fail workflow load with `non-negative millisecond integer` validation error.

### Scheduler parameter behavior (current runnable contract)

- Current runtime behavior is effectively single-attempt, single-worker:
  - `--continuous` starts one work lane and one merge lane;
  - the work lane performs `runOne` for at most one candidate per iteration and then yields.
- `agent.max_concurrent_agents` and `agent.max_turns` are currently accepted but not enforced by scheduler logic.
- `max_turns` therefore currently does not gate or stop an attempt by turn count in the Pi CLI runtime.
- `max_retry_backoff_ms` is currently parsed and stored but not used to gate retry timing.
- Duplicate dispatch prevention relies on workspace-level run lock artifacts and SQLite lease acquisition when available.
- For duplicate-claim safety, the runner:
  - cleans stale/dead run locks before candidate selection;
  - skips any candidate with an active run lock;
  - acquires an issue lock before workspace mutation;
  - skips candidates with reusable terminal run artifacts unless fresh PR feedback exists.

### Retry, feedback, and persistence source (current state)

- Retry-capable transitions today are driven by run artifacts and feedback files, not scheduler counters:
  - `.pi-symphony-run.json` terminal artifacts mark prior outcomes.
  - `.pi-symphony-feedback.md` presence allows a retry path when terminal success artifacts include a PR URL.
- `max_retry_backoff_ms` does not control this path today.
- Process restarts do not preserve additional retry-delay state for these fields, because no scheduler delay/backoff state is recorded.
- Existing persisted run artifacts do preserve prior outcome and feedback hashes for continuity across restarts.

## CLI modes

- Default and `--once`: attempt one eligible Linear issue with `runOne`.
- `--continuous` / `--daemon`: run merge and work lanes until canceled, or until `--cycles=N` completes N cycles per lane.
- `--merge-approved`: merge eligible Symphony-owned PRs whose gates pass.
- `--cleanup-workspaces`: inspect workspace cleanup eligibility; `--apply` deletes eligible workspaces.
- `--repair-artifacts`: repair local Symphony artifacts.
- `--status`: print runner/workspace status for the workflow.
- `--explain` / `--dry-run`: print structured JSON for the next scheduling decision, merge blockers, and cleanup eligibility without mutating Linear, GitHub, workspaces, artifacts, or orchestration state.
- `--status` includes SQLite event-log counts when the durable orchestration event schema is available; these counts are diagnostic evidence only and do not replace artifact summaries or lifecycle decisions.

## Continuous scheduler

- Continuous mode starts a merge lane and a work lane concurrently.
- The merge lane is continuous, sleeps 30 seconds between cycles, cleans Done workspaces with apply enabled, then runs merge-approved processing.
- The work lane calls `runOne` and sleeps 60 seconds only when no work was done.
- Any lane error cancels the scheduler and returns the error.
- With `--cycles=N`, each lane exits after N cycles.

## Candidate selection and state movement

- Active states come from the workflow and usually include `Ready for Agent` and `In Progress`.
- `Ready for Agent` candidates rank before other active states.
- Safety labels rank before unlabeled work: runner-safety/harness first, docs-only/low-risk next, all others after.
- Priority and older creation time break ties after state and safety ranking.
- Before claiming work, stale/dead run locks are cleaned up.
- Explain mode reuses candidate ordering and reconciliation policy to report ordered candidates, the selected candidate when one is runnable, and skip/block reasons.
- A claimed issue is moved to the configured running state, usually `In Progress`.
- If the implementation outputs `NEEDS_INFO`, the issue moves to the configured needs-info state and receives the questions as a Linear comment.

## Workspace lifecycle

- Each issue runs in `.symphony/workspaces/<issue-identifier>`.
- The workspace branch is `symphony/<issue-identifier>-workspace`.
- The runner creates an on-disk run lock before changing issue state or mutating the workspace.
- The runner clones the configured base branch and switches to the expected workspace branch.
- Configured pre-run and post-run validation hooks execute in the workspace.
- Completed workspaces become cleanup candidates when the Linear issue is Done and SQLite-backed durable run attempt status indicates completion, failure, review failure, timeout, budget exhaustion, or another terminal cleanup-policy status. Evaluation/terminal outcome rows such as `handoff_ready` or `operational_failure` remain diagnostic context and must not mask a present terminal run attempt status. Local artifacts remain compatibility evidence and safety blockers, but missing DB rows or artifact/DB conflicts keep the workspace for reconciliation instead of guessing.
- Mutating cleanup fails closed when SQLite cannot be opened. Read-only cleanup may report degraded artifact-backed decisions without deleting.
- Explain mode reports cleanup eligibility using artifact-backed cleanup decisions only and does not delete workspaces or mirror dry-run cleanup rows into SQLite.

## Pi implementation attempt

- The implementation prompt includes the workflow body, Linear issue description, ticket-contract preflight, behavior-contract preflight, PR feedback when present, and runner constraints.
- The agent must create or update exactly one PR from the expected workspace branch into the configured base branch.
- The agent should stop after scoped diff, validation notes, and PR handoff.
- The runner parses Pi usage and the first configured-repository GitHub PR URL from the output.
- When a Linear issue includes machine-readable `Allowed paths:` or `Out of scope:` bullets, the runner checks changed files against that path contract before review and handoff. Scope violations are recorded as behavior/spec blockers and move the issue back to the configured Ready state. Issues without a machine-readable path contract continue with a warning so legacy tickets remain runnable.
- Primary daemon logs record concise lifecycle summaries and do not print the raw Pi JSONL implementation or review stream during normal operation.
- When `PI_SYMPHONY_DEBUG_RAW_OUTPUT=1` is set, raw agent output is written to capped debug artifacts outside the issue workspace (for example `.symphony/debug/<issue>/*-raw.log` under the workspace root), and the primary log includes the artifact path.
- If no PR URL is detected, the run fails unless a NEEDS_INFO path was detected.

## Review and handoff

- When a review command is configured, the runner runs a separate review prompt after the implementation opens/updates a PR.
- Review output must contain `REVIEW_PASS` or `REVIEW_FAIL`; failed review is classified so behavior/spec blockers remain `review_failed` and prevent automatic handoff success, while `missing_evidence_only` failures with an existing PR may route to Human Review for human judgment instead of returning to Ready for Agent.
- Missing-evidence-only review handoff is not merge approval: evaluation artifacts must keep the failed review status/classification, mark the run merge-ineligible, and record a no-retry human-review next action.
- Before handoff, the runner validates PR details through the GitHub API.
- Handoff requires the PR to belong to the expected repository, branch, base branch, and issue identifier context.
- On successful handoff, the runner posts or updates deterministic PR/Linear comments and moves the Linear issue to the configured handoff state, usually `Human Review`.

## Merge gates

- Merge automation only considers Symphony-owned PRs.
- Merge gates check PR state, mergeability/conflicts, review decision, status checks, branch/issue mapping, app author and commit author invariants, and configured workflow ownership expectations.
- The PR author invariant derives accepted GitHub App author forms from the configured app slug (`app/<slug>` and `<slug>[bot]`) or an explicit workflow PR author override; if neither source is available, merge automation fails closed with a clear ownership blocker.
- Successful merge deletes the Symphony workspace branch and moves the Linear issue to Done.
- Blocked merges should explain the gate reason instead of forcing a merge.
- Explain mode uses the same pull-request merge gate evaluator as merge automation and reports blockers without merging, deleting branches, moving Linear issues, or writing feedback artifacts.

## Failure and artifact behavior

- Every attempt writes a run record with issue, workspace, branch, timing, usage, review, PR URL, status, budget, and behavior-contract evidence fields when possible.
- Evaluation artifacts classify dogfood outcomes and suggested improvements.
- Under the SQLite transition plan, run records, evaluation artifacts, deterministic comments, PR bodies, and capped debug logs become evidence/debug exports rather than primary coordination state as each decision class is migrated.
- Command timeouts and budget failures produce failure status and, when possible, Linear comments.
- Run locks are released when the attempt exits.
- Secrets, GitHub App private keys, and `.env.local` files must stay untracked.
