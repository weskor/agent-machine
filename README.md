# Pi Symphony

Pi Symphony is a Pi-native runner for a conservative Linear → workspace → Pi → review → GitHub PR handoff loop.

It is extracted from `pennywise-investments/compound-web` and should be treated as private/experimental while the runner dogfoods its own workflow.

## Features

- Linear project polling with Ready/In Progress/Human Review/Done lanes.
- Fresh per-issue workspaces and run locks.
- One implementation Pi run plus a separate review Pi pass.
- GitHub App authentication for bot-authored PR handoff, merge gates, squash merge, and branch cleanup.
- Structured run/evaluation artifacts with usage, review status, blockers, and next action.
- Conservative merge gating: approval, green checks/statuses, correct base/head, run evidence, and review pass required.

## Requirements

- Go 1.23+ installed through `mise` (`mise install` in this repository, then use `mise exec go -- ...` for validation)
- `pi` CLI available on `PATH`
- Linear API token
- GitHub token or GitHub App credentials with repository access

## Configuration

Create a workflow file in the target repository, usually `WORKFLOW.md`. See `WORKFLOW.example.md`.

This repository also has its own `WORKFLOW.md` for Pi Symphony runner work. It uses the CAG / Compound Agents team with the `Pi Symphony Runner` Linear project and targets the `main` branch.

Local GitHub App credentials are resolved from the nearest `.env.local` next to the target `WORKFLOW.md`; those `GITHUB_APP_*` values and the workflow repository remote take precedence over stale parent-shell values.

Secrets can be exported in the environment or placed in a local `.env.local` next to either the runner or the workflow file:

- `LINEAR_API_KEY`
- `GITHUB_TOKEN` / `GH_TOKEN`, or GitHub App credentials:
  - `GITHUB_APP_ID`
  - `GITHUB_APP_INSTALLATION_ID`
  - `GITHUB_APP_PRIVATE_KEY_PATH`

Keep `.env.local` local-only. Do not commit tokens, private keys, absolute credential paths, or copied environment files; commit only placeholder examples such as `WORKFLOW.example.md`.

For local runner development, a minimal `.env.local` usually contains `LINEAR_API_KEY` plus either `GITHUB_TOKEN`/`GH_TOKEN` or the three `GITHUB_APP_*` values. Prefer GitHub App credentials for bot-authored PR handoff and merge-lane testing.

## Commands

From this repository:

```bash
go run . --status WORKFLOW.md
go run . --run-status=CAG-123 WORKFLOW.md
go run . --explain WORKFLOW.md
go run . --continuous WORKFLOW.md
go run . --status /path/to/target/WORKFLOW.md
go run . --once /path/to/target/WORKFLOW.md
go run . --continuous /path/to/target/WORKFLOW.md
go run . --merge-approved /path/to/target/WORKFLOW.md
go run . --dry-run /path/to/target/WORKFLOW.md
go run . --cleanup-workspaces --apply /path/to/target/WORKFLOW.md
go run . --repair-artifacts /path/to/target/WORKFLOW.md
```

`--explain` (alias `--dry-run`) prints structured JSON describing the next candidate Symphony would run, merge blockers for open Symphony PRs, and cleanup eligibility. It does not claim issues, merge PRs, delete workspaces, or update local orchestration state.

`--run-status=<issue>` prints a single compact local progress line from `.symphony/state/run-progress/<issue>/progress.json` and does not require Linear or GitHub access.

## Development

Start with the project docs when planning behavior or architecture work:

- `docs/vision/pi-symphony-v1.md` for the north star and V1 milestones.
- `docs/agents/development-loop.md` for the spec-first, TDD-oriented agent workflow.
- `docs/agents/implementation.md` and `docs/agents/review.md` for agent-session expectations.
- `CONTEXT.md` and `LANGUAGE.md` for shared domain and architecture vocabulary.
- `docs/specs/` and `docs/adr/` for behavior contracts and durable decisions.

