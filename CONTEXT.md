# Pi Symphony

Pi Symphony is a runner harness for taking one well-scoped Linear issue through an isolated workspace, an Agent implementation attempt, optional review, and GitHub PR handoff.

## Language

**Pi Symphony**:
The standalone runner harness in this repository. It coordinates Linear, local workspaces, Pi CLI execution, review, GitHub PRs, merge gates, and cleanup.
_Avoid_: compound-web runner, generic bot.

**Project config**:
The repository-local `symphony.yaml` plus `symphony.agent.md` prompt that tell Pi Symphony which Linear project, repository, workspace root, branch, runtime, hooks, states, budgets, and agent instructions to use.
_Avoid_: config file, pipeline, job config.

**Linear issue**:
The source work item. A runnable issue must be fully specified with Goal, Scope, Requirements, Acceptance Criteria, and Validation sections.
_Avoid_: ticket when precision matters, GitHub issue.

**Candidate**:
An eligible Linear issue in an active state that Pi Symphony may claim for a run. Candidate ordering is part of the runner behavior contract.
_Avoid_: task, job.

**Workspace**:
An isolated git clone under `.symphony/workspaces/<issue-identifier>` used for exactly one issue attempt.
_Avoid_: checkout, temp dir.

**Run lock**:
A JSON lock under `.symphony/workspaces/.pi-symphony-locks/` that prevents concurrent runs from claiming the same Linear issue.
_Avoid_: mutex when referring to the on-disk artifact.

**Run record**:
The `.pi-symphony-run.json` artifact written in the workspace to describe the issue, branch, timing, usage, review result, PR URL, status, and behavior-contract evidence for one attempt.
_Avoid_: log, summary.

**Agent session**:
A bounded Pi or review process that works on one attempt inside a workspace, with its own prompt, budget, output, validation, and terminal outcome.
_Avoid_: generic bot run when referring to the bounded attempt process.

**Evaluation artifact**:
The `.pi-symphony-evaluation.json` artifact derived from the run record to classify dogfood outcomes and improvement signals.
_Avoid_: metrics file.

**Orchestration state**:
The durable local state Pi Symphony uses to remember attempts, PR mappings, review classifications, retry decisions, merge blockers, cleanup decisions, leases, heartbeats, and terminal outcomes.
_Avoid_: cache when the state is authoritative for runner decisions.

**Reconciliation-needed**:
A safe blocked state used when Linear, GitHub, SQLite, workspace, or artifact facts conflict and Pi Symphony cannot choose a destructive or externally visible action without operator or repair logic.
_Avoid_: unknown failure, flaky state.

**Handoff**:
The transition after Pi opens or updates a PR: validate the PR, optionally review it, post/update deterministic PR and Linear comments, and move the Linear issue to Human Review.
_Avoid_: completion, done.

**Work lane**:
The implementation-domain lane that claims queued implementation worker tasks and executes runnable issue attempts under durable worker task ownership.
_Avoid_: worker when referring to the named daemon lane.

**Merge lane**:
The daemon lane that cleans completed workspaces and merges approved Symphony-owned PRs.
_Avoid_: janitor, sweeper.

**ACP Adapter**:
A future Protocol Adapter that lets ACP-compatible editors communicate with Pi Symphony while keeping orchestration policy in the core runner Modules.
_Avoid_: Zed-specific core logic.

**Product surface**:
A user-facing or integration layer such as ACP, MCP, web UI, cloud runner, or dashboard. Product surfaces should call core Modules instead of owning orchestration policy.
_Avoid_: platform when the layer is only an Adapter or UI.

**Behavior contract**:
The observable promises a runner change must preserve or deliberately update: inputs, outputs, state transitions, side effects, cleanup, error handling, security/ownership assumptions, timeouts, and hidden operational contracts.
_Avoid_: vague “it still works” statements.

**Runner invariant**:
A deterministic fact, gate, or transition Pi Symphony can compute from project configuration, Linear, GitHub, SQLite, workspace metadata, or typed artifacts. Runner invariants belong in runner Modules and specs, not in Agent judgment.
_Avoid_: asking an LLM to decide parseable scope, ownership, lifecycle, merge, cleanup, lease, or artifact facts.

**Agent judgment**:
The non-deterministic implementation, review, and repair reasoning an Agent session performs when facts are ambiguous or semantic quality matters.
_Avoid_: treating Agent judgment as authority for deterministic runner invariants.

## Example dialogue

Dev: “Can we split the runner?”

Maintainer: “Yes, but cite the harness behavior spec. If the split is mechanical, the Behavior Contract Evidence should say which config inputs, state transitions, locks, handoff steps, and cleanup paths are preserved. If behavior changes, update the spec and add an ADR when the decision is durable.”
