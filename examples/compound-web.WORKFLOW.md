---
tracker:
  kind: linear
  endpoint: https://api.linear.app/graphql
  api_key: $LINEAR_API_KEY
  project_slug: 434f2b9745b1
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
  root: /home/wes/Workspace/pennywise/compound-web/.symphony/workspaces
  base_branch: develop
hooks:
  timeout_ms: 120000
  after_create: |
    git clone --branch develop git@github.com:pennywise-investments/compound-web.git .
    bun install --frozen-lockfile
  before_run: bun run agent:status
  after_run: bun run repo:check
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
    git clone --branch develop git@github.com:pennywise-investments/compound-web.git .
  before_run: bun run symphony:doctor
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
  required_validation:
    - bun run agent:status
    - bun run repo:check
---

# Compound Web Symphony Workflow Example

This is a trimmed example for the first consumer repository. Keep the live workflow in the consumer repo.
