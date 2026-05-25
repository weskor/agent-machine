# Issue tracker: Linear

Issues and PRDs for this repo live in Linear, not GitHub Issues.

## Project

- Linear team: CAG / Agent Machine Runner
- Linear project: Agent Machine Runner
- Project slug: `c08e7f84bb75`
- Local project config: `symphony.yaml`

## Conventions

- Runnable implementation issues should contain five sections: Goal, Scope, Requirements, Acceptance Criteria, and Validation.
- `Ready for Agent` means a ticket is fully specified and safe for Agent Machine to claim.
- `In Progress` means Agent Machine has claimed the issue or a human is actively repairing it.
- `Human Review` means a PR exists and needs review/merge judgment.
- `Needs Info` means the agent could not proceed safely without more detail.
- `Done` means the PR or requested work has been accepted and the branch/issue cleanup is complete.

## When a skill says “publish to the issue tracker”

Create or update a Linear issue in the Agent Machine Runner project. Use the Linear API with `LINEAR_API_KEY` from `.env.local`; never commit credentials.

## When a skill says “fetch the relevant ticket”

Read the Linear issue by identifier, including description, state, priority, labels, comments, and linked PRs when available.

## GitHub’s role

GitHub is the code review and CI surface. Agent Machine opens PRs from `symphony/<issue>-workspace` branches into the configured base branch and posts deterministic handoff comments.
