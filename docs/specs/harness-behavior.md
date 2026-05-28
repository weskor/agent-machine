# Harness Behavior Spec

This spec captures current observable Agent Machine runner behavior. Update it when a ticket intentionally changes runner behavior; cite it when a refactor only moves code.

Future SQLite-backed orchestration state work is specified in [SQLite Orchestration State Contract](./sqlite-orchestration-state.md). That contract is for CAG-49 implementation planning and does not change the current behavior described here until an implementation ticket updates this spec.

CAG-105 adds the SQLite authority matrix and rollout plan to that contract. Current file-based behavior in this spec remains the observable contract until a later implementation ticket wires a specific decision class to SQLite and updates this spec.

## Runner and Agent responsibility boundary

Principle: the Agent handles ambiguity; the runner owns invariants. A runner invariant is a fact or transition Agent Machine can compute from the project config, Linear, GitHub, SQLite, workspace metadata, or typed artifacts without asking an LLM to judge intent. Agent output may provide evidence, explanations, and edits, but it must not be the only authority for runner-owned invariants.

### Runner-owned deterministic invariants

The runner owns these checks and should fail closed, route to Needs Info/Human Review, or mark reconciliation-needed when the facts are missing or contradictory:

- **Issue contract parsing:** detect the five ticket sections (`Goal`, `Scope`, `Requirements`, `Acceptance Criteria`, `Validation`), explicit MUST/MUST NOT constraints, allowed paths, out-of-scope paths, required validation commands, and hard package/approach constraints.
- **Path scope validation:** compare changed files with machine-readable `Allowed paths:` and `Out of scope:` bullets before review and handoff.
- **Branch and PR/MR ownership:** verify the expected workspace branch, base branch, configured repository, Agent Machine ownership, author/commit-author policy, and issue identifier mapping.
- **PR/MR URL resolution:** resolve the current attempt PR/MR from configured code-host facts and configured repository/branch ownership; agent text may suggest a URL but must not be trusted without API validation.
- **Git/PR/MR handoff ownership:** commit, push, PR/MR create-update, PR/MR URL validation, branch/base validation, and handoff artifact recording are runner-owned wherever possible. Agent/runtime output may provide hints and semantic explanation, but must not be the final authority for code-host identity or handoff state.
- **Lifecycle state transitions:** compute legal Linear state moves for claimed, running, Needs Info, review failed, Human Review, Done, retry, reconciliation-needed, and terminal failure outcomes.
- **Run outcome classification:** classify missing PR, NEEDS_INFO, validation failure, review failure, missing-evidence-only review failure, timeout, budget exhaustion, operational failure, success with PR handoff, and terminal failure from typed evidence.
- **SQLite lease authority:** acquire, heartbeat, renew, release, and reclaim leases according to durable owner/process/heartbeat evidence instead of agent assertions.
- **Merge gates:** evaluate PR state, mergeability, review decision, checks, branch/issue mapping, repository ownership, app author forms, commit author policy, active leases, and project ownership expectations.
- **Cleanup eligibility:** delete only workspaces whose Linear/code-host/SQLite/workspace facts satisfy the cleanup policy; conflicts or missing durable rows are reconciliation blockers, not agent judgment calls. For a Done issue with a non-terminal local attempt artifact, a verified merged PR/MR for the expected issue branch is terminal cleanup evidence; missing PR mappings, wrong branches, closed-unmerged PRs/MRs, or failed code-host refreshes remain non-destructive reconciliation blockers.
- **Artifact and debug locations:** write run records, evaluation artifacts, feedback files, deterministic comments, and capped raw debug output to the specified workspace or `.am/debug/<issue>/` locations.
- **Evidence artifact schema validation:** validate run records, evaluation artifacts, handoff evidence, review classifications, and debug artifact metadata against typed schemas before using them for runner decisions.

### Agent-owned non-deterministic responsibilities

Agent sessions own work that requires judgment, design taste, or semantic understanding:

- choose the implementation approach within the issue contract;
- make code, test, config example, and documentation edits;
- decide where characterization or TDD gives the best behavior evidence;
- evaluate semantic correctness, abstraction quality, naming, depth, locality, and maintainability;
- explain ambiguous repair options, trade-offs, and why Needs Info or Human Review is appropriate;
- perform semantic review of whether a scoped diff satisfies the Goal and Acceptance Criteria.

Agent judgment may inform review comments and handoff summaries, but the runner should convert any repeatable, typed, or externally verifiable judgment into a deterministic check in a follow-up slice.

### Current ambiguous seams to reduce

These seams still rely too much on Agent or reviewer interpretation and should be converted gradually without changing current behavior in this documentation slice:

