# Smoke Evidence

This directory holds public artifacts from opt-in Agent Machine smoke runs.

Disposable marker files such as `live-smoke-*.md` prove that the fake Agent
produced the scoped repository diff for a specific Linear issue. They do not, by
themselves, prove the full runner lifecycle.

For public dogfood evidence, run the live smoke harness with `--public-report
auto`. The generated `*-evidence.md` report records:

- Linear issue links used by the harness;
- isolated smoke config and workspace root paths;
- whether the deterministic fake Agent was used;
- the exact runner commands the harness executed;
- command exit status;
- whether merge application was requested.

Evidence reports intentionally state their boundary. They are a public index of
harness-controlled steps, not a replacement for PR review, CI checks, Linear
state inspection, or code-host merge evidence.

To render public Markdown from an existing JSON report without contacting
Linear, run:

```bash
mise exec go -- go run ./cmd/agent-machine-live-smoke \
  --render-report \
  --from-report .am/live-smoke/live-smoke-YYYYMMDDTHHMMSSZ.json \
  --public-report auto
```
