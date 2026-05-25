# AgentRuntime interface contract

## Goal

Provide a machine-readable contract that allows Agent Machine to drive Pi CLI today
and future runtime adapters (fake/test, API, ACP-style, or MCP-style) without
embedding orchestration logic in runtime execution code.

## Scope

In this ticket, scope is limited to contract definition and docs/tests:

- `docs/specs/agentruntime-interface-contract.md` (this spec)
- `internal/agentruntime/contract.go` (public interface and data types)
- `internal/agentruntime/contract_test.go` (contract-level test coverage)

This documentation slice preserves production behavior. No runtime behavior in
`run_one.go`, `reconciler.go`, merge lane, or cleanup logic is changed in this
ticket.

## Contract

### Runtime responsibilities (executor only)

The `AgentRuntime` interface is an **execution adapter contract** only. It is
not a policy engine.

### Supported provider vocabulary

Runtime provider names identify Adapter choices at the AgentRuntime seam. They
must not be used as architecture names for the runner itself.

- `codex_cli`: the default local shell Adapter. It shells to `codex exec`, reads
  the prepared prompt file through stdin, and supports the same one-shot
  implementation and review attempt shape as `pi_cli`. Users therefore need
  `codex` installed and configured on `PATH` for the default production runtime.
  That dependency should fail during runner preflight before claiming or mutating
  work, not after a workspace or Linear issue has been changed.
- `pi_cli`: the legacy local Adapter. It shells to the local `pi` executable and
  remains available as an explicit runtime provider for operators that opt into
  it.
- `fake`: deterministic fake/test runtime used by tests and characterization
  scenarios. It should exercise the same AgentRuntime contract and handoff
  evidence paths without requiring network, auth, or an installed Agent CLI.
- Future provider names may include `api`, `acp`, or `mcp` style
  Adapters. Those names describe transport or process shape only; they must call
  the same runner Modules for orchestration, ownership, validation, and handoff.

`pi_cli` is an Adapter choice, not Agent Machine's architecture. Adding another
provider must not move issue claiming, branch/PR validation, lifecycle state,
handoff comments, merge gates, or cleanup policy into provider-specific prompts.
Local one-shot CLI providers should share the same shell Adapter implementation
and differ only by provider name, command shape, and parser hooks.

### Runtime preflight contract

Before mutating a workspace, acquiring a run lease or externally claiming work,
or moving a Linear issue, the selected runtime provider exposes a preflight
result with actionable failures. The result includes the provider name, checked
command(s), resolved executable path when available, prerequisite status, and a
configuration error that does not expand environment variables or leak secrets.
For shell CLI providers, this includes:

- binary availability and executable path for the configured implementation
  command;
- binary availability and executable path for the configured review command when
  review is enabled;
- auth/config discoverability where feasible without leaking secrets;
- selected provider/model visibility when the runtime can report it;
- quota or account readiness when cheaply discoverable;
- explicit failure messages that tell the operator what to install, configure,
  login to, or select.

Preflight must be best-effort where runtime CLIs do not expose stable auth/model
inspection commands, but a missing configured executable for the selected shell
CLI provider is a hard pre-claim failure.
The runner should record the selected provider and visible model/config evidence
in artifacts or orchestration state when available.

The runtime adapter must implement:

1. Runtime preflight (`Preflight`) before claim, lease, workspace mutation, or
   Agent execution.
2. Attempt lifecycle start (`StartAttempt`).
3. Attempt execution (`RunAttempt`) that produces a terminal outcome, usage, and
   optional PR URL.
4. Review execution (`ReviewAttempt`) when available.
5. Cancel/stop hooks (`Cancel`, `Stop`) where supported by the adapter.
6. Event emission throughout execution.

Runtime providers declare capabilities instead of relying on caller guesses:

| Capability | Meaning |
| --- | --- |
| `implementation_run` | Can perform an implementation attempt. |
| `review_run` | Can perform a semantic review attempt. |
| `usage_cost_reporting` | Can report token, cost, or other usage telemetry. |
| `timeout_cancellation` | Can enforce timeout and/or cancellation signals. |
| `structured_output` | Can emit typed outcomes/events without text scraping. |
| `raw_debug_capture` | Can expose raw streams for capped debug artifacts. |
| `deterministic_handoff_support` | Can provide machine-readable PR/handoff hints, while the runner still validates and owns handoff. |

Unsupported capabilities must be explicit. If future runtime providers need
turn or iteration limits, that behavior needs a concrete provider contract and
must not be approximated by repeatedly shelling out to a one-shot CLI runtime.

### Deterministic handoff boundary

AgentRuntime output may include a PR URL, branch name, summary, usage, and debug
evidence, but those are inputs to runner validation, not authority. Wherever the
runner can perform the operation with configured Git/GitHub credentials and typed
project facts, the runner owns:

- commit creation or validation of the exact diff to hand off;
- push to the expected `am/<issue>-workspace` branch;
- PR create/update against the configured repository and base branch;
- PR URL resolution and validation by repository, branch, base, issue identifier,
  author/owner policy, and current attempt;
- artifact recording for run result, usage, validation, review, PR identity, and
  handoff evidence.

The Agent should focus on code/test/doc changes and semantic explanation, then
stop without creating, pushing, updating, or commenting on a PR. Any PR URL or
handoff instruction produced by an Agent or runtime is advisory compatibility
input until the runner validates it against GitHub facts.

Unsupported operations must fail with a typed `UnsupportedOperation` error (for
example `cancel` on a runtime that has no cancellation primitive).

### Data contract

- `AttemptContext`: identifies a single logical attempt.
- `AttemptTimeouts`: wall-clock / command / review timeout budgets.
- `AttemptResult`: terminal outcome, PR URL, usage telemetry, structured
  outcome envelope, error text, and timing.
- `AttemptUsage`: parsed token/cost telemetry for cost governance.
- `ReviewResult`: optional review status/classification/findings/output usage.
- `RuntimeEvent`: stable event stream with typed event names and payload.

Shell-style runtime adapters accept an explicit structured outcome marker in
stdout:

```text
AM_OUTCOME: {"runtime_outcome":"needs_info","needs_info_questions":["Which tenant?"],"validation":["go test ./..."],"pr_url":"https://github.com/owner/repo/pull/123"}
```

When this line is present, the adapter parses it as `AttemptOutcomeEnvelope` and
the runner prefers its typed fields over legacy text scraping. Missing marker
lines preserve compatibility with existing plain-text output. Malformed marker
JSON is a runtime execution failure, not a silent fallback, because the Agent
attempted to provide typed orchestration evidence and produced an invalid
contract.

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
| `RunAttempt` | `captureAgentOutput(...)` with the configured runtime command and
  timeout `Budget.RuntimeTimeout` (`Budget.PiTimeout` remains a legacy alias). |
| `RuntimeEvent` | Structured event equivalents for command start, finished, timeout,
  and terminal outcome (to be produced by adapter implementation). |
| `AttemptUsage` | `parseUsage(piOutput)` output currently stored on
  `runRecord.RuntimeUsage`. |
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
