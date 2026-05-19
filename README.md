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

- Go 1.23+
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

## Commands

From this repository:

```bash
go run . --status WORKFLOW.md
go run . --continuous WORKFLOW.md
go run . --status /path/to/target/WORKFLOW.md
go run . --once /path/to/target/WORKFLOW.md
go run . --continuous /path/to/target/WORKFLOW.md
go run . --merge-approved /path/to/target/WORKFLOW.md
go run . --cleanup-workspaces --apply /path/to/target/WORKFLOW.md
go run . --repair-artifacts /path/to/target/WORKFLOW.md
```

## Development

```bash
make fmt        # apply gofmt/goimports
make fmt-check  # verify gofmt/goimports formatting
make vet        # run go vet ./...
make lint       # run golangci-lint with the repository baseline
go test ./...
make test       # run go test ./...
make ci         # run format, vet, lint, and tests
```

## Current extraction status

This repository owns the Pi Symphony runner implementation, tests, GitHub/Linear integrations, review/merge/status/cleanup behavior, and runner workflow. `compound-web` is now a consumer that keeps only its `WORKFLOW.md` and ignored `.symphony/` runtime state.
