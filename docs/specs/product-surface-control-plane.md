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

## TUI Adapter

The OpenTUI Adapter renders the snapshot into an operator dashboard:

- Header: project identity, SQLite health, snapshot timestamp, and refresh
  errors.
- Summary: counts for issues, active locks, lanes, worker tasks, and
  reconciliation-needed worker tasks.
- Views rail: Overview, Issues, Lanes, Tasks, and Logs.
- List pane: rows for the active view.
- Details pane: stable key/value fields for the selected row.
- Logs view: typed worker results and recent orchestration events from the
  snapshot. It must not stream raw Agent output or daemon stdout directly.

The Adapter owns only presentation state: active view, selected row, local
refresh state, and terminal key handling. It must not infer scheduler,
handoff, retry, cleanup, merge, or repair decisions from presentation state.

Keyboard controls are local UI controls only:

- `tab`, `h`/`l`, or left/right arrows switch views.
- `j`/`k` or up/down arrows move the selected row.
- `1`-`5` jump to Overview, Issues, Lanes, Tasks, or Logs.
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

Product surfaces may use `AM_BIN` to call a built runner binary. The command
contract remains `surface snapshot --config <path>`.

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
