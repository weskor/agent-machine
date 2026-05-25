# Review agent guidance

Use this guidance for Agent sessions that review Agent Machine PRs, plans, or architecture changes.

## Review sources of truth

Use these in order:

1. Linear issue contract.
2. `docs/specs/` behavior contracts.
3. `docs/adr/` durable decisions.
4. `CONTEXT.md` domain language.
5. `LANGUAGE.md` architecture vocabulary.
6. Existing tests and artifacts.
7. PR description and Behavior Contract Evidence.

## Review posture

- Separate behavior/spec blockers from missing evidence.
- Treat green CI as necessary but not sufficient for broad runner changes.
- Prefer precise, actionable findings over broad concern language.
- Do not require unrelated specs for a narrow change.
- Do require docs/spec updates when observable behavior intentionally changes.
- Route ambiguity to Human Review instead of encouraging blind retry.
- Check whether the PR leaves deterministic runner invariants to Agent or reviewer judgment. Repeatable checks over issue contracts, paths, PR ownership, lifecycle outcomes, leases, merge gates, cleanup, artifact locations, or evidence schemas should be implemented or tracked as runner checks, not normalized as prompt responsibility.

## Hard blockers

Fail review for:

- behavior that contradicts a spec or ADR without updating it;
- dropped state transitions, side effects, cleanup, locks, review, handoff, merge, retry, or timeout behavior;
- missing tests for risky behavior changes;
- unsafe ownership, repository, branch, secrets, or credential handling;
- broad scope drift beyond the Linear issue.

## Missing evidence

Missing evidence is not the same as broken behavior. If a PR is otherwise plausible and has a PR URL, classify missing-evidence-only failures so Agent Machine can route to Human Review instead of retrying blindly.

## Architecture review

When reviewing architecture, use `LANGUAGE.md` terms:

- Module
- Interface
- Implementation
- Depth
- Seam
- Adapter
- Leverage
- Locality

Recommend small Linear issues with allowed paths, out-of-scope paths, expected tests, and behavior-contract implications.

When a review finds an ambiguous seam, prefer a follow-up implementation slice that turns the seam into a typed runner invariant. Keep semantic concerns such as abstraction quality, maintainability, and ambiguous repair reasoning with the Agent or human reviewer.

