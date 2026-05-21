# Harness Behavior Spec

This spec captures current observable Pi Symphony runner behavior. Update it when a ticket intentionally changes runner behavior; cite it when a refactor only moves code.

Future SQLite-backed orchestration state work is specified in [SQLite Orchestration State Contract](./sqlite-orchestration-state.md). That contract is for CAG-49 implementation planning and does not change the current behavior described here until an implementation ticket updates this spec.

CAG-105 adds the SQLite authority matrix and rollout plan to that contract. Current file-based behavior in this spec remains the observable contract until a later implementation ticket wires a specific decision class to SQLite and updates this spec.

## Runner and Agent responsibility boundary

Principle: the Agent handles ambiguity; the runner owns invariants. A runner invariant is a fact or transition Pi Symphony can compute from the workflow, Linear, GitHub, SQLite, workspace metadata, or typed artifacts without asking an LLM to judge intent. Agent output may provide evidence, explanations, and edits, but it must not be the only authority for runner-owned invariants.

### Runner-owned deterministic invariants

The runner owns these checks and should fail closed, route to Needs Info/Human Review, or mark reconciliation-needed when the facts are missing or contradictory:

- **Issue contract parsing:** detect the five ticket sections (`Goal`, `Scope`, `Requirements`, `Acceptance Criteria`, `Validation`), explicit MUST/MUST NOT constraints, allowed paths, out-of-scope paths, required validation commands, and hard package/approach constraints.
- **Path scope validation:** compare changed files with machine-readable `Allowed paths:` and `Out of scope:` bullets before review and handoff.
- **Branch and PR ownership:** verify the expected workspace branch, base branch, configured repository, Symphony ownership, author/commit-author policy, and issue identifier mapping.
- **PR URL resolution:** resolve the current attempt PR from GitHub facts and configured repository/branch ownership; agent text may suggest a URL but must not be trusted without API validation.
- **Git/PR handoff ownership:** commit, push, PR create/update, PR URL validation, branch/base validation, and handoff artifact recording are runner-owned wherever possible. Agent/runtime output may provide hints and semantic explanation, but must not be the final authority for GitHub identity or handoff state.
- **Lifecycle state transitions:** compute legal Linear state moves for claimed, running, Needs Info, review failed, Human Review, Done, retry, reconciliation-needed, and terminal failure outcomes.
- **Run outcome classification:** classify missing PR, NEEDS_INFO, validation failure, review failure, missing-evidence-only review failure, timeout, budget exhaustion, operational failure, success with PR handoff, and terminal failure from typed evidence.
- **SQLite lease authority:** acquire, heartbeat, renew, release, and reclaim leases according to durable owner/process/heartbeat evidence instead of agent assertions.
- **Merge gates:** evaluate PR state, mergeability, review decision, checks, branch/issue mapping, repository ownership, app author forms, commit author policy, active leases, and workflow ownership expectations.
- **Cleanup eligibility:** delete only workspaces whose Linear/GitHub/SQLite/workspace facts satisfy the cleanup policy; conflicts or missing durable rows are reconciliation blockers, not agent judgment calls. For a Done issue with a non-terminal local attempt artifact, a verified merged PR for the expected issue branch is terminal cleanup evidence; missing PR mappings, wrong branches, closed-unmerged PRs, or failed GitHub refreshes remain non-destructive reconciliation blockers.
- **Artifact and debug locations:** write run records, evaluation artifacts, feedback files, deterministic comments, and capped raw debug output to the specified workspace or `.symphony/debug/<issue>/` locations.
- **Evidence artifact schema validation:** validate run records, evaluation artifacts, handoff evidence, review classifications, and debug artifact metadata against typed schemas before using them for runner decisions.

### Agent-owned non-deterministic responsibilities

Agent sessions own work that requires judgment, design taste, or semantic understanding:

