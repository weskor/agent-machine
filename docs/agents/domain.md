# Domain Docs

This is a single-context repository.

## Before planning architecture or runner behavior changes

Read these files when they are relevant to the requested area:

- `CONTEXT.md` — Pi Symphony domain language.
- `LANGUAGE.md` — architecture vocabulary for modules, interfaces, seams, depth, adapters, leverage, and locality.
- `docs/vision/pi-symphony-v1.md` — the V1 north star, milestones, and product-surface ordering.
- `docs/agents/development-loop.md` — the spec-first, TDD-oriented workflow for agent changes.
- `docs/adr/` — durable design decisions and trade-offs.
- `docs/specs/` — observable behavior contracts that broad refactors must preserve or explicitly update.
- `docs/agents/implementation.md` — implementation-session expectations for tests, evidence, and stopping conditions.
- `docs/agents/review.md` — review-session expectations for blockers, missing evidence, and architecture findings.
- `docs/agents/review-policy.md` — what implementation and review prompts should require as evidence.

## Update rules

- Update `CONTEXT.md` when a new durable Pi Symphony term is resolved.
- Add an ADR when a decision is hard to reverse, surprising without context, and the result of a real trade-off.
- Update `docs/specs/` when observable behavior changes.
- For mechanical refactors, cite the relevant specs and state that no spec changes were needed.

## Consumer rules

Use the glossary’s vocabulary in issue titles, PR descriptions, specs, ADRs, and review findings. If a proposal contradicts an ADR or spec, call that out explicitly instead of silently overriding it.
