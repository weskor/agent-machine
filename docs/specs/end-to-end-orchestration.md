# End-to-End Orchestration Spec

This spec describes the target end-to-end Pi Symphony behavior for V1. It does not replace the current observable behavior in `docs/specs/harness-behavior.md` or the SQLite transition contract in `docs/specs/sqlite-orchestration-state.md`; implementation tickets must update the relevant spec when behavior intentionally changes.

## Goals

- Make the Linear issue to GitHub PR loop smooth, observable, and recoverable.
- Ensure every Agent session outcome is explicit: success, retry, Needs Info, Human Review, reconciliation-needed, or terminal failure.
- Support multiple Agent sessions without duplicate issue claims, unsafe workspace mutation, or unclear ownership.
- Make quality evidence durable enough for a human reviewer to trust the PR without reconstructing the run from logs.
- Keep future editor, MCP, web, and cloud surfaces as Adapters over the same orchestration Modules.

## Actors and systems

- **Linear** is the external system of record for Linear issue identity, workflow state, comments, labels, priority, and operator handoff states.
- **GitHub** is the external system of record for repository state, PR identity, review decision, checks, mergeability, authorship, branches, and merge result.
- **SQLite orchestration state** is the intended local source of truth for Pi Symphony decisions once the SQLite behavior contract is implemented.
- **Workspace artifacts** are audit and evidence exports. They may seed backfill or repair, but after SQLite adoption they must not silently override newer local state.
- **Agent sessions** perform bounded implementation or review attempts in isolated workspaces through a selected AgentRuntime provider.
- **Operators** configure workflows, inspect status, answer Needs Info, review PRs, and approve merge policy.