- choose the implementation approach within the issue contract;
- make code, test, workflow example, and documentation edits;
- decide where characterization or TDD gives the best behavior evidence;
- evaluate semantic correctness, abstraction quality, naming, depth, locality, and maintainability;
- explain ambiguous repair options, trade-offs, and why Needs Info or Human Review is appropriate;
- perform semantic review of whether a scoped diff satisfies the Goal and Acceptance Criteria.

Agent judgment may inform review comments and handoff summaries, but the runner should convert any repeatable, typed, or externally verifiable judgment into a deterministic check in a follow-up slice.

### Current ambiguous seams to reduce

These seams still rely too much on Agent or reviewer interpretation and should be converted gradually without changing current behavior in this documentation slice:

- Ticket-contract syntax is partly prompt-enforced; malformed or prose-only scope contracts are not fully normalized into a typed issue-contract model.
- PR URL discovery still starts from agent output and then validates the first configured-repository URL; branch/repository lookup should become primary when possible.
- Commit/push/PR create/update are still largely prompt-assigned to the Agent in the current `pi_cli` flow; the target contract makes those runner-owned once implementation slices add safe Git/GitHub operations.
- Runtime readiness is implicit today: the current implementation shells to `pi`, so operators need a configured `pi` binary on `PATH`; missing binary/auth/model/provider issues should become pre-claim failures with actionable messages.
- Missing-PR outcomes depend on parsing agent text for `NEEDS_INFO` versus failure; a typed outcome envelope would make classification less brittle.
- Review output classification depends on text markers and reviewer wording; behavior/spec blockers versus missing-evidence-only should be structured.
- Cleanup eligibility is specified but still spans SQLite state, artifacts, Linear state, and workspace facts; conflict reasons should be typed and reusable by status/explain.
- Raw debug artifact caps, names, and retention are described operationally but not represented as a schema the runner can validate.
- Lease stale/reclaim policy depends on durable facts plus process checks; status/explain should expose the exact typed blocker rather than requiring log interpretation.
- Merge and cleanup gates are deterministic in intent, but their evidence should be emitted in a common gate-result schema for handoff, status, and repair.

### Prioritized follow-up implementation slices

1. **Typed issue-contract parser and scope model** — allowed paths: `docs/specs/*.md`, parser code, and focused tests. Convert ticket sections, MUST/MUST NOT constraints, allowed/out-of-scope paths, and validation commands into typed evidence used by prompts and runner checks.
2. **Runtime doctor/preflight** — validate selected provider, `pi` binary availability for `pi_cli`, auth/config/model visibility where feasible, and actionable pre-claim failure messages.
3. **Runner-owned PR create/update and artifact recording** — move commit/push/PR create-update/URL recording toward typed runner operations while preserving current prompt-driven behavior until implemented.
4. **Configurable runtime provider selection** — allow explicit provider selection (`pi_cli` default, `fake` for tests, future API/app-server/ACP-style Adapters) and persist selected provider/model evidence.
5. **Fake runtime parity tests** — prove fake/test runtime behavior covers implementation, review, usage, timeout/cancellation, structured output, raw debug, and handoff evidence paths without needing an installed `pi`.
6. **GitHub-first PR resolver** — resolve the attempt PR by configured repository, workspace branch, base branch, issue identifier, and ownership before falling back to agent-output URL parsing.
7. **Structured attempt outcome envelope** — require implementation/review adapters to emit typed outcomes for PR handoff, Needs Info, validation failure, missing PR, retryable failure, and terminal failure; keep legacy text parsing as compatibility input.
8. **Review classification schema** — replace marker-only review parsing with typed `behavior_spec_blocker`, `missing_evidence_only`, `scope_blocker`, and `human_review` classifications.
9. **Gate-result schema for merge, cleanup, status, and explain** — make merge blockers, cleanup eligibility, lease blockers, and reconciliation-needed reasons share a typed result shape.
10. **Evidence/debug artifact schema validation** — define and validate schemas for run records, evaluation artifacts, deterministic handoff comments, feedback files, and capped raw debug artifact metadata.
11. **Provider-aware `max_turns` behavior** — only after provider selection, preflight, handoff ownership, and fake parity exist, specify and implement turn/iteration limits against explicit runtime capabilities.

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
  - `max_concurrent_agents`: missing, malformed, or negative values fall back to `1` without failing CLI startup.
  - `max_turns`: missing, malformed, zero, or negative values fall back to `1` without failing CLI startup.
  - `max_retry_backoff_ms`: missing values default to `300000`; malformed or negative values fail workflow load with `non-negative millisecond integer` validation error.

