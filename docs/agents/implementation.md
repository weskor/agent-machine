# Implementation agent guidance

Use this guidance for Agent sessions that change Pi Symphony code, behavior, specs, or architecture.

## Required reading

Before planning, read:

- `AGENTS.md`
- `CONTEXT.md`
- `LANGUAGE.md`
- `docs/vision/pi-symphony-v1.md` for roadmap or architecture work
- relevant specs in `docs/specs/`
- relevant ADRs in `docs/adr/`
- the Linear issue contract

## Implementation posture

- Keep the change focused on the issue.
- Prefer tests or characterization before implementation.
- Preserve observable behavior unless the issue and spec explicitly require a change.
- Update specs when behavior changes.
- Add an ADR when the design decision is durable, surprising, or trade-off heavy.
- Avoid broad mechanical moves in the same PR as behavior changes.
- Do not ask the Agent to decide runner-owned invariants that can be computed from typed state or external facts. Use the runner-vs-Agent boundary in `docs/specs/harness-behavior.md` when deciding whether a check belongs in code, tests, or reviewer judgment.

## Agent-owned judgment

Implementation Agents own non-deterministic work: choosing the implementation approach, editing code/tests/docs, deciding useful characterization coverage, assessing abstraction quality, and explaining ambiguous repair options. They may cite deterministic evidence, but should not be the final authority for issue contract syntax, path scope, PR ownership, PR URL validity, lifecycle transitions, outcome classification, leases, merge gates, cleanup eligibility, artifact locations, or evidence schema validity.

## Behavior Contract Evidence

Broad refactors, state-machine changes, review policy changes, merge policy changes, cleanup changes, and multi-agent changes should include Behavior Contract Evidence in the PR Handoff.

Evidence should state:

- relevant specs and ADRs read or updated;
- existing behavior inventory;
- preserved behavior;
- intentionally changed behavior;
- tests or characterization added;
- validation commands run;
- known risks and out-of-scope items.

## When to stop

Stop and ask for Needs Info or Human Review when:

- the issue contract is incomplete;
- behavior cannot be determined from docs or tests;
- Linear, GitHub, SQLite, workspace, or artifact facts conflict;
- the requested change would exceed the allowed scope;
- required evidence cannot be produced because of tool or environment limits.

