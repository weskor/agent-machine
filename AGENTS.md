# Agent Machine agent guidance

Work in small, reviewable changes. Use `mise exec go -- make ci` and `git diff --check` before handoff when Go is available through mise.

For runner behavior changes, read the relevant specs first and preserve observable behavior unless the ticket explicitly asks for a behavior change.

Read `docs/vision/agent-machine-v1.md` before planning roadmap, architecture, multi-agent, editor Adapter, MCP, web UI, cloud, or other product-surface work.

Use the development loop in `docs/agents/development-loop.md`: spec/ADR first for durable behavior, TDD or characterization before behavior-risky refactors, then the smallest implementation slice.

## Agent skills

### Issue tracker

Issues are tracked in Linear in the CAG / Agent Machine Runner project; GitHub is used for PR handoff and CI. See `docs/agents/issue-tracker.md`.

### Triage labels

The skill triage roles map to Linear workflow states, not GitHub labels. See `docs/agents/triage-labels.md`.

### Domain docs

This is a single-context repo: read `CONTEXT.md`, `LANGUAGE.md`, relevant `docs/adr/`, and relevant `docs/specs/` before planning architecture or runner behavior changes. See `docs/agents/domain.md`.

### Development loop

Agent Machine work should be spec-driven, TDD-oriented, and evidence-backed. See `docs/agents/development-loop.md`.

### Implementation and review

Implementation agents should follow `docs/agents/implementation.md`. Review agents should follow `docs/agents/review.md` and `docs/agents/review-policy.md`.