### Scheduler parameter behavior (current runnable contract)

- Current runtime behavior is effectively single-attempt, single-worker:
  - `--continuous` starts one work lane and one merge lane;
  - the work lane performs `runOne` for at most one candidate per iteration and then yields.
- `agent.max_concurrent_agents` is currently accepted but not enforced by scheduler logic.
- `agent.max_turns` is enforced at the AgentRuntime/config preflight boundary for `pi_cli`: normalized `1` preserves the single implementation attempt, while values greater than `1` fail before claim, lease acquisition, workspace mutation, Linear state movement, or Agent execution.
- `pi_cli` does not gate or stop an in-flight attempt by turn count; future session-runtime Adapters must declare and enforce a `max_turns` capability rather than relying on scheduler guesses.
- `max_retry_backoff_ms` gates retry timing for durable retry decisions: retryable failed or blocked attempts write retry metadata to SQLite, candidate selection skips the issue until the exponential backoff delay elapses, and the delay is capped by the configured maximum.
- Duplicate dispatch prevention relies on workspace-level run lock artifacts and SQLite lease acquisition when available.
- For duplicate-claim safety, the runner:
  - cleans stale/dead run locks before candidate selection;
  - skips any candidate with an active run lock;
  - acquires an issue lock before workspace mutation;
  - skips candidates with reusable terminal run artifacts unless fresh PR feedback exists.
- When the SQLite state DB exists, candidate reconciliation also reads latest durable attempt, PR mapping, retry decision, terminal outcome, cleanup, and active run lease facts. Fresh Linear candidate facts and fresh GitHub open-PR facts remain required before externally visible or mutating actions. Workspace artifacts are evidence/backfill inputs and must not silently override newer SQLite rows or current GitHub PR facts.
- Selection, `--status`, and `--explain` report deterministic skip/retry reasons from the same reconciliation policy. An active SQLite run lease blocks the issue. A durable SQLite PR mapping without a matching current open GitHub PR is reported as reconciliation-needed instead of retrying from stale workspace artifacts.

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
- `--run-status=<issue>`: print one compact progress line for an active or recently terminal run from the runner-owned progress snapshot under `.symphony/state/run-progress/<issue>/progress.json`. This command is local/read-only and must not require Linear or GitHub access.
- `--explain` / `--dry-run`: print structured JSON for the next scheduling decision, merge blockers, and cleanup eligibility without mutating Linear, GitHub, workspaces, artifacts, or orchestration state.
- `--status` includes SQLite event-log counts and recent event summaries when the durable orchestration event schema is available; these diagnostics do not replace artifact summaries or lifecycle decisions.

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
- After selecting a candidate but before acquiring a run lease, moving the issue
  to the running state, creating/updating the workspace, creating/updating a PR,
  or invoking `after_create`, the runner preflights the selected AgentRuntime.
  A preflight failure leaves the issue in `Ready for Agent`, avoids workspace
  creation/mutation, and returns an operational configuration error naming the
  provider and missing command.
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
- Current production behavior shells to the local `pi` CLI (`pi_cli` provider). Operators must have the configured implementation command installed, discoverable on `PATH` or as an executable path, and configured for the desired auth/provider/model. When review is configured, the configured review command executable must also resolve. Missing command setup fails during preflight before claim or workspace mutation.
- The agent should stop after scoped diff, validation notes, and PR handoff.
- The runner parses Pi usage and the first configured-repository GitHub PR URL from the output.
- When a Linear issue includes machine-readable `Allowed paths:` or `Out of scope:` bullets, the runner checks changed files against that path contract before review and handoff. Scope violations are recorded as behavior/spec blockers and move the issue back to the configured Ready state. Issues without a machine-readable path contract continue with a warning so legacy tickets remain runnable.
- Primary daemon logs record concise lifecycle summaries and do not print the raw Pi JSONL implementation or review stream during normal operation.
- When `PI_SYMPHONY_DEBUG_RAW_OUTPUT=1` is set, raw agent output is written to capped debug artifacts outside the issue workspace (for example `.symphony/debug/<issue>/*-raw.log` under the workspace root), and the primary log includes the artifact path.
- Workspace dirtiness ignores only bounded runner/operator evidence artifacts. A top-level regular file named `false` is treated as a non-authoritative external subagent scratch marker only when it is zero bytes or bounded reviewer-output text with the known subagent scratch signature. Non-matching non-empty `false` files, nested `false` files, symlinks, and all other untracked files still block cleanup and merge readiness as real dirty workspace state.
- If no Agent PR URL is detected after a successful implementation, the runner attempts deterministic Git/PR handoff. The run fails only when runner handoff cannot prove branch changes, push the branch, create/reuse exactly one PR, or validate repository/base/head ownership.

