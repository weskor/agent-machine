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
  and show command hints, but it must not own orchestration policy.

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
