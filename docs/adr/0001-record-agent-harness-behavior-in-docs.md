# Record agent harness behavior in docs

Pi Symphony broad refactors were being reviewed against an implicit “behavior contract” that agents could not reliably discover. We will keep a single `CONTEXT.md`, architecture `LANGUAGE.md`, ADRs in `docs/adr/`, and observable behavior specs in `docs/specs/`; broad refactors must cite or update these files instead of inventing behavior-contract evidence only in PR prose.

## Consequences

- The implementation prompt should point agents at the docs before broad refactors.
- The review prompt should treat specs and ADRs as the source of truth for behavior-contract evidence.
- Missing evidence should route work to Human Review rather than causing blind retries when code and CI are otherwise healthy.