- Ticket-contract syntax is partly prompt-enforced; malformed or prose-only scope contracts are not fully normalized into a typed issue-contract model.
- Agent-emitted PR URLs still exist as compatibility hints; wrong-branch stale hints should not block runner-owned branch/repository handoff, while invalid URLs, wrong repositories, non-recoverable lookups, and blockers on the expected branch remain deterministic failures.
- Runtime readiness is still shallow for shell CLI providers: operators need the selected `pi` or `codex` binary on `PATH`, and missing binary/auth/model/provider issues should produce pre-claim failures with actionable messages.
- Missing-PR outcomes still keep legacy text parsing compatibility, but shell runtime adapters now prefer an explicit `AM_OUTCOME:` JSON envelope when present. Malformed structured envelopes fail closed instead of falling back to prose.
- Review output classification depends on text markers and reviewer wording; behavior/spec blockers versus missing-evidence-only should be structured.
- Cleanup eligibility is specified but still spans SQLite state, artifacts, Linear state, and workspace facts; conflict reasons should be typed and reusable by status/explain.
- Raw debug artifact caps, names, and retention are described operationally but not represented as a schema the runner can validate.
- Lease stale/reclaim policy depends on durable facts plus process checks; status/explain should expose the exact typed blocker rather than requiring log interpretation.
- Merge and cleanup gates are deterministic in intent, but their evidence should be emitted in a common gate-result schema for handoff, status, and repair.

### Prioritized follow-up implementation slices

1. **Typed issue-contract parser and scope model** — allowed paths: `docs/specs/*.md`, parser code, and focused tests. Convert ticket sections, MUST/MUST NOT constraints, allowed/out-of-scope paths, and validation commands into typed evidence used by prompts and runner checks.
2. **Runtime doctor/preflight** — validate selected provider, `pi` binary availability for `pi_cli`, auth/config/model visibility where feasible, and actionable pre-claim failure messages.
3. **Runner-owned PR create/update and artifact recording** — move commit/push/PR create-update/URL recording toward typed runner operations while preserving current prompt-driven behavior until implemented.
4. **Configurable runtime provider evidence** — persist selected provider/model evidence for supported local providers (`codex_cli` default, explicit `pi_cli`) and future fake/API/ACP-style Adapters.
5. **Fake runtime parity tests** — prove fake/test runtime behavior covers implementation, review, usage, timeout/cancellation, structured output, raw debug, and handoff evidence paths without needing an installed `pi`.
6. **GitHub-first PR resolver** — resolve the attempt PR by configured repository, workspace branch, base branch, issue identifier, and ownership before falling back to agent-output URL parsing.
7. **Structured attempt outcome envelope** — require implementation/review adapters to emit typed outcomes for PR handoff, Needs Info, validation failure, missing PR, retryable failure, and terminal failure; keep legacy text parsing as compatibility input.
8. **Review classification schema** — replace marker-only review parsing with typed `behavior_spec_blocker`, `missing_evidence_only`, `scope_blocker`, and `human_review` classifications.
9. **Gate-result schema for merge, cleanup, status, and explain** — make merge blockers, cleanup eligibility, lease blockers, and reconciliation-needed reasons share a typed result shape.
10. **Evidence/debug artifact schema validation** — define and validate schemas for run records, evaluation artifacts, deterministic handoff comments, feedback files, and capped raw debug artifact metadata.
11. **Provider-aware iteration limits** — only after provider selection, preflight, handoff ownership, and fake parity exist, specify whether turn/iteration limits are useful enough to add.

## Configuration loading

- The CLI defaults to `am.yaml` unless another config path is supplied with `--config` or `-c`.
- The runner loads explicit `--env-file` paths first, then `.env.local` next to the selected `am.yaml`; environment files only fill missing variables, so process environment values win.
- `LINEAR_API_KEY` is required.
- `tracker.project_slug` and `workspace.root` are required in the project config.
- `agent.prompt_path` defaults to `am.agent.md` next to the selected `am.yaml`.
- `repository.remote` enables typed clone setup for empty workspaces; `hooks.after_create` remains an optional setup hook.
- Code-host repository context is configured from the project config before GitHub or GitLab API use. `repository.provider` defaults to `github`; `gitlab` uses GitLab merge requests through `gitlab.endpoint`, `gitlab.project`, and `GITLAB_TOKEN`/`GL_TOKEN`.
- Budget settings from the project config control command, runtime, review, merge, code-host, token, cost, and wall-clock limits.
- `agent.max_concurrent_agents` is parsed from config YAML, defaulting to `1` when omitted.
- `agent.max_retry_backoff_ms` is parsed as a non-negative integer millisecond duration, defaulting to `300000`.
- Invalid values are handled per parser behavior:
  - `max_concurrent_agents`: missing, malformed, or negative values fall back to `1` without failing CLI startup.
  - `max_retry_backoff_ms`: missing values default to `300000`; malformed or negative values fail config load with `non-negative millisecond integer` validation error.

### Scheduler parameter behavior (current runnable contract)

- Current runtime behavior is capacity-limited by `agent.max_concurrent_agents`:
  - `--continuous` starts scheduler, cleanup, merge, handoff, review, and implementation lanes;
  - the scheduler lane deterministically enqueues cleanup tasks for current workspaces, merge tasks for current Human Review Agent Machine PRs, plus up to `agent.max_concurrent_agents` distinct fresh runnable review-resume tasks and implementation tasks per iteration;
  - the cleanup lane claims queued cleanup tasks, refreshes Done issue identifiers, and applies workspace cleanup after re-checking SQLite/workspace facts;
  - the merge lane claims queued merge tasks, refreshes current code-host/Linear/SQLite facts, and runs merge-approved processing without cleanup prepass work;
  - the handoff lane claims pending SQLite `pr_handoff_pending` and `handoff_pending` payload refs before running PR or final handoff side effects;
  - the implementation lane deterministically claims up to `agent.max_concurrent_agents` queued implementation tasks per iteration, then executes the claimed attempts concurrently;
  - the review lane resumes existing review-not-ready attempts after current code-host checks become successful;
  - the default value of `1` preserves the historical single-agent behavior.
