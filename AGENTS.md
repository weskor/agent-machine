# Pi Symphony agent guidance

Work in small, reviewable changes. Use `mise exec go -- make ci` and `git diff --check` before handoff when Go is available through mise.

For runner behavior changes, read the relevant specs first and preserve observable behavior unless the ticket explicitly asks for a behavior change.

## Agent skills

### Issue tracker

Issues are tracked in Linear in the CAG / Pi Symphony Runner project; GitHub is used for PR handoff and CI. See `docs/agents/issue-tracker.md`.

### Triage labels

The skill triage roles map to Linear workflow states, not GitHub labels. See `docs/agents/triage-labels.md`.

### Domain docs

This is a single-context repo: read `CONTEXT.md`, `LANGUAGE.md`, relevant `docs/adr/`, and relevant `docs/specs/` before planning architecture or runner behavior changes. See `docs/agents/domain.md`.