```bash
make fmt        # apply gofmt/goimports
make fmt-check  # verify gofmt/goimports formatting
make vet        # run go vet ./...
make lint       # run golangci-lint with the repository baseline
mise exec go -- go test ./...
make test       # run go test ./...
make ci         # run format, vet, lint, and tests
git diff --check
```

Use `make ci` and `git diff --check` before handing off a runner change. When validating workflow/status/cleanup/merge behavior, also run a safe status smoke check against the intended workflow file, for example:

```bash
mise exec go -- go run . --status WORKFLOW.md
```

### Opt-in live smoke harness

`cmd/pi-symphony-live-smoke` creates or uses disposable Linear issues and runs them through a generated, isolated workflow. It is off by default, uses a deterministic fake Agent by default, and is not part of `make ci`.

Required gates:

- `LIVE_LINEAR=1`
- `LINEAR_API_KEY` exported or present in `.env.local`
- GitHub credentials accepted by the runner, usually the local GitHub App variables from `.env.local`

Basic fake-agent smoke:

```bash
LIVE_LINEAR=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --workflow WORKFLOW.md \
  --count 1
```

Concurrency-oriented setup without running the merge lane:

```bash
LIVE_LINEAR=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --workflow WORKFLOW.md \
  --count 2 \
  --concurrency 2
```

The harness prints every created issue identifier/URL and writes a JSON report under `.symphony/live-smoke/`. It refuses to create issues when unrelated `Ready for Agent` issues already exist, and its generated workflow only treats `Ready for Agent` as active so human `In Progress` work is not claimed.

The harness never runs merge-approved unless both controls are present:

```bash
LIVE_LINEAR=1 LIVE_SMOKE_APPLY=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --workflow WORKFLOW.md \
  --from-report .symphony/live-smoke/live-smoke-YYYYMMDDTHHMMSSZ.json \
  --apply-merge
```

Use `--from-report` for follow-up merge checks so the harness reuses the original smoke workspace root and artifact evidence. Supplying only `--issue` with a fresh workspace root is intentionally rejected for `--apply-merge` because merge gates need the original run artifacts.

Architecture and behavior docs live in `CONTEXT.md`, `LANGUAGE.md`, `docs/adr/`, and `docs/specs/`. Broad refactors should cite the relevant specs/ADRs in PR handoff notes, update specs when observable behavior changes, or state that no spec changes were needed for a mechanical move.

## Symphony dogfood loop

Use small, reviewable Linear tickets when evaluating the runner against itself or another target repository:

1. Write each ticket with the standard `Goal`, `Scope`, `Requirements`, `Acceptance Criteria`, and `Validation` sections.
2. Move exactly one ticket into `Ready for Agent` when it is safe for the runner to start it. Keep future dogfood tickets out of active states until the current PR is reviewed.
3. Run one issue with `go run . --once WORKFLOW.md` or the target workflow path. The runner treats `Ready for Agent` and `In Progress` as active states by default; it moves claimed work to the configured running state and hands completed implementation PRs to `Human Review`.
4. Review the PR before activating the next ticket. Objective review signals are: scoped diff, no secrets, required validation recorded, `make ci`/tests green, `git diff --check` clean, review pass or clear blocker notes, and a PR from the expected `symphony/<issue>-workspace` branch into the configured base branch.
5. Only after the PR is accepted or the ticket is moved to a non-active lane should the next dogfood ticket be moved into `Ready for Agent`.

Do not use the dogfood loop to batch unrelated work. If a ticket needs missing credentials, unclear scope, or unsafe production changes, move it to `Needs Info` instead of guessing.

## Current extraction status

This repository owns the Pi Symphony runner implementation, tests, GitHub/Linear integrations, review/merge/status/cleanup behavior, and runner workflow. `compound-web` is now a consumer that keeps only its `WORKFLOW.md` and ignored `.symphony/` runtime state.