- Live dogfood smoke test CAG-131 validated that the claim-first split still lets a `Ready for Agent` issue enter the normal isolated workspace flow; no scheduler or state-machine policy changed as part of that smoke test.
- `agent.max_concurrent_agents` controls only implementation-lane claim capacity. Duplicate work prevention remains enforced before Agent execution by candidate reconciliation, durable SQLite attempt/PR state, issue-specific implementation worker tasks, run locks, and SQLite leases. Terminal run artifacts are compatibility evidence; when SQLite is available, normal candidate selection does not read artifact-only terminal state as run/skip authority.
- `codex_cli` and `pi_cli` do not gate or stop an in-flight attempt by turn count.
- `max_retry_backoff_ms` gates retry timing for durable retry decisions: retryable failed or blocked attempts write retry metadata to SQLite, candidate selection skips the issue until the exponential backoff delay elapses, and the delay is capped by the configured maximum.
- Duplicate dispatch prevention relies on workspace-level run lock artifacts and SQLite lease acquisition when available.
- For duplicate-claim safety, the runner:
  - cleans stale/dead run locks before candidate selection;
  - skips candidates with a currently claimed SQLite implementation worker task;
  - skips any candidate with an active run lock;
  - acquires an issue lock before workspace mutation;
  - skips or retries candidates from durable SQLite state, not from terminal run artifacts, when SQLite is available.
- When the SQLite state DB exists, candidate reconciliation and merge gates also read latest durable attempt, PR mapping, review classification, retry decision, terminal outcome, cleanup, and active run lease facts. Fresh Linear candidate facts and fresh code-host open PR/MR facts remain required before externally visible or mutating actions. Workspace artifacts are evidence/backfill inputs for explicit reconciliation and repair paths, not implicit scheduling inputs.
- Selection, `--status`, and `--explain` report deterministic skip/retry reasons from durable state and fresh external facts. An active SQLite run lease blocks the issue. A durable SQLite PR mapping without a matching current open code-host PR/MR is reported as reconciliation-needed instead of retrying from stale workspace artifacts.

### Retry, feedback, and persistence source (current state)

- Retry-capable implementation transitions are driven by durable SQLite retry and review facts:
  - retryable failed or blocked attempts write retry rows with backoff metadata.
  - review-failed repair retries use SQLite review classification and PR mapping.
  - merge-lane `CHANGES_REQUESTED` handling compares fresh GitHub feedback against the durable SQLite processed feedback hash before suppressing another retry.
  - `.am-run.json`, `.am-evaluation.json`, and `.am-feedback.md` remain compatibility/evidence inputs and may trigger reconciliation, but they are not the implementation-lane queue when SQLite is available.
- `max_retry_backoff_ms` controls durable retry timing for SQLite retry decisions.
- Process restarts preserve retry-delay state through SQLite.

## CLI modes

- Bare invocation without a mode fails closed and does not claim issues. Operators must choose an explicit mode.
- `--once` has been removed. Use `--continuous` for production, or `--worker=implementation` for one controlled implementation worker process.
- `--continuous` / `--daemon`: run scheduler, cleanup, merge, handoff, review, and implementation lanes until canceled, or until `--cycles=N` completes N cycles per lane.
- `--worker=<role>`: run one selected worker role as a separate CLI process through a durable worker task, process heartbeat, and SQLite lease. Supported roles are `status`, `plan`, `cleanup`, `merge`, `reconciliation`, `review`, `implementation`, `handoff`, `linear-status`, and `work`.
  - `status` wraps normal status output and is read-only.
  - `plan` wraps normal explain/planning output and is read-only.
  - `cleanup` refreshes Done issue identifiers and applies existing workspace cleanup behavior.
  - `merge` runs approved-PR merge behavior without cleanup prepass work.
  - `reconciliation` refreshes Linear candidates, open Agent Machine PRs, workspace artifacts, and SQLite facts, then records reconciliation-needed/quarantine evidence without mutating external systems.
  - `review` claims existing SQLite `review_pending` payload refs before claiming queued review-resume worker tasks; it does not claim fresh implementation work or execute handoff side effects.
  - `implementation` claims queued SQLite implementation tasks first; when no implementation task is queued, it selects the next fresh runnable attempt, enqueues an issue-specific implementation task, immediately claims it, and skips review-ready resumes owned by `review`.
  - `handoff` claims existing SQLite `pr_handoff_pending` payload refs before final `handoff_pending` payload refs; it executes PR handoff from the bounded PR handoff payload, queues review/final handoff output, and completes final handoff side effects without implementation or semantic review.
  - `linear-status` claims queued Linear transition intents and applies workflow-state moves without implementation, review, handoff, merge, cleanup, or planning work.
  - `work` is a compatibility alias for implementation-domain claim/execution with capacity from `agent.max_concurrent_agents`; it does not run review, handoff, merge, cleanup, or Linear status work.
