# AgentRuntime interface contract

## Goal

Provide a machine-readable contract that allows Pi Symphony to drive Pi CLI today
and future runtime adapters (ACP/MCP/app-server) without embedding orchestration
logic in runtime execution code.

## Scope

In this ticket, scope is limited to contract definition and docs/tests:

- `docs/specs/agentruntime-interface-contract.md` (this spec)
- `internal/agentruntime/contract.go` (public interface and data types)
- `internal/agentruntime/contract_test.go` (contract-level test coverage)

No runtime behavior in `run_one.go`, `reconciler.go`, merge lane, or cleanup logic
is changed in this ticket.

## Contract

### Runtime responsibilities (executor only)

The `AgentRuntime` interface is an **execution adapter contract** only. It is
not a policy engine.

The runtime adapter must implement:

1. Session/attempt lifecycle start (`StartAttempt`).
2. Attempt execution (`RunAttempt`) that produces a terminal outcome, usage, and
   optional PR URL.
3. Review execution (`ReviewAttempt`) when available.
4. Cancel/stop hooks (`Cancel`, `Stop`) where supported by the adapter.
5. Event emission throughout execution.

Unsupported operations must fail with a typed `UnsupportedOperation` error (for
example `cancel` on a runtime that has no cancellation primitive).

### Data contract

- `AttemptContext`: identifies a single logical session/attempt.
- `AttemptTimeouts`: wall-clock / command / review timeout budgets.
- `AttemptResult`: terminal outcome, PR URL, usage telemetry, error text, and
  timing.
- `AttemptUsage`: parsed token/cost telemetry for cost governance.
- `ReviewResult`: optional review status/classification/findings/output usage.
- `RuntimeEvent`: stable event stream with typed event names and payload.

### Error contract

- `RuntimeError` is used for runtime failures with a concrete `Kind`.
- `UnsupportedOperation` is used for explicit unsupported capabilities.
- `Stop` and `Cancel` operations must return `UnsupportedOperation` when the
  adapter cannot perform them.

### Required outcomes/events

Implementations should emit at least:

- `attempt_started`
- `attempt_run_started`
- `attempt_run_finished`
- `attempt_terminal_outcome`

Optional where supported:

- `review_started`
- `review_finished`
- `run_canceled`
- `run_stopped`

`AttemptOutcome` terminal values are:

- `success`
- `failed`
- `review_failed`
- `needs_info`
- `needs_info_failed`
- `timeout`
- `budget_exceeded`

## Mapping to current Pi CLI behavior

### How existing code maps

The current Pi CLI flow in `run_one.go` maps to the contract as follows:

| Contract concept | Pi CLI behavior in `run_one.go` |
| --- | --- |
| `StartAttempt` | Workspace/attempt context creation (`safeWorkspacePath`, lock,
  `expectedWorkspaceBranch`, branch detection). |
| `AttemptContext` | Workspace path, branch, issue id/identifier, attempt number, and
  timeout budget (`config.Budget`). |
| `RunAttempt` | `captureAgentOutput(...)` with command `PI_COMMAND @<prompt>` and
  timeout `Budget.PiTimeout`. |
| `RuntimeEvent` | Structured event equivalents for command start, finished, timeout,
  and terminal outcome (to be produced by adapter implementation). |
| `AttemptUsage` | `parseUsage(piOutput)` output currently stored on
  `runRecord.PiUsage`. |
| `AttemptResult` | Terminal status currently represented by
  `runAttemptStatus*` and persisted through `runRecord`. |
| `ReviewAttempt` | `runReview(...)` when `config.ReviewCommand != ""` and run
  produced a PR URL. |
| `Stop/Cancel` | Not currently exposed by `run_one.go`; should remain explicit
  no-op with `UnsupportedOperation` until orchestration policy requires support. |

### Behavioral mapping guidance

- `sh.ErrCommandTimeout` and budget checks map to `AttemptOutcomeTimeout` or
  `AttemptOutcomeBudgetExceeded` and `RuntimeErrorKindTimeout`.
- Missing PR URL / pre-run command failures / PR handoff/validation failures map to
  `AttemptOutcomeFailed`.
- Review command failures with no pass map to `AttemptOutcomeReviewFailed`.
- Review results with `needs_info` classification route to `AttemptOutcomeNeedsInfo`.

## Non-goals

- No change to scheduler policy, lock semantics, review heuristics, handoff
  transitions, or status mapping logic.
- No behavior re-implementation in this ticket.

