# Contributing

Pi Symphony changes should be small, reviewable, and evidence-backed.

Before planning behavior, architecture, runner, state, product-surface, or
multi-agent work, read:

- `AGENTS.md`
- `CONTEXT.md`
- `LANGUAGE.md`
- `docs/vision/pi-symphony-v1.md`
- relevant files in `docs/specs/` and `docs/adr/`

For runner behavior changes, update or add the relevant spec before changing the
implementation when the observable contract changes. Add characterization tests
before behavior-risky refactors.

Before handoff, run:

```bash
mise exec go -- make ci
git diff --check
```

Do not commit `.env.local`, private keys, `.symphony/` runtime state, generated
release artifacts, or target-repository secrets.