- `merge-approved` / `--merge-approved`: merge eligible Agent Machine-owned PRs whose gates pass.
- `cleanup-workspaces` / `--cleanup-workspaces`: inspect workspace cleanup eligibility; `--apply` deletes eligible workspaces.
- `repair-artifacts` / `--repair-artifacts`: repair local Agent Machine artifacts.
- `--repair-worker-task=<task_key>`: operator-triggered repair for a durable worker task that is currently `reconciliation_needed`; it requeues that exact task through SQLite and records a repair event instead of requiring manual database edits.
- `status` / `--status`: print runner/workspace status for the project config.
- `run-status <issue>` / `--run-status=<issue>`: print one compact progress line for an active or recently terminal run from the runner-owned progress snapshot under `.am/state/run-progress/<issue>/progress.json`. This command is local/read-only and must not require Linear or GitHub access.
- `run-ledger <issue>` / `--run-ledger=<issue>` / `status <issue>`: print the append-only local timeline from `.am/state/run-ledger/<issue>/events.jsonl`. Ledger events are exported from runner progress snapshots and terminal run-record summaries. If an older run has no ledger file yet, the command may synthesize a one-event read-only view from the current progress snapshot. This command is local/read-only and must not require Linear or GitHub access.
- `explain` / `--explain` / `--dry-run`: print structured JSON for the next scheduling decision, merge blockers, and cleanup eligibility without mutating Linear, code-host state, workspaces, artifacts, or orchestration state.
- `config print`: print the resolved, redacted config and does not require Linear or code-host access.
- `doctor` / `--doctor`: print a read-only first-run diagnostic for the selected project config. It validates local config readability, prompt file availability, workspace-root parent usability, required credential presence, supported runtime provider selection, and selected runtime command executables without contacting Linear or a code host and without mutating workspaces, artifacts, or orchestration state. Missing hard prerequisites exit non-zero with actionable check messages.
- `--status` includes SQLite event-log counts and recent event summaries when the durable orchestration event schema is available; these diagnostics do not replace artifact summaries or lifecycle decisions.
- `--status` includes durable worker task and worker result rows when available. Worker result rows are the latest typed status for each continuous/selected worker task and are preferred over reconstructing worker outcomes from logs or event streams.

## Live smoke harness

- The live smoke harness is an operator tool, not part of normal `make ci` or daemon startup.
- The harness must require `LIVE_LINEAR=1` and `LINEAR_API_KEY` before reading or mutating Linear.
- The harness may create disposable Linear issues only when explicitly invoked and must print issue identifiers and URLs for manual cleanup.
- The harness must generate an isolated config/workspace root instead of editing the tracked `am.yaml`.
- The generated config defaults to a deterministic fake Agent command so operators can exercise Linear, workspace, code-host PR/MR handoff, review, status, artifacts, and cleanup evidence without spending real Pi budget.
- The harness must not invoke merge or mutating cleanup behavior unless the operator passes an apply flag and sets `LIVE_SMOKE_APPLY=1`.
- Normal offline/local validation must remain unaffected; `make ci` must not require Linear, code-host, Pi, or live smoke credentials.

## Continuous scheduler

- Continuous mode starts scheduler, cleanup, merge, handoff, review, and implementation lanes concurrently.
- Before opening continuous worker state or starting lanes, continuous mode creates a missing configured workspace root when it is safely creatable under an existing non-symlink ancestor.
- Continuous mode fails closed before starting lanes when the SQLite state store cannot be opened; lanes do not fall back to direct artifact/progress-driven work without a shared store.
- The scheduler lane creates durable SQLite cleanup, merge, review-resume, and implementation tasks from current workspace/code-host/Linear candidate/reconciliation facts without acquiring run locks, mutating workspaces, moving Linear, deleting workspaces, merging PRs/MRs, or running agents.
- Each continuous lane claim is wrapped by a durable worker task lease. While a lane task is running, the worker renews the lease, refreshes the claimed task timestamp, and writes an active-task daemon heartbeat with task key, role, lease name, and start time. If supervision fails, the lane task context is canceled and the worker records a failed supervision result. Worker-domain entry points use that task context when claiming queued work and running implementation, review, handoff, merge, cleanup, and Linear status work, including shell hooks, scope-guard git reads, review evidence polling, review runtime execution, PR handoff commands, and merge cleanup. Status output exposes active ownership fields.
- Failed worker tasks are requeued with role-specific retry backoff by setting a future `available_at` from the latest failed worker result instead of immediately re-running tight failure loops.
- Before enqueueing new work, the scheduler marks stale claimed worker tasks as `reconciliation_needed` when the claim is older than the stale threshold and no active lease/fresh owner heartbeat proves the worker is still live. These rows block duplicate dispatch until repaired by an explicit worker-task repair command or future richer reconciliation repair flow.
- The cleanup lane is continuous, sleeps 30 seconds between cycles, claims queued SQLite `cleanup:<workspace>` tasks, refreshes Done issues, re-checks SQLite/workspace facts, and cleans eligible Done workspaces with apply enabled.
- The merge lane is continuous, sleeps 30 seconds between cycles, claims queued SQLite `merge:<issue>:<pr>` tasks, re-reads open code-host PR/MR metadata plus Linear/SQLite reconciliation facts, and runs merge-approved processing without cleanup prepass work.
- The handoff lane consumes pending SQLite `pr_handoff_pending` payload refs before final `handoff_pending` payload refs, acquires the run lease, and executes PR/final handoff from the persisted payload boundary instead of progress output.
- The review lane resumes review-not-ready attempts after current code-host checks become successful.
- Review resume work is queued as durable SQLite `review:<issue>:<attempt>:resume` tasks and consumed by the review lane after pending review payload refs.
- The implementation lane claims up to `agent.max_concurrent_agents` distinct queued SQLite implementation tasks per cycle, executes those claimed attempts concurrently, and sleeps 60 seconds only when no task was claimed or executed.
- Any lane error cancels the scheduler and returns the error.
- With `--cycles=N`, each lane exits after N cycles.
- Each continuous lane writes a typed SQLite worker result with task key, role, lane name, terminal task status, `did_work`, reason code, error text when present, and bounded non-secret payload. This row is the read model for latest worker outcomes; event-log entries remain chronological evidence.

