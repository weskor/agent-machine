# Agent development loop

Use this loop for Pi Symphony changes, especially runner behavior, architecture, state, review policy, merge policy, and multi-agent work.

## 1. Start from the contract

Read these before planning:

- `docs/vision/pi-symphony-v1.md`
- `CONTEXT.md`
- `LANGUAGE.md`
- relevant files in `docs/specs/`
- relevant files in `docs/adr/`
- the Linear issue contract

If the desired behavior is not represented in a spec, update or add a spec before implementing the behavior. If the change records a durable design decision, add an ADR.

## 2. Make the slice small

Prefer one focused Linear issue per behavior or Module change. A good issue states:

- Goal
- Scope
- Requirements
- Acceptance Criteria
- Validation
- Allowed paths
- Out-of-scope paths

Avoid combining a behavior change with a broad mechanical refactor.

## 3. Test first when behavior can regress

- Add a failing test for bugs.
- Add characterization tests before refactors.
- Use table-driven tests for outcome Modules, reconciliation Modules, merge gates, and CLI mode dispatch.
- Keep tests at the Module Interface when possible.

## 4. Implement behind a deep Module

Use the terms in `LANGUAGE.md`.

Good extraction candidates hide policy behind a small Interface:

- Run attempt outcome
- Candidate reconciliation
- Merge gate decision
- Run evaluation classification
- SQLite state projection
- CLI mode dispatch
- Protocol Adapter

Callers should not need to know hidden ordering rules, side effects, fallback defaults, or status vocabulary.

## 5. Preserve or update behavior evidence

Every broad refactor or behavior change should state one of:

- behavior preserved, with characterization or parity evidence;
- behavior intentionally changed, with spec/ADR update;
- behavior uncertain, with Human Review or Needs Info requested.

PR Handoff should include Behavior Contract Evidence, validation commands, and known risks.

## 6. Validate before handoff

Run the standard gates when Go is available through mise:

```bash
mise exec go -- make ci
git diff --check
```

Add focused smoke checks when changing status, cleanup, merge, backfill, daemon, or CLI mode behavior.

## 7. Escalate instead of guessing

Use Needs Info or Human Review when:

- the issue contract is incomplete;
- Linear, GitHub, SQLite, workspace, or artifact facts conflict;
- the Agent session cannot prove behavior-contract safety;
- a PR exists but review evidence is missing or ambiguous;
- a subagent or tool-surface mismatch prevents requested evidence from being produced.

## 8. Keep future surfaces as Adapters

ACP, MCP, web UI, and cloud runner work must reuse core orchestration Modules. They should not create separate policy for issue claiming, validation, review, merge, retry, cleanup, or reconciliation.

