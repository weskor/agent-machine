---
tracker:
  kind: linear
  endpoint: https://api.linear.app/graphql
  api_key: $LINEAR_API_KEY
  project_slug: c08e7f84bb75
  active_states:
    - Ready for Agent
    - In Progress
  needs_info_state: Needs Info
  terminal_states:
    - Done
    - Canceled
    - Cancelled
    - Duplicate
polling:
  interval_ms: 30000
workspace:
  root: /home/wes/Workspace/weskor/pi-symphony/.symphony/workspaces
  base_branch: main
hooks:
  timeout_ms: 120000
  after_create: |
    git clone --branch main git@github.com:weskor/pi-symphony.git .
  before_run: null
  after_run: null
  before_remove: null
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 300000
pi:
  command: >-
    pi --mode json --print --no-session --thinking low
  review_command: >-
    pi --mode json --print --no-session --thinking xhigh
  after_create: |
    git clone --branch main git@github.com:weskor/pi-symphony.git .
  before_run: mise exec go -- go test ./...
  after_run: mise exec go -- go test ./... && git diff --check
budgets:
  wall_clock: 2h
  max_tokens: 0
  max_cost: 0
  command_timeout: 10m
  pi_timeout: 90m
  review_timeout: 45m
  merge_timeout: 10m
  github_timeout: 2m
compound:
  handoff_state: Human Review
  running_state: In Progress
  needs_info_state: Needs Info
  done_state: Done
  auto_merge: false
  required_validation:
    - mise exec go -- go test ./...
    - git diff --check
---

# Pi Symphony Runner Workflow

You are running inside a Symphony-managed isolated workspace for one Linear issue in the standalone `weskor/pi-symphony` repository.

Follow the runner harness rather than inventing a new workflow:

- Keep changes scoped to Pi Symphony runner code, tests, workflow examples, and runner documentation.
- Do not touch Compound Web runtime app, customer/dashboard, database/schema/seed, auth/onboarding, payment, KYC, document, secret, or unrelated product code.
- Do not commit secrets. Use `.env.local` locally and keep only placeholder examples in git.
- Prefer behavior-driven or characterization tests before refactors, replacements, or state-machine changes.
- Use `mise exec go -- go test ./...` as the default validation command.
- Use `git diff --check` before PR handoff.
- When workflow/status/cleanup/merge behavior changes, also run `mise exec go -- go run . --status /home/wes/Workspace/weskor/pi-symphony/WORKFLOW.md` when it is safe and relevant.
- Open exactly one PR from `symphony/<issue>-workspace` into `main`.
- Stop at Human Review after a passing pre-handoff review; do not auto-merge implementation PRs unless the merge lane later sees explicit approval and green checks.

Standard ticket contract:

1. `Goal` — one outcome statement.
2. `Scope` — allowed paths, required packages or approaches, and explicit out-of-scope items.
3. `Requirements` — hard implementation constraints, including every explicit MUST / MUST NOT.
4. `Acceptance Criteria` — objective pass/fail conditions the reviewer can verify.
5. `Validation` — required commands, smoke checks, or preview validation.

Issue context:

- Identifier: {{issue.identifier}}
- Title: {{issue.title}}
- URL: {{issue.url}}
- State: {{issue.state}}
- Attempt: {{attempt}}