## Candidate selection and state movement

- Active states come from the project config and usually include `Ready for Agent` and `In Progress`.
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

- Each issue runs in `.am/workspaces/<issue-identifier>`.
- The workspace branch is `am/<issue-identifier>-workspace`.
- The runner creates an on-disk run lock before changing issue state or mutating the workspace.
- The runner clones the configured base branch and switches to the expected workspace branch.
- Configured pre-run and post-run validation hooks execute in the workspace.
- Completed workspaces become cleanup candidates when the Linear issue is Done and SQLite-backed durable run attempt status indicates completion, failure, review failure, timeout, budget exhaustion, or another terminal cleanup-policy status. Evaluation/terminal outcome rows such as `handoff_ready` or `operational_failure` remain diagnostic context and must not mask a present terminal run attempt status. Local artifacts remain compatibility evidence, not cleanup authority, when SQLite facts are available. Missing DB rows keep the workspace for reconciliation instead of guessing; missing, insufficient, or stale artifacts do not override terminal SQLite cleanup facts.
- Mutating cleanup fails closed when SQLite cannot be opened. Read-only cleanup may report degraded artifact-backed decisions without deleting.
- Explain mode reports cleanup eligibility using artifact-backed cleanup decisions only and does not delete workspaces or mirror dry-run cleanup rows into SQLite.

Progress snapshots under `.am/state/run-progress/<issue>/progress.json`
are observability-only. They may help an operator inspect an active or recently
terminal attempt, but they must not drive candidate selection, Linear/code-host
transitions, SQLite leases, merge gates, cleanup eligibility, retry decisions,
or review classification.

Run ledgers under `.am/state/run-ledger/<issue>/events.jsonl` are also
observability-only. They append progress-derived events for local inspection and
public evidence reconstruction, but they must not drive candidate selection,
Linear/code-host transitions, SQLite leases, merge gates, cleanup eligibility,
retry decisions, or review classification.

