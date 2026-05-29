# Product surface control plane

## Goal

Provide a small read-only control-plane contract that product surfaces can use
without duplicating runner policy. The first surface is a local TUI; future web,
MCP, ACP, app, and dashboard surfaces should use the same typed contract shape.

## First slice

- Add a local `surface snapshot` command that returns a JSON read model.
- The read model is derived from runner-owned orchestration snapshot logic.
- The snapshot is read-only and must not claim issues, move Linear state, mutate
  workspaces, merge PRs, repair tasks, or clean up.
- The TUI is an Adapter over this snapshot. It may render state, refresh state,
  show typed logs, and show command hints, but it must not own orchestration
  policy.
- The no-mode CLI entrypoint launches the TUI by default. Mutating work remains
  behind explicit runner commands such as `start`, `worker`, `merge-approved`,
  `repair-artifacts`, and `cleanup-workspaces --apply`.
- The runner may launch a compiled `agent-machine-tui` helper when present.
  Source checkouts may fall back to Bun for local development.

## TUI Adapter

The OpenTUI Adapter renders the snapshot into an operator dashboard:

- Header: project identity, SQLite health, snapshot timestamp, and refresh
  errors.
- Summary: counts for issues, active locks, lanes, worker tasks, and
  reconciliation-needed worker tasks.
- Primary view: a flat prioritized issue work queue. Secondary diagnostics such
  as Overview, Lanes, Tasks, and Logs may exist, but they must be demoted behind
  the issue-centric default view.
- List pane: the flat prioritized issue work queue.
- Details pane: the selected-issue evidence pane.
- Logs or diagnostics views, when implemented: typed worker results and recent
  orchestration events from the snapshot. They must not stream raw Agent output
  or daemon stdout directly.

The Adapter owns only presentation state: selected row, optional active
secondary view, local refresh state, and terminal key handling. It must not
infer scheduler, handoff, retry, cleanup, merge, or repair decisions from
presentation state.

Keyboard controls are local UI controls only:

- `j`/`k` or up/down arrows move the selected row.
- `tab`, `h`/`l`, left/right arrows, or number keys may switch secondary views
  only when those views are implemented and visible in the footer.
- `r` refreshes the snapshot.
- `q` exits.

## Contract

The snapshot includes:

- schema version and observation timestamp;
- project/config identity;
- source precedence used for status facts;
- SQLite health and counts;
- issue attempt summaries;
- active lane summaries;
- worker task and worker result summaries;
- recent event summaries.

The snapshot intentionally excludes secrets and raw agent output.

## Issue-centric work queue

The default TUI model is a single flat issue work queue ordered by runner-owned
priority. The default navigation must not group issues into lanes as the primary
operator path. Lane, role, status, assignee, and repository filters may be added
as secondary views or local presentation controls, but they must not replace the
flat prioritized list as the default issue surface.

Each issue queue row must expose these stable fields:

- `issue_identifier`: Linear issue key such as `CAG-197`.
- `title`: current Linear issue title when available.
- `am_status`: synthesized Agent Machine status from runner-owned state, such
  as `reconciliation_needed`, `running`, `review_blocked`, `mergeable`,
  `queued`, `needs_info`, `done`, or `cleanup_only`.
- `lane_role_hint`: the runner lane or role most likely to act next, such as
  `work-lane`, `review`, `handoff`, `merge-lane`, `cleanup`, `linear-status`,
  `reconciliation`, or `operator`.
- `age`: how long the issue has been visible to Agent Machine or how long the
  current attempt has existed when an attempt is active.
- `updated_at`: latest relevant runner-owned or external observation timestamp
  used for this row.
- `attention`: a compact attention/blocker indicator with a stable code, for
  example `none`, `blocked`, `failed`, `stale`, `needs_info`,
  `reconciliation_needed`, or `operator_review`.

The initial flat priority order is:

1. `reconciliation_needed`: local, SQLite, Linear, GitHub, workspace, or
   artifact facts conflict and automation must not guess.
2. `failed_or_blocked_active_work`: active or recently claimed work is failed,
   blocked, stale, timed out, over budget, or missing required evidence.
3. `running_work`: currently claimed or running attempts, reviews, handoffs, or
   lane tasks with fresh ownership evidence.
4. `review_or_merge_blockers`: Human Review, review-not-ready, failed checks,
   changes-requested, ownership, merge-gate, or handoff blockers.
5. `mergeable`: Agent Machine-owned PRs that currently satisfy merge-lane gates
   or are waiting for the merge lane to act.
