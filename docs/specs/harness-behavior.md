# Harness Behavior Spec

This spec captures current observable Pi Symphony runner behavior. Update it when a ticket intentionally changes runner behavior; cite it when a refactor only moves code.

## Configuration loading

- The CLI defaults to `WORKFLOW.md` unless another workflow path is supplied.
- The runner loads `.env.local` from the current directory, then the nearest `.env.local` for the workflow path.
- `LINEAR_API_KEY` is required.
- `tracker.project_slug` and `workspace.root` are required in the workflow.
- GitHub repository context is configured from the workflow before GitHub API use.
- Budget settings from the workflow control command, Pi, review, merge, GitHub, token, cost, and wall-clock limits.

## CLI modes

- Default and `--once`: attempt one eligible Linear issue with `runOne`.
- `--continuous` / `--daemon`: run merge and work lanes until canceled, or until `--cycles=N` completes N cycles per lane.
- `--merge-approved`: merge eligible Symphony-owned PRs whose gates pass.
- `--cleanup-workspaces`: inspect workspace cleanup eligibility; `--apply` deletes eligible workspaces.
- `--repair-artifacts`: repair local Symphony artifacts.
- `--status`: print runner/workspace status for the workflow.

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
- A claimed issue is moved to the configured running state, usually `In Progress`.
- If the implementation outputs `NEEDS_INFO`, the issue moves to the configured needs-info state and receives the questions as a Linear comment.

## Workspace lifecycle

- Each issue runs in `.symphony/workspaces/<issue-identifier>`.
- The workspace branch is `symphony/<issue-identifier>-workspace`.
- The runner creates an on-disk run lock before changing issue state or mutating the workspace.
- The runner clones the configured base branch and switches to the expected workspace branch.
- Configured pre-run and post-run validation hooks execute in the workspace.
- Completed workspaces become cleanup candidates when the Linear issue is Done and local artifacts indicate completion, failure, or review failure according to cleanup policy.

## Pi implementation attempt

- The implementation prompt includes the workflow body, Linear issue description, ticket-contract preflight, behavior-contract preflight, PR feedback when present, and runner constraints.
- The agent must create or update exactly one PR from the expected workspace branch into the configured base branch.
- The agent should stop after scoped diff, validation notes, and PR handoff.
- The runner parses Pi usage and the first configured-repository GitHub PR URL from the output.
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
- Successful merge deletes the Symphony workspace branch and moves the Linear issue to Done.
- Blocked merges should explain the gate reason instead of forcing a merge.

## Failure and artifact behavior

- Every attempt writes a run record with issue, workspace, branch, timing, usage, review, PR URL, status, budget, and behavior-contract evidence fields when possible.
- Evaluation artifacts classify dogfood outcomes and suggested improvements.
- Command timeouts and budget failures produce failure status and, when possible, Linear comments.
- Run locks are released when the attempt exits.
- Secrets, GitHub App private keys, and `.env.local` files must stay untracked.