| Transition | Current trigger and facts | Current runner decision | Current authority | SQLite target authority |
| --- | --- | --- | --- | --- |
| Selected | Candidate ordering and reconciliation select a runnable Linear issue. | Write `selected` progress and run runtime preflight before claim, Linear movement, workspace mutation, or Agent execution. | Linear candidate facts, fresh code-host open PR/MR facts, run locks, and SQLite reconciliation facts. Run artifacts and progress are evidence only when SQLite is available. | SQLite owns active attempts, leases, retry blocks, reconciliation blockers, and prior attempt facts; Linear/code-host state remains authoritative for current external facts. |
| Implementation task claimed | A selected issue passes runtime preflight and no current implementation task already owns it. | Queue and claim `implementation:<issue>:<attempt>` in SQLite before acquiring the run lock or mutating the workspace. A currently claimed task skips duplicate dispatch. | SQLite worker task state plus current candidate/reconciliation facts. | Worker task state is the durable implementation dispatch record during the scheduler transition; progress snapshots and terminal artifacts do not dispatch implementation work. |
| Preflight failed | Selected `AgentRuntime` cannot satisfy command requirements. | Leave the issue in Ready, avoid workspace mutation and Linear state movement, write failed progress with `fix_runtime_configuration_before_retry`, and return an operational configuration error. | AgentRuntime preflight result plus project configuration. No run record is required before claim. | SQLite records the fail-closed pre-claim decision when the relevant decision class is authoritative. |
| Claimed | Preflight succeeds and run lock/lease acquisition succeeds. | Write `claimed` progress, emit attempt-started evidence, and move a Ready issue to the configured running state. | On-disk run lock plus SQLite lease mirror when available; Linear current state for state transition. | SQLite lease and heartbeat are authoritative for claim ownership; JSON lock is compatibility/debug evidence. |
| Workspace prepared | Workspace directory exists, optional `after_create` runs for a new workspace, isolated clone/branch is ready, and optional `before_run` validation passes. | Continue to implementation; command timeout writes a `timeout` run record and budget comment when possible. | Workspace filesystem/git facts, configured hook command results, and run lock/lease ownership. | SQLite attempt and workspace lease must be current before mutation; hook outcomes are recorded as attempt evidence. |
| Implementation failed | AgentRuntime implementation command exits with error. | Write a `failed` run record, except command timeout writes `timeout`; include usage/PR hint when available. | AgentRuntime execution result and shell timeout classification; runner-owned record writer. | SQLite attempt outcome is committed before artifact export once the attempt decision class is authoritative. |
| GitHub App auth failed | GitHub App environment or commit identity setup fails before handoff can proceed. | Write status `failed` with `github_app_error` in auth evidence, stop the attempt, and return the error. `github_app_error` is also accepted as a terminal cleanup-policy status for legacy/repair paths. | Environment/GitHub App configuration checks and runner-owned artifact writing. | SQLite records the failed attempt and auth blocker before artifact export. |
| Needs Info | AgentRuntime returns a structured needs-info envelope, or compatibility text output contains `NEEDS_INFO` with blocking questions. | Move the issue to configured Needs Info when available, post the questions as a Linear comment, write status `needs_info`, and stop without PR handoff. If the Linear transition fails, write `needs_info_failed`. | AgentRuntime structured outcome envelope is preferred; legacy text remains compatibility input for questions. Runner owns parsing, Linear transition, comment, and status. | SQLite records the terminal Needs Info decision before artifact export. |
| Post-run validation failed | Configured `after_run` command fails after implementation output. | Write status `failed`, or `timeout` for command timeout, and stop before scope guard, PR handoff, and review. | Hook command result and shell timeout classification. | SQLite records the validation failure and terminal attempt evidence before artifact export. |
| Scope guard failed | Changed files violate machine-readable `Allowed paths:` or `Out of scope:` issue contract. | Move the issue back to Ready when possible, post a scope guard comment, write `review_failed` with behavior/spec blocker classification, and stop before review/handoff success. | Runner-owned scope guard from Linear issue text, workspace git diff, and base branch. | SQLite stores the behavior/spec blocker and retry/next-action decision; artifacts export the evidence. |
| Scope guard errored | Scope guard cannot compute changed files or contract evidence. | Write status `failed` and stop before PR handoff. | Runner-owned scope guard error and workspace/git facts. | SQLite records the failed attempt and error evidence. |
| PR/MR handoff failed | Runner cannot prove branch changes, push the expected branch, create/reuse exactly one PR/MR, or validate repository/base/head ownership for the expected branch. | Write status `failed` with the handoff error and stop before review or Human Review movement. | Runner-owned git/code-host handoff and PR/MR validation. Agent-emitted PR/MR URL is advisory only. | SQLite owns attempt/PR mapping and handoff decision; fresh code-host facts remain authoritative for PR/MR facts. |
| PR/MR handoff succeeded | Runner validates the PR/MR for configured repository, expected branch, base branch, and issue context. | Continue to review when configured, otherwise post deterministic handoff evidence and move to Human Review. | Runner-owned git/code-host handoff, fresh PR/MR details, and Linear transition. | SQLite records PR mapping and handoff evidence before artifacts/comments are treated as exports. |
| Review not ready | PR/MR exists but bounded pre-review readiness finds pending or unavailable checks. | Write status `review_not_ready`, progress next action `wait_for_github_checks_then_retry`, and stop without rerunning implementation. A later cycle may resume semantic review for the same PR/MR once checks are successful. | Fresh code-host PR/MR/check facts, review readiness module, and the SQLite attempt/PR rows. Run records and progress are compatibility evidence. | SQLite retry/review-readiness state owns the resume decision; progress and artifacts are evidence exports only. |
| Review failed | Review command returns `REVIEW_FAIL` with behavior/spec, scope, or generic blocker classification. | Move the issue back to Ready when applicable, post review findings, write status `review_failed`, and stop before successful handoff. | Review command output is semantic evidence; runner owns classification handling, Linear transition, comments, and artifact writing. | Structured review classification and SQLite review state own retry/no-retry decisions; artifacts export findings. |
| Missing-evidence review failure | Review command returns `REVIEW_FAIL` classified as `missing_evidence_only` and a valid PR exists. | Route to Human Review while preserving failed review classification, merge-ineligible evaluation, and no-retry human-review next action. | Review output classification plus runner-owned PR validation and Linear transition. | SQLite stores failed review classification and merge-ineligible/human-review routing before artifact export. |
| Success with PR handoff | Valid PR exists, validation passed, scope guard passed or warned, and configured review is absent or passed. | Post/update deterministic PR and Linear handoff comments, move the issue to Human Review, and write status `success`. | Runner-owned validation, PR identity, review status, comments, Linear transition, and run/evaluation artifacts. | SQLite records attempt success, PR mapping, handoff state, review classification, and merge blockers before artifacts/comments act as exports. |
| Timeout | Hook, implementation, or review command exceeds configured timeout. | Write status `timeout`, post a budget failure comment when possible, and stop the current path. | Shell timeout classification and budget configuration. | SQLite records timeout as terminal attempt state with evidence pointer. |
| Budget exceeded | Usage or wall-clock budget exceeds configured limit after implementation or review evidence is available. | Write status `budget_exceeded`, post a budget failure comment when possible, and stop before further handoff/merge eligibility. | Runner-owned budget calculation from project config, timing, and parsed usage. | SQLite records budget state and terminal attempt evidence before artifact export. |

## Pi implementation attempt

