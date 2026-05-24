# Session Runtime Contract

## Goal

Enable `agent.max_turns > 1` through one persistent runtime session instead of
multiple independent shell attempts.

## Scope

This contract defines the next runtime shape for Pi Symphony. It does not make
`max_turns > 1` valid for `codex_cli` or `pi_cli`; those remain one-shot shell
providers. A selectable session provider must satisfy this contract before the
runner may execute multi-turn attempts.

## Provider Shape

The first target session provider is a Codex app-server Adapter:

- provider name: `codex_app_server`
- process surface: `codex app-server --listen stdio://` or an equivalent
  configured app-server endpoint
- protocol: JSON-RPC with `thread/start` for one attempt session and
  `turn/start` for each runner turn
- thread source: `pi_symphony`

The app-server provider is distinct from `codex_cli`. `codex_cli` uses
`codex exec` as a one-shot command and must continue to reject
`agent.max_turns > 1` during preflight.

Until the production app-server stdio client can wait for a completed turn and
project typed results into the runner, `codex_app_server` must fail closed during
preflight in normal runner execution. Tests may inject an app-server client to
exercise the Adapter seam without claiming Linear work.

## Turn Semantics

For one implementation attempt, the runner starts exactly one runtime thread.
The thread ID is attempt evidence and must be persisted in progress/SQLite state
before the first turn result can drive further decisions.

Turn 1 sends the normal implementation prompt. Later turns may be sent only when
the previous turn returns typed continuation evidence. The runner must not infer
continuation from vague prose, logs, raw debug output, or missing PR URLs.

Required continuation envelope fields:

- `runtime_outcome`: `needs_continuation`
- `continuation_prompt`: bounded prompt text for the next turn
- `reason`: concise operator-facing reason
- `changed_files`: optional evidence summary

Terminal runtime outcomes stop the turn loop immediately. If the final allowed
turn still requests continuation, the runner records a failed attempt with a
budget/configuration explanation that names `agent.max_turns`.

## Max-Turns Enforcement

Session providers declare all of these capabilities:

- `SupportsSessions`
- `SupportsTurnContinuation`
- `SupportsMaxTurns`

Only providers with all three capabilities may accept `agent.max_turns > 1`.
The runner owns the turn counter and must pass the current turn number and limit
to each session turn. Providers may enforce stricter internal limits, but they
must report them as preflight failures before claim or workspace mutation.

## Runner Boundaries

The session runtime owns execution only. The runner still owns:

- Candidate selection, leases, and workspace isolation
- Linear state movement
- validation hooks
- scope guard
- Git/PR handoff
- semantic review routing
- run records, evaluation artifacts, comments, and merge gates

A session turn may produce PR hints, summaries, usage, and continuation
requests. Those are inputs to runner-owned validation, not authority.

## Recovery

SQLite is the target source of truth for thread and turn recovery. Until the
SQLite decision class is authoritative, progress JSON may expose thread and turn
IDs for operator diagnosis, but it must not make scheduling decisions by itself.

Minimum persisted evidence:

- attempt ID
- provider
- runtime thread ID
- current turn number
- max turns
- last turn ID
- last terminal or continuation envelope

On process restart, a future session provider may resume an active thread only
when the durable lease still belongs to the current attempt and the provider can
prove the thread belongs to the same workspace, issue, branch, and provider
configuration.

## Non-Goals

- Do not approximate multi-turn behavior by launching independent `codex exec`
  or `pi` attempts.
- Do not let runtime continuation bypass review, handoff, or merge policy.
- Do not use app-server thread state as the source of truth for runner
  scheduling decisions.