6. `queued_runnable`: runnable Linear issues or durable worker tasks waiting
   for capacity/backoff to clear.
7. `cleanup_only_or_done`: Done, terminal, cleaned, or cleanup-only work that is
   no longer blocking forward progress.

Rows in the same priority bucket should use deterministic tie-breakers from the
runner snapshot, such as explicit worker priority, active lease age, issue
priority, then oldest relevant timestamp. A surface must display the runner
provided order instead of recomputing scheduling, retry, merge, or cleanup
policy.

## Selected-issue evidence pane

Selecting an issue opens an evidence pane that answers four operator questions:
what is happening, why this issue is visible, what happens next, and what
evidence backs that up. The pane must be derived from the same read-only
snapshot and must not call Linear, GitHub, SQLite, or workspace files directly.

The selected issue contract includes these fields:

- `header`: issue identifier, title, `am_status`, current Linear workflow state,
  attempt number when known, PR/MR URL when mapped, and workspace path when
  relevant.
- `next_action`: runner-owned next action code and short display text, such as
  `wait_for_checks`, `run_review`, `handoff_pr`, `merge_pr`, `repair_state`,
  `ask_needs_info`, `retry_after_backoff`, `cleanup_workspace`, or
  `operator_review`.
- `blocker_reason`: concise runner-owned blocker or reason text. This should
  cite deterministic blocker codes when present, such as merge gate codes,
  stale lease reasons, scope guard failures, missing PR mapping, or
  reconciliation-needed causes.
- `current_activity`: active lane, worker task key, lease owner, heartbeat
  freshness, command phase, or idle/backoff state.
- `external_state`: current Linear state and relevant GitHub/GitLab PR/MR
  state, review decision, checks, mergeability, branch/base, and author
  ownership facts when the snapshot includes them.
- `agent_evidence_summary`: short bounded summary of recent AgentRuntime,
  implementation, review, validation, or handoff output. This field is evidence
  only and must not override runner-owned facts.
- `recent_events`: append-ordered recent orchestration events for the selected
  issue, including event type, timestamp, source component, stable reason code
  when present, and artifact pointer when detailed evidence is external.

The pane may include links or pointers to run records, evaluation artifacts,
debug artifacts, PR comments, Linear comments, and capped logs. These pointers
are evidence and diagnostics; they are not independent authority for current
status when runner-owned state is available.

## Fact authority

Product surfaces must preserve the runner and Agent responsibility boundary from
the harness behavior spec. The snapshot should label facts by source or make the
source precedence explicit enough that a renderer can avoid presenting raw text
as a decision.

Runner-owned synthesized facts include:

- `am_status`, `next_action`, priority bucket, attention code, blocker codes,
  retry/backoff decision, merge eligibility, cleanup eligibility, review
  readiness, reconciliation-needed state, lease/heartbeat ownership, and
  source precedence.
- Runner-owned facts come from core runner Modules and durable state/read-model
  logic. Product surfaces may render them but must not derive substitute policy
  from local presentation state, raw logs, or agent prose.

Raw or bounded Agent output includes:

- implementation summaries, review summaries, validation excerpts, advisory PR
  hints, terminal output summaries, and pointers to capped debug logs.
- These fields are secondary evidence. Raw logs, daemon stdout, runtime JSONL,
  or Agent prose must not replace deterministic runner facts, must not be used
  as the only source for issue visibility or next action, and must not drive
  scheduling, retry, merge, cleanup, repair, or Linear/GitHub transitions.

Product surfaces may use `AM_BIN` to call a built runner binary. The command
contract remains `surface snapshot --config <path>`. The runner may use
`AM_TUI_BIN` or a sibling `agent-machine-tui` executable to launch a compiled
TUI helper before falling back to a source-checkout Bun command.

## Future commands

Mutating controls should be added as typed runner commands with explicit
preflight, dry-run, confirmation, and result contracts before any surface renders
them as buttons, shortcuts, HTTP endpoints, or app actions.

Examples:

- start or stop daemon lanes;
- requeue a reconciliation-needed worker task;
- run cleanup with `apply`;
- trigger merge-lane evaluation.

Each mutating command must continue to use runner Modules for leases, state
reconciliation, Linear/GitHub refreshes, handoff validation, cleanup, retry, and
merge gates.

## Non-goals

- No web server in the first TUI slice.
- No direct TUI calls to Linear or GitHub.
- No TUI-owned scheduling, retry, merge, cleanup, or repair policy.
- No terminal-key shortcut that performs destructive work without a typed
  control-plane command and confirmation contract.
