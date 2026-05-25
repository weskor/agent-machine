# Agent Machine V1 vision

## North star

Agent Machine is a local-first, spec-driven, multi-agent orchestration harness that can be pointed at a real repository and trusted to move well-scoped work from a Linear issue to a high-quality GitHub PR.

The desired V1 experience is boring in the best way: operators can let Agent Machine run for a full working session, inspect status at any point, and understand every outcome without reading daemon logs first.

## V1 promise

Given a configured `am.yaml`, a matching agent prompt, repository guidance, and a queue of well-specified Linear issues, Agent Machine should:

1. Select eligible Candidates deterministically.
2. Claim work with durable coordination.
3. Run one or more isolated Agent sessions without overlapping unsafe mutations.
4. Require tests, validation, review, and behavior-contract evidence before PR Handoff.
5. Produce PRs that are small enough to review and good enough to merge after normal checks.
6. Explain retries, Needs Info, Human Review, merge blockers, cleanup decisions, and terminal failures.
7. Recover safely after restarts, interrupted runs, stale locks, missing workspaces, or partially completed handoffs.

## Quality bar

Agent Machine should optimize for safe autonomy rather than maximum autonomy.

- Prefer a correct escalation over a silent or guessed success.
- Prefer one narrow PR with strong evidence over a broad PR with unclear behavior changes.
- Prefer characterization tests before refactoring behavior that is already relied on.
- Prefer specs and ADRs before durable behavior changes.
- Prefer deterministic status, artifacts, and comments over log-only evidence.

## Engineering principles

### Spec-first behavior

Behavior changes start by reading and, when necessary, updating the relevant spec in `docs/specs/`. Durable decisions get an ADR in `docs/adr/` before implementation work depends on them.

### TDD and characterization

Use tests to describe the current contract before extracting or changing a Module. For broad refactors, characterization tests are part of the design, not cleanup after the fact.

### Deep Modules

Use the vocabulary in `LANGUAGE.md`. Favor Modules whose Interfaces hide real behavior and increase Locality:

- Run attempt outcome
- Candidate reconciliation
- Merge gate decision
- Run evaluation classification
- SQLite state projection
- CLI mode dispatch
- Protocol Adapters

### Runtime providers as Adapters

The default local runtime provider is `codex_cli`: Agent Machine shells to a
locally installed and configured `codex exec` command with clean per-run
configuration. The legacy `pi_cli` provider remains an explicit Adapter choice,
but V1 should treat both as providers rather than the runner architecture. The
same AgentRuntime seam should support a deterministic fake/test provider and
future API, ACP-style, or MCP-style providers.

Provider preflight should fail early before claiming or mutating work when the
selected runtime is unavailable or not configured. Runtime capabilities such as
review support, usage reporting, cancellation, structured output, raw debug
capture, and handoff hints should be declared explicitly.

Git/PR handoff remains a runner invariant: commit/push/PR create-update, branch
and base validation, PR URL validation, and artifact recording should be
runner-owned wherever possible. Agents and runtime providers contribute edits,
semantic explanations, usage, and advisory PR hints; they do not own the final
handoff decision.

### Durable orchestration state

SQLite becomes the local source of truth for Agent Machine orchestration decisions after the contract in `docs/specs/sqlite-orchestration-state.md` is implemented. Linear and GitHub remain external systems of record.

### Multi-agent readiness

V1 should support multiple concurrent Agent sessions through isolated workspaces, leases, heartbeats, budgets, and explicit recovery rules. Parallelism must never require guessing which Agent session owns an issue, PR, workspace, or cleanup decision.

### Editor integration as an Adapter

Agent Client Protocol (ACP) support is a future Adapter milestone. It should expose Agent Machine to ACP-compatible editors such as Zed without moving orchestration policy into editor-specific code.

References:

- https://agentclientprotocol.com/overview/introduction
- https://zed.dev/docs/ai/external-agents

## V1 milestones

### M0: Documentation and architecture baseline

- Vision, behavior specs, ADRs, and agent guidance exist.
- Architecture reviews produce focused Linear issues instead of vague refactor goals.
- Review prompts require behavior-contract evidence for broad changes.

### M1: Smooth single-issue loop

- A Ready Linear issue can move through claim, workspace setup, implementation, validation, review, PR Handoff, Human Review, merge, and cleanup.
- Missing PRs, failed validations, review blockers, and Needs Info are not marked as silent successes.
- Status output explains the current state without requiring log spelunking.

### M2: SQLite-backed recovery

- Attempts, PR mappings, review states, merge blockers, cleanup states, leases, heartbeats, retry decisions, and terminal outcomes are transactionally recorded.
- Runner decisions fail closed when the SQLite decision store is unavailable.
- Reconciliation-needed states are explicit and repairable.

### M3: Multiple Agent sessions

- The daemon can run multiple Agent sessions safely within configured budgets.
- Leases and heartbeats prevent duplicate claims and stale destructive work.
- Operator status shows running, blocked, stale, retryable, mergeable, and reconciliation-needed work.

### M4: ACP Adapter

- Agent Machine can run as an ACP-compatible agent process for editor clients.
- The Adapter delegates to the same core Modules used by the CLI.
- Editor integration does not bypass specs, validation, review policy, leases, budgets, or state reconciliation.

### M5: Product surfaces after the core is trustworthy

Only after the local runner is smooth and recoverable should the project expand toward:

- Web UI
- MCP control plane
- sandboxed cloud environments
- hosted runner fleets
- team dashboards and analytics

## V1 non-goals

- Replacing Linear or GitHub as systems of record.
- Optimizing for unreviewed automatic merge of arbitrary changes.
- Building a web UI before the local CLI/state machine is reliable.
- Moving orchestration policy into editor, MCP, or cloud-specific Adapters.
- Letting stale artifacts override newer durable state without explicit reconciliation.

## Definition of “let it loose”

Agent Machine reaches the intended V1 quality when we can point it at a medium-sized repository for a day of bounded work and trust that every issue ends in one of these explainable outcomes:

- PR ready for Human Review or merge lane evaluation.
- Needs Info with a deterministic question.
- Retry queued with a concrete reason and budget state.
- Human Review because automation found ambiguity or missing evidence.
- Reconciliation-needed because local, Linear, GitHub, or workspace facts conflict.
- Terminal failure with evidence and no unsafe side effects hidden as success.