The authority matrix in [SQLite Orchestration State Contract](./sqlite-orchestration-state.md#authority-matrix) defines which system owns each runner decision during the SQLite transition. Later implementation tickets must cite that matrix instead of re-deciding precedence between SQLite, Linear, GitHub, artifacts, and operator input.

## Deterministic runner boundary

The V1 orchestration target follows the boundary in [Harness Behavior Spec: Runner and Agent responsibility boundary](./harness-behavior.md#runner-and-agent-responsibility-boundary): the Agent handles ambiguity; the runner owns invariants.

- Runner Modules should compute issue contract parsing, path scope, branch/PR ownership, PR URL resolution, lifecycle transitions, outcome classification, leases, merge gates, cleanup eligibility, artifact/debug locations, and evidence schema validity from typed state and external system facts.
- Agent sessions should make implementation choices, edit code/tests/docs, perform semantic review, judge abstraction quality, and explain ambiguous repair options.
- Future Adapters, including ACP, MCP, web, and cloud surfaces, must not move orchestration policy into Adapter-specific prompts. They should call the same runner Modules and surface typed runner decisions.
- When an LLM repeatedly makes the same check from parseable facts, treat that as a signal to add or prioritize a deterministic runner invariant slice.

## AgentRuntime provider strategy

Pi Symphony is a runner harness with an AgentRuntime Adapter seam. The current
local production provider is `pi_cli`, which shells to the local `pi` executable.
That means current users need `pi` installed on `PATH` and configured for auth,
provider, model, and quota outside Pi Symphony. `pi_cli` may remain the first and
default local provider, but it is not the runner architecture itself.

Supported vocabulary:

- `pi_cli`: local shell Adapter for the Pi CLI.
- `fake`: deterministic fake/test Adapter for parity tests and contract coverage.
- Future API, app-server, ACP-style, or MCP-style Adapters: transport choices
  that must reuse runner Modules instead of owning orchestration policy.

Before claiming or mutating work, the selected provider preflights command
availability, auth/config discoverability where feasible, selected provider/model
visibility, and actionable operator-facing failure messages. Missing configured
implementation or review command executables for `pi_cli` are hard pre-claim
failures.

Provider capabilities should be explicit for implementation runs, review runs,
usage/cost reporting, timeout/cancellation, `max_turns`/iteration limits,
structured output, raw debug capture, and deterministic handoff support.

## Happy path

1. A Linear issue is written with Goal, Scope, Requirements, Acceptance Criteria, and Validation.
2. The Candidate reconciliation Module determines that the issue is runnable and not blocked by active attempts, open PRs, stale artifacts, or missing external facts. After the relevant SQLite rollout phase, it uses SQLite for local claim/retry/reconciliation decisions, fresh Linear/GitHub for their external facts, and artifacts only as evidence exports or verified backfill inputs.
3. Pi Symphony claims the issue by recording a lease and heartbeat before mutating external state.
4. Pi Symphony creates or refreshes an isolated Workspace for the attempt.
5. The Agent session reads `AGENTS.md`, `CONTEXT.md`, `LANGUAGE.md`, relevant specs, relevant ADRs, and the Linear issue contract.
6. The Agent session writes or updates tests first when behavior is changed or characterized.
7. The Agent session implements the smallest scoped change that satisfies the issue.
8. Validation runs in the Workspace using the workflow-configured commands.
9. Pi Symphony owns Git/PR handoff: commit or validate the exact scoped diff, push the expected branch, create or update exactly one PR, validate the PR URL, and record artifacts. The Agent stops after the scoped diff and validation notes; any Agent-emitted PR URL remains advisory compatibility input.
10. Pi Symphony validates that the PR belongs to the configured repository, expected branch, expected base branch, expected author/owner policy, and current issue attempt.
11. When review is configured, Pi Symphony refreshes PR/check/scope evidence, confirms the run is ready for semantic review, and passes that deterministic evidence packet into the review prompt.
12. Review runs when configured and classifies the semantic/spec result.
13. Pi Symphony posts deterministic PR and Linear Handoff comments with behavior-contract evidence.
14. The Linear issue moves to Human Review, Needs Info, Done, or another configured state according to the outcome.
15. The merge lane merges only Symphony-owned PRs that pass all configured gates.
16. Cleanup deletes only workspaces that are safe by current cleanup policy and records cleanup state.

During the attempt, Pi Symphony updates a compact local progress snapshot for the
issue so operators can inspect current phase, PR URL, review/check state, next
action, and artifact pointers without reading raw daemon or Agent logs.

## Outcome contract

### Success with PR Handoff

A successful implementation attempt must have a valid PR URL unless an explicit issue type or future spec allows a no-PR outcome. Missing PR output without `NEEDS_INFO` is not success.

### Needs Info

An Agent session may ask for Needs Info only when required requirements are missing or unsafe to infer. Needs Info must include the blocking question and enough context for the operator to answer.

### Human Review

Human Review is appropriate when automation produced a PR but cannot prove safety, scope, or evidence. Missing-evidence-only review failures with a PR may route to Human Review, but the evaluation must remain merge-ineligible until the blocker is resolved.

### Retry

Retry requires a concrete reason, retry budget state, and the input that changed since the previous attempt, such as new PR feedback or a repairable validation failure.

### Reconciliation-needed

Use reconciliation-needed when Linear, GitHub, SQLite, workspace, or artifact facts conflict and Pi Symphony cannot safely choose a destructive or externally visible action.

### Terminal failure

Terminal failure must include the failing phase, evidence pointer, and side effects already performed. It must not be recorded as success.

## Multi-agent behavior

- Each Agent session has a durable attempt identity, lease owner, heartbeat, workspace, branch, budget, and terminal outcome.
- Two Agent sessions must not claim the same Linear issue unless a future spec defines cooperative sub-attempts.
- Two Agent sessions must not mutate the same Workspace concurrently.
- A stale lease may be reclaimed only after heartbeat evidence and process/owner checks satisfy the configured stale policy.
- Parallel Agent sessions must share no implicit state through logs alone; status must report durable state.
- Merge and cleanup lanes must respect active leases and reconciliation-needed blockers.

### Scheduler parameter contract (runtime semantics)

- `max_concurrent_agents`:
  - Current CLI runtime behavior: one work lane processes one issue attempt at a time. This is effectively a concurrency limit of 1 regardless of configured value.
  - Default of `1` preserves current behavior.
  - Invalid/zero handling is delegated to configuration parsing, which currently falls back to `1` for missing/malformed/negative values.
- `max_turns`:
  - Current `pi_cli` runtime behavior: no continuation/session loop exists today, so missing, invalid, zero, or `1` resolves to exactly one implementation attempt for the selected issue.
  - A normalized value greater than `1` is unsupported for `pi_cli` and fails runtime preflight before claim, lease acquisition, workspace mutation, Linear state movement, or Agent execution.
  - The failure is an operator-facing configuration error that names `pi_cli`, the configured value, and the remediation: set `agent.max_turns: 1` or use a future session runtime with continuation support.
  - Future session-runtime Adapters may support this by declaring a `max_turns` capability and enforcing turns inside one durable session; the runner must not approximate multi-turn behavior by issuing multiple independent `pi_cli` attempts.
- `max_retry_backoff_ms`:
  - Current CLI runtime behavior: parsed for configuration storage only; no scheduler delay/backoff is applied before retry.
  - Default is `300000` ms.
  - Invalid/negative values fail configuration loading.

### Retry/backoff persistence and process restart expectations

- Current retry continuation is evidence-based (run/feedback artifacts), not scheduler-state-based:
  - `.pi-symphony-run.json` is the source for terminal outcome and PR URL reuse.
  - `.pi-symphony-feedback.md` is the source for whether a retry can continue on captured feedback.
- There is no persisted backoff timer state in the current runner that survives restart.
- A restart may still continue or re-attempt work based on preserved artifacts, but timing/backoff policy is not yet durable/portable across restarts.
- Session runtimes should interpret this as "retry timing is a no-op today"; when implemented, backoff state should move to durable local state (SQLite in the v1 orchestration target).

## Quality evidence

Each PR Handoff should include:

- issue identifier and scope summary;
- tests added or characterization evidence;
- validation commands and results;
- behavior-contract evidence or a statement that no behavior contract changed;
- changed files summary;
- known risks and out-of-scope items;
- review status and classification when review ran.
- progress status from `.symphony/state/run-progress/<issue>/progress.json` for active or recently terminal issue-level inspection.

## TDD expectation

- Bug fixes start with a failing or characterizing test when practical.
- Refactors start with characterization tests for behavior that could regress.
- New Modules expose an Interface that can be table-tested without running a full daemon loop.
- Tests should prefer behavior terms from `CONTEXT.md` and `LANGUAGE.md`.

## ACP Adapter target

ACP support is a Protocol Adapter target, not a replacement orchestration path.

The Adapter should:

- run as a separate agent process suitable for ACP-compatible clients;
- communicate through the Agent Client Protocol transport expected by the client;
- map editor turns to existing Pi Symphony command or session Modules;
- preserve workflow configuration, leases, budgets, validation, review, and state reconciliation;
- surface status, plans, diffs, validation output, and Handoff evidence in editor-friendly content;
- avoid editor-specific orchestration policy.

References:

- https://agentclientprotocol.com/overview/introduction
- https://zed.dev/docs/ai/external-agents

## Acceptance criteria for V1

- A fresh operator can run status and understand every active or blocked issue.
- The daemon can complete multiple issue attempts without duplicate claims or stale hidden work.
- Restarting the daemon does not lose ownership, retry, review, PR, merge, or cleanup decisions.
- A missing PR, invalid PR, failed validation, failed review, timeout, budget issue, or SQLite decision-store failure cannot be reported as a clean success.
- Merge lane decisions use current GitHub state and durable Pi Symphony blockers.
- Cleanup decisions are recorded and explain why each workspace was kept, dry-run eligible, deleted, or failed to delete.
- ACP, MCP, web UI, and cloud surfaces reuse the same core Modules instead of reimplementing policy.

