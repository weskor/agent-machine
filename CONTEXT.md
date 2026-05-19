# Pi Symphony

Pi Symphony is a runner harness for taking one well-scoped Linear issue through an isolated workspace, a Pi implementation attempt, optional review, and GitHub PR handoff.

## Language

**Pi Symphony**:
The standalone runner harness in this repository. It coordinates Linear, local workspaces, Pi CLI execution, review, GitHub PRs, merge gates, and cleanup.
_Avoid_: compound-web runner, generic bot.

**Workflow**:
The repository-local `WORKFLOW.md` configuration that tells Pi Symphony which Linear project, workspace root, branch, commands, hooks, states, and budgets to use.
_Avoid_: pipeline, job config.

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

**Evaluation artifact**:
The `.pi-symphony-evaluation.json` artifact derived from the run record to classify dogfood outcomes and improvement signals.
_Avoid_: metrics file.

**Handoff**:
The transition after Pi opens or updates a PR: validate the PR, optionally review it, post/update deterministic PR and Linear comments, and move the Linear issue to Human Review.
_Avoid_: completion, done.

**Work lane**:
The daemon lane that repeatedly attempts one runnable Linear issue via `runOne`.
_Avoid_: worker when referring to the named daemon lane.

**Merge lane**:
The daemon lane that cleans completed workspaces and merges approved Symphony-owned PRs.
_Avoid_: janitor, sweeper.

**Behavior contract**:
The observable promises a runner change must preserve or deliberately update: inputs, outputs, state transitions, side effects, cleanup, error handling, security/ownership assumptions, timeouts, and hidden operational contracts.
_Avoid_: vague “it still works” statements.

## Example dialogue

Dev: “Can we split the runner?”

Maintainer: “Yes, but cite the harness behavior spec. If the split is mechanical, the Behavior Contract Evidence should say which workflows, state transitions, locks, handoff steps, and cleanup paths are preserved. If behavior changes, update the spec and add an ADR when the decision is durable.”