- The implementation prompt includes the project config and prompt, Linear issue description, ticket-contract preflight, behavior-contract preflight, current PR feedback when present, SQLite-backed prior review state for review-failed repairs, and runner constraints. State-backed implementation retries do not read prior review findings from `.am-run.json`.
- The agent leaves the scoped code/test/doc diff and validation notes in the workspace; the runner creates or updates exactly one PR from the expected workspace branch into the configured base branch.
- Current production behavior defaults to the local clean `codex exec` shell Adapter (`codex_cli` provider). Project configs may explicitly select Claude Code with `runtime.provider: claude_cli` or the legacy local `pi` CLI with `runtime.provider: pi_cli`, and legacy configs that only configure `pi.command` continue to resolve to `pi_cli`. Operators must have the configured implementation command installed, discoverable on `PATH` or as an executable path, and configured for the desired auth/provider/model. When review is configured, the configured review command executable must also resolve. Missing command setup fails during preflight before claim or workspace mutation.
- Runtime commands are configured with `runtime.command` and optional `runtime.review_command`; legacy `pi.command` and `pi.review_command` remain accepted as compatibility aliases.
- Current run records write `runtime_command` and `runtime_usage`; legacy `pi_command` and `pi_usage` are still read and normalized during artifact repair, backfill, status, and evaluation paths.
- The agent should stop after scoped diff and validation notes. Any Agent-emitted PR URL is advisory compatibility input only.
- The runner parses runtime usage from output and may read an Agent-emitted configured-repository GitHub PR or GitLab MR URL as a hint, but branch-based runner-owned handoff is authoritative for the current attempt.
- When a Linear issue includes machine-readable `Allowed paths:` or `Out of scope:` bullets, the runner checks changed files against that path contract before review and handoff. Scope violations are recorded as behavior/spec blockers and move the issue back to the configured Ready state. Issues without a machine-readable path contract continue with a warning so legacy tickets remain runnable.
- Primary daemon logs record concise lifecycle summaries and do not print the raw Pi JSONL implementation or review stream during normal operation.
- When `AM_DEBUG_RAW_OUTPUT=1` is set, raw agent output is written to capped debug artifacts outside the issue workspace. For the standard `<repo>/.am/workspaces/<issue>` layout, artifacts go under `<repo>/.am/debug/<issue>/*-raw.log`; nonstandard workspace roots preserve the parent-root `.am/debug/<issue>/` fallback. Debug artifact phase names are path-sanitized before writing. The primary log includes the artifact path.
- Workspace dirtiness ignores only bounded runner/operator evidence artifacts. A top-level regular file named `false` is treated as a non-authoritative external subagent scratch marker only when it is zero bytes or bounded reviewer-output text with the known subagent scratch signature. Non-matching non-empty `false` files, nested `false` files, symlinks, and all other untracked files still block cleanup and merge readiness as real dirty workspace state.
- After a successful implementation diff, the runner attempts deterministic Git/PR handoff. A same-repository Agent PR hint whose head branch does not match the expected workspace branch is treated as stale and ignored; the run fails only when runner handoff cannot prove branch changes, push the branch, create/reuse exactly one PR, or validate repository/base/head ownership for the expected branch.
- Before commit, push, PR create/update, or PR validation side effects, the runner writes a durable SQLite PR handoff intent/result row, a `pr_handoff_pending` payload ref, and a bounded PR handoff payload with issue identity, workspace, expected branch, advisory Agent PR URL, and non-secret continuation facts for review/final handoff. Progress output is compatibility evidence. Inline PR handoff execution re-reads that persisted payload before side effects and completes the typed intent row with the resulting PR URL or error. The selected `handoff` worker may claim a pending PR handoff payload ref through SQLite and the run lease, execute PR handoff from that payload, complete the same typed intent/result row, then queue `review_pending` when review is configured or final `handoff_pending` when it is not.
- Runner-owned handoff may update the exact expected `am/<issue>-workspace` remote branch on retry using a lease-protected branch update. This is limited to the validated current issue branch and must not broaden into arbitrary force-push behavior.

## Review and handoff