## Review and handoff

- When a review command is configured, the runner runs a separate review prompt after runner-owned PR create/update resolves a validated PR URL.
- Before review, the runner refreshes GitHub PR details and status checks into a bounded deterministic evidence packet for the review prompt. The runner may wait up to the configured GitHub timeout for pending/unavailable checks to become terminal; remaining pending, unavailable, or failed check evidence is reported as runner-owned review-readiness state instead of relying on the reviewer to rediscover timing-sensitive GitHub facts.
- Pending or unavailable checks after that bounded wait are recorded as `review_not_ready` progress with `wait_for_github_checks_then_retry`; this is a retryable runner state, not an unrecoverable operational failure or merge-gate blocker. A later daemon cycle may resume at semantic review for the existing PR once checks become successful, without re-running implementation. Terminal progress/evaluation exports must preserve the retryable waiting-for-checks next action even when older artifacts or summaries contain generic merge-blocker wording. Failed checks remain a review-readiness blocker and are reported with `fix_failing_github_checks_before_review` evidence.
- If the authenticated GitHub App cannot read check-run details but GitHub reports the PR merge state as `CLEAN`, the runner may treat that clean merge-state fact as a bounded status-check readiness fallback for review readiness and merge-gate summaries. The fallback must not apply when merge state is absent, pending, unstable, dirty, or when any visible status context/check is failing or pending.
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
- Run records include `schema_version` and `schema_source` metadata. Existing unversioned run records are treated as schema version 1 with `schema_source: legacy`; current writers emit schema version 1 with `schema_source: current`.
- Evaluation artifacts classify dogfood outcomes and suggested improvements, and include the same schema metadata. Existing unversioned evaluation artifacts are accepted as schema version 1 legacy artifacts.
- Repair and backfill paths preserve compatibility with unversioned schema version 1 artifacts, reject unsupported or malformed explicit schema versions, and project legacy/current provenance into SQLite artifact references.
- Active and terminal attempts write a compact progress snapshot outside the cloned issue workspace. The snapshot is observability-only evidence for polling/status efficiency; it must not drive candidate selection, Linear/GitHub transitions, leases, merge gates, cleanup eligibility, or review classification.
- Under the SQLite transition plan, run records, evaluation artifacts, deterministic comments, PR bodies, and capped debug logs become evidence/debug exports rather than primary coordination state as each decision class is migrated.
- Command timeouts and budget failures produce failure status and, when possible, Linear comments.
- Run locks are released when the attempt exits.
- Secrets, GitHub App private keys, and `.env.local` files must stay untracked.
