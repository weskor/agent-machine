# Dogfood Evidence Index

This index records public evidence for Agent Machine runs against this
repository. It is intentionally conservative: a row is not treated as proof of
unattended end-to-end automation unless the public artifacts show the harness
commands, exit status, issue link, PR/MR handoff, review/CI state, and merge or
terminal outcome.

## Current Public Evidence

| Issue | Smoke artifact | Public PR evidence | What it proves | Boundary |
| --- | --- | --- | --- | --- |
| CAG-135 | [live-smoke-20260522T200454Z-01.md](./live-smoke-20260522T200454Z-01.md) | [PR #84](https://github.com/weskor/agent-machine/pull/84), merged 2026-05-22T20:16:35Z from `symphony/CAG-135-workspace` into `main` by `app/pi-symphony-bot` | A disposable smoke issue produced a scoped docs diff and reached a merged GitHub PR. | The tracked artifact predates structured command-result capture, so it does not prove every harness step or absence of manual intervention. |
| CAG-142 | [live-smoke-20260522T200905Z-01.md](./live-smoke-20260522T200905Z-01.md) | [PR #86](https://github.com/weskor/agent-machine/pull/86), merged 2026-05-22T20:16:08Z from `symphony/CAG-142-workspace` into `main` by `app/pi-symphony-bot` | A second disposable smoke issue produced a scoped docs diff and reached a merged GitHub PR. | The tracked artifact predates structured command-result capture, so it does not prove every harness step or absence of manual intervention. |
| CAG-186 | [live-smoke-20260525T165252Z-01.md](./live-smoke-20260525T165252Z-01.md) | [PR #150](https://github.com/weskor/agent-machine/pull/150), merged 2026-05-25T18:28:31Z from `am/CAG-186-workspace` into `main` by `app/pi-symphony-bot` | A later disposable smoke issue produced a scoped docs diff and reached a merged GitHub PR after the rename-era runner changes. | Local JSON reports exist under `.am/live-smoke/`, but the public tracked artifact predates structured command-result capture. |
| CAG-187 | [live-smoke-20260526T175925Z-01.md](./live-smoke-20260526T175925Z-01.md), [handoff evidence](./live-smoke-20260526T175925Z-evidence.md), [merge evidence](./live-smoke-20260526T180221Z-evidence.md) | [PR #152](https://github.com/weskor/agent-machine/pull/152), merged 2026-05-26T18:02:32Z from `am/CAG-187-workspace` into `main` by `app/pi-symphony-bot`; GitHub `go-ci` check succeeded and review decision was `APPROVED` before merge | Current-format smoke evidence shows the harness-controlled implementation worker, status, and merge-approved commands all exited successfully; the runner moved the issue to Human Review, then Done, and merged the PR. | The merge evidence explicitly records one manual/operator intervention: GitHub PR #152 was approved before the merge-check follow-up. |
| CAG-188 | [live-smoke-20260526T180458Z-01.md](./live-smoke-20260526T180458Z-01.md), [batch handoff evidence](./live-smoke-20260526T180458Z-evidence.md), [partial merge evidence](./live-smoke-20260526T180803Z-evidence.md) | [PR #153](https://github.com/weskor/agent-machine/pull/153), merged 2026-05-26T18:08:19Z from `am/CAG-188-workspace` into `main` by `app/pi-symphony-bot`; GitHub `go-ci` check succeeded and review decision was `APPROVED` before merge | Current-format batch smoke evidence shows the harness-controlled implementation worker and status commands passed; the first merge-approved follow-up merged CAG-188 and moved it to Done. | The partial merge evidence explicitly records approval of PRs #153 and #154 before merge. The same merge-approved command later failed on CAG-189 because strict checks required a fresh check after #153 changed `main`. |
| CAG-189 | [live-smoke-20260526T180458Z-02.md](./live-smoke-20260526T180458Z-02.md), [batch handoff evidence](./live-smoke-20260526T180458Z-evidence.md), [recovered merge evidence](./live-smoke-20260526T181423Z-evidence.md) | [PR #154](https://github.com/weskor/agent-machine/pull/154), merged 2026-05-26T18:14:32Z from `am/CAG-189-workspace` into `main` by `app/pi-symphony-bot`; GitHub `go-ci` check succeeded after branch update and review decision was `APPROVED` before merge | Current-format batch smoke evidence shows the harness-controlled implementation worker, status, and recovered merge-approved commands all exited successfully; the runner moved the issue to Human Review, then Done, and merged the PR. | The recovered merge evidence explicitly records the manual/operator approval and the branch rebase needed after CAG-188 changed `main` so `go-ci` could rerun. |

## Evidence Bar Going Forward

Future public dogfood claims should use `cmd/agent-machine-live-smoke
--public-report auto` so the repository contains a `*-evidence.md` report with:

- Linear issue links;
- isolated smoke config and workspace root;
- exact runner commands executed by the harness;
- command exit status;
- whether merge application was requested;
- an explicit evidence boundary.

That public report still does not replace PR review, CI checks, Linear state
inspection, or code-host merge evidence. It makes the harness-controlled steps
auditable so dogfood claims do not depend on private local state or memory.