- When a review command is configured, the runner runs a separate review prompt after runner-owned PR create/update resolves a validated PR URL.
- Before review, the runner refreshes code-host PR/MR details and status checks into a bounded deterministic evidence packet for the review prompt. The runner may wait up to the configured GitHub/code-host timeout for pending/unavailable checks to become terminal; remaining pending, unavailable, or failed check evidence is reported as runner-owned review-readiness state instead of relying on the reviewer to rediscover timing-sensitive code-host facts.
- Pending or unavailable checks after that bounded wait are recorded as a SQLite `review_not_ready` attempt/PR state plus progress compatibility output with `wait_for_github_checks_then_retry`; this is a retryable runner state, not an unrecoverable operational failure or merge-gate blocker. A later daemon cycle resumes at semantic review from SQLite state for the existing PR/MR once checks become successful, without re-running implementation. Terminal progress/evaluation exports must preserve the retryable waiting-for-checks next action even when older artifacts or summaries contain generic merge-blocker wording. Failed checks remain a review-readiness blocker and are reported with `fix_failing_github_checks_before_review` evidence.
- If the authenticated GitHub App cannot read check-run details but GitHub reports the PR merge state as `CLEAN`, the runner may treat that clean merge-state fact as a bounded status-check readiness fallback for review readiness and merge-gate summaries. The fallback must not apply when merge state is absent, pending, unstable, dirty, or when any visible status context/check is failing or pending.
- Review output must contain `REVIEW_PASS` or `REVIEW_FAIL`; failed review is classified so behavior/spec blockers remain `review_failed` and prevent automatic handoff success, while `missing_evidence_only` failures with an existing PR may route to Human Review for human judgment instead of returning to Ready for Agent.
- Missing-evidence-only review handoff is not automatic merge approval: evaluation artifacts must keep the failed review status/classification, mark the run merge-ineligible before human approval, and record a no-retry human-review next action.
- Before semantic review side effects, the runner writes a durable SQLite `review_pending` payload ref and a bounded review payload with the PR URL, issue identity, workspace, branch, scope result, validation evidence, usage, and GitHub auth evidence. Progress output is compatibility evidence. Inline review execution re-reads that persisted payload before collecting review evidence or invoking the review command. Review resume after `review_not_ready` uses the same payload execution boundary. The selected `review` worker may claim a pending review payload ref through SQLite and the run lease, execute review from that payload, and write `handoff_pending` output for the handoff worker when review does not end terminally.
- Before handoff, the runner validates PR/MR details through the configured code-host API.
- Handoff requires the PR/MR to belong to the expected repository, branch, base branch, and issue identifier context.
- Before final handoff side effects, the runner writes a durable SQLite `handoff_pending` payload ref and a bounded handoff payload with the PR URL, review result, validation evidence, usage, issue identity, workspace/branch, and GitHub auth evidence. Progress output is compatibility evidence. Inline execution completes handoff by re-reading that persisted payload, and the selected `handoff` worker consumes the same payload boundary when a pending handoff is left for another process.
- On successful handoff, the runner posts or updates deterministic PR/Linear comments and moves the Linear issue to the configured handoff state, usually `Human Review`.

## Merge gates

- Merge automation only considers Agent Machine-owned PRs.
- Merge gates check PR state, mergeability/conflicts, review decision, status checks, branch/issue mapping, app author and commit author invariants, and configured project ownership expectations.
- A Human Review issue with a successful run artifact, `review_status=failed`, and `review_classification=missing_evidence_only` may merge only after GitHub reports explicit approval, green checks, mergeable state, and the other merge gates pass; behavior/spec/scope review failures remain blocked.
- The PR/MR author invariant derives accepted GitHub App author forms from the configured app slug (`app/<slug>` and `<slug>[bot]`) or an explicit project config PR author override. GitHub App commit identity and commit-author merge checks derive the bot name/email from the same configured app slug (`<slug>[bot] <<slug>[bot]@users.noreply.github.com>`) or `GITHUB_APP_SLUG` fallback. GitLab MR ownership uses `gitlab.pr_author_override` or `GITLAB_PR_AUTHOR_OVERRIDE`. If no expected author source is available, merge automation fails closed with a clear ownership blocker.
- Successful merge deletes the Agent Machine workspace branch and moves the Linear issue to Done.
- Blocked merges should explain the gate reason instead of forcing a merge.
- Pull-request merge gates produce a deterministic gate result with domain, subject, status, blocker codes, reason text, and next action when available. Merge automation, status/explain output, and merge-blocked events consume this shared shape rather than rebuilding independent blocker strings.
- Cleanup decisions are also projected into the deterministic gate-result shape so cleanup explain/status paths report the same passed, blocked, or reconciliation-needed status as the cleanup worker.
- Explain mode uses the same pull-request merge gate evaluator as merge automation and reports blockers without merging, deleting branches, moving Linear issues, or writing feedback artifacts.

## Failure and artifact behavior

- Every attempt writes a run record with issue, workspace, branch, timing, usage, review, PR URL, status, budget, and behavior-contract evidence fields when possible.
- Before exporting run/evaluation artifacts, every terminal attempt writes a typed SQLite attempt result with issue, workspace, branch, status, PR mapping, review state, retry decision, and terminal outcome fields. Run records and evaluation JSON are evidence exports of that decision, not the only authority for the continuous loop.
- Run records include `schema_version` and `schema_source` metadata. Existing unversioned run records are treated as schema version 1 with `schema_source: legacy`; current writers emit schema version 1 with `schema_source: current`.
- Evaluation artifacts classify dogfood outcomes and suggested improvements, and include the same schema metadata. Existing unversioned evaluation artifacts are accepted as schema version 1 legacy artifacts.
- Repair, backfill, and compatibility artifact readers preserve compatibility with unversioned schema version 1 artifacts, reject unsupported or malformed explicit schema versions, and project legacy/current provenance into SQLite artifact references.
- Active and terminal attempts write a compact progress snapshot outside the cloned issue workspace. The snapshot is observability-only evidence for polling/status efficiency; it must not drive candidate selection, Linear/code-host transitions, leases, merge gates, cleanup eligibility, or review classification.
- Under the SQLite transition plan, run records, evaluation artifacts, deterministic comments, PR bodies, and capped debug logs become evidence/debug exports rather than primary coordination state as each decision class is migrated.
- Command timeouts and budget failures produce failure status and, when possible, Linear comments.
- Run locks are released when the attempt exits.
- Secrets, GitHub App private keys, and `.env.local` files must stay untracked.
