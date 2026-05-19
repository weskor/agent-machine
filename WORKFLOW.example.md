---
tracker:
  kind: linear
  endpoint: https://api.linear.app/graphql
  api_key: $LINEAR_API_KEY
  project_slug: replace-with-linear-project-slug
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
  root: /absolute/path/to/target-repo/.symphony/workspaces
hooks:
  timeout_ms: 120000
  after_create: |
    git clone --branch develop git@github.com:OWNER/REPO.git .
    bun install --frozen-lockfile
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
    git clone --branch develop git@github.com:OWNER/REPO.git .
  before_run: git status --short --branch
  after_run: git diff --check
budgets:
  wall_clock: 2h
  max_tokens: 0
  max_cost: 0
  command_timeout: 10m
  pi_timeout: 90m
  review_timeout: 30m
  merge_timeout: 10m
  github_timeout: 2m
compound:
  handoff_state: Human Review
  running_state: In Progress
  needs_info_state: Needs Info
  done_state: Done
  auto_merge: false
  required_validation: []
---

# Target Repository Symphony Workflow

Use this body to tell Pi how to work in the target repository. Keep it repository-specific and explicit about scope, validation, and handoff rules.

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
