# Pi Symphony

Pi Symphony is a local-first Agent runner for a conservative Linear -> workspace -> Agent -> review -> GitHub PR handoff loop.

It is extracted from `pennywise-investments/compound-web` and should be treated as private/experimental while the runner dogfoods its own project configuration.

## Features

- Linear project polling with Ready/In Progress/Human Review/Done lanes.
- Fresh per-issue workspaces and run locks.
- One implementation Agent run plus a separate review pass.
- GitHub App authentication for bot-authored PR handoff, merge gates, squash merge, and branch cleanup.
- Structured run/evaluation artifacts with usage, review status, blockers, and next action.
- Conservative merge gating: approval, green checks/statuses, correct base/head, run evidence, and review pass required.

## Requirements

- Go 1.23+ installed through `mise` (`mise install` in this repository, then use `mise exec go -- ...` for validation)
- `codex` CLI available on `PATH` for the default `codex_cli` runtime
- Optional: `pi` CLI available on `PATH` when `runtime.provider: pi_cli` is configured
- Linear API token
- GitHub token or GitHub App credentials with repository access

## Configuration

Create two files in the target repository:

- `symphony.yaml` for tracker, repository, workspace, runtime, hook, budget, GitHub, and lane settings.
- `symphony.agent.md` for the target-repository prompt template and agent instructions.

See `symphony.example.yaml` and `symphony.agent.example.md`.

This repository also has its own `symphony.yaml` for Pi Symphony runner work. It uses the CAG / Compound Agents team with the `Pi Symphony Runner` Linear project and targets the `main` branch.

Set `repository.remote` when new workspaces should be bootstrapped by cloning the target repository. Use `agent.prompt_path` when the prompt file is not named `symphony.agent.md`.

Secrets can be exported in the process environment, loaded with `--env-file`, or placed in `.env.local` next to the selected `symphony.yaml`. Process environment values win over `.env.local` values:

- `LINEAR_API_KEY`
- `GITHUB_TOKEN` / `GH_TOKEN`, or GitHub App credentials:
  - `GITHUB_APP_ID`
  - `GITHUB_APP_INSTALLATION_ID`
  - `GITHUB_APP_PRIVATE_KEY_PATH`

Keep `.env.local` local-only. Do not commit tokens, private keys, absolute credential paths, or copied environment files; commit only placeholder examples such as `symphony.example.yaml`.

For local runner development, a minimal `.env.local` usually contains `LINEAR_API_KEY` plus either `GITHUB_TOKEN`/`GH_TOKEN` or the three `GITHUB_APP_*` values. Prefer GitHub App credentials for bot-authored PR handoff and merge-lane testing.

## Commands

From this repository:

```bash
go run . config print
go run . status
go run . run-status CAG-123
go run . surface snapshot
go run . explain
go run . start
go run . status --config /path/to/target/symphony.yaml
go run . start --config /path/to/target/symphony.yaml
go run . worker implementation --config /path/to/target/symphony.yaml
go run . merge-approved --config /path/to/target/symphony.yaml
go run . cleanup-workspaces --apply --config /path/to/target/symphony.yaml
go run . repair-artifacts --config /path/to/target/symphony.yaml
```

`explain` (also available as `--explain` or `--dry-run`) prints structured JSON describing the next candidate Symphony would run, merge blockers for open Symphony PRs, and cleanup eligibility. It does not claim issues, merge PRs, delete workspaces, or update local orchestration state.

`run-status <issue>` prints a single compact local progress line from `.symphony/state/run-progress/<issue>/progress.json` and does not require Linear or GitHub access.

`surface snapshot` prints the read-only JSON contract used by product surfaces. It reads local orchestration state, locks, and artifacts without requiring Linear or GitHub access.

The first product surface is an OpenTUI adapter over that snapshot:

```bash
cd tui
bun install
bun run start -- --config ../symphony.yaml
```

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

Use `make ci` and `git diff --check` before handing off a runner change. When validating config/status/cleanup/merge behavior, also run a safe status smoke check against the intended config file, for example:

```bash
mise exec go -- go run . status --config symphony.yaml
```

### Opt-in live smoke harness

`cmd/pi-symphony-live-smoke` creates or uses disposable Linear issues and runs them through a generated, isolated config plus prompt file. It is off by default, uses a deterministic fake Agent by default, and is not part of `make ci`.

Required gates:

- `LIVE_LINEAR=1`
- `LINEAR_API_KEY` exported or present in `.env.local`
- GitHub credentials accepted by the runner, usually the local GitHub App variables from `.env.local`

Basic fake-agent smoke:

```bash
LIVE_LINEAR=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --config symphony.yaml \
  --count 1
```

Concurrency-oriented setup without running the merge lane:

```bash
LIVE_LINEAR=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --config symphony.yaml \
  --count 2 \
  --concurrency 2
```

The harness prints every created issue identifier/URL and writes a JSON report under `.symphony/live-smoke/`. It refuses to create issues when unrelated `Ready for Agent` issues already exist, and its generated config only treats `Ready for Agent` as active so human `In Progress` work is not claimed.

The harness never runs merge-approved unless both controls are present:

```bash
LIVE_LINEAR=1 LIVE_SMOKE_APPLY=1 mise exec go -- go run ./cmd/pi-symphony-live-smoke \
  --config symphony.yaml \
  --from-report .symphony/live-smoke/live-smoke-YYYYMMDDTHHMMSSZ.json \
  --apply-merge
```

Use `--from-report` for follow-up merge checks so the harness reuses the original smoke workspace root and artifact evidence. Supplying only `--issue` with a fresh workspace root is intentionally rejected for `--apply-merge` because merge gates need the original run artifacts.

Architecture and behavior docs live in `CONTEXT.md`, `LANGUAGE.md`, `docs/adr/`, and `docs/specs/`. Broad refactors should cite the relevant specs/ADRs in PR handoff notes, update specs when observable behavior changes, or state that no spec changes were needed for a mechanical move.

## Symphony dogfood loop

Use small, reviewable Linear tickets when evaluating the runner against itself or another target repository:

1. Write each ticket with the standard `Goal`, `Scope`, `Requirements`, `Acceptance Criteria`, and `Validation` sections.
2. Move exactly one ticket into `Ready for Agent` when it is safe for the runner to start it. Keep future dogfood tickets out of active states until the current PR is reviewed.
3. Run the production loop with `go run . start --config symphony.yaml`, or run one implementation worker process with `go run . worker implementation --config symphony.yaml` when doing a controlled dogfood pass. The runner treats `Ready for Agent` and `In Progress` as active states by default; it moves claimed work to the configured running state and hands completed implementation PRs to `Human Review`.
4. Review the PR before activating the next ticket. Objective review signals are: scoped diff, no secrets, required validation recorded, `make ci`/tests green, `git diff --check` clean, review pass or clear blocker notes, and a PR from the expected `symphony/<issue>-workspace` branch into the configured base branch.
5. Only after the PR is accepted or the ticket is moved to a non-active lane should the next dogfood ticket be moved into `Ready for Agent`.

Do not use the dogfood loop to batch unrelated work. If a ticket needs missing credentials, unclear scope, or unsafe production changes, move it to `Needs Info` instead of guessing.

## Current extraction status

This repository owns the Pi Symphony runner implementation, tests, GitHub/Linear integrations, review/merge/status/cleanup behavior, and runner project config. `compound-web` is now a consumer that keeps only its `symphony.yaml`, `symphony.agent.md`, and ignored `.symphony/` runtime state.
