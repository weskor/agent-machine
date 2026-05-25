# Review Policy

Agent Machine review should distinguish broken behavior from missing evidence, but both must be visible before merge.

## Required evidence for refactors, replacements, and rewrites

PRs that move or reshape runner behavior should include a `Behavior Contract Evidence` section with:

- relevant `docs/specs/` and `docs/adr/` references;
- existing-behavior inventory: inputs, outputs, state transitions, side effects, cleanup, error handling, security/ownership assumptions, timeouts, and hidden operational contracts;
- parity checklist: preserved behavior, intentionally changed behavior with issue-backed justification, and unknown behavior that needs clarification;
- characterization or behavior-driven test evidence for observable behavior;
- complexity/LOC budget: expected files touched, LOC direction, why net growth is acceptable, code removed, and when to split.

## Review outcomes

- **Behavior/spec mismatch**: blocker. Fail the review unless the spec and ticket explicitly justify the change.
- **Missing evidence on a broad refactor**: blocker for automated merge/handoff confidence. Move to Human Review rather than retrying blindly when CI is green.
- **Pure mechanical move with green CI**: acceptable only when the PR cites relevant specs/ADRs and states that no spec changes were needed.

## Reviewer posture

Use specs and ADRs as the contract source. Do not require speculative docs for unrelated areas, but do require explicit evidence for code that changes runner modes, Linear state movement, workspace lifecycle, locks, review, PR handoff, merge gates, budgets, or cleanup.
