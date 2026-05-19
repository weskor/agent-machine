# Prioritize local-first orchestration before product surfaces

## Status

Accepted for V1 planning.

## Context

Pi Symphony is being dogfooded on its own repository. The current work has exposed valuable harness gaps: implicit behavior contracts, review evidence drift, state spread across workspace artifacts, daemon recovery concerns, and subagent output/tool-surface mismatch.

At the same time, future product directions are attractive: editor integration through Agent Client Protocol (ACP), MCP control surfaces, web UI, hosted or sandboxed cloud environments, and richer multi-agent dashboards.

Those surfaces will amplify whatever orchestration behavior exists. If the local runner can silently mark missing PRs as success, lose state across restarts, or hide reconciliation failures in logs, adding a web UI or editor Adapter will make the behavior easier to trigger but not safer.

## Decision

Prioritize a local-first, spec-driven V1 before building product surfaces.

The V1 core is:

- documented vision, specs, ADRs, and agent guidance;
- TDD and characterization tests for behavior changes;
- deep Modules for run outcomes, Candidate reconciliation, merge gates, evaluation classification, state projection, and CLI mode dispatch;
- SQLite-backed durable orchestration state with explicit reconciliation and fail-closed behavior;
- safe multiple Agent sessions through leases, heartbeats, budgets, and isolated workspaces;
- deterministic status, Handoff evidence, merge blockers, cleanup state, and terminal outcomes.

ACP should be the first external Protocol Adapter after the core CLI/state machine is trustworthy because it gives editor access without requiring a web service. Web UI, MCP control plane, and cloud environments remain later product-surface milestones.

## Consequences

- Architecture and behavior-contract work may take priority over visible UI features.
- Specs and ADRs are required before durable behavior changes, even when implementation seems obvious.
- Product-surface tickets should prove that they reuse core Modules rather than duplicate policy.
- The project can validate quality by dogfooding locally before adding hosted operational complexity.
- ACP work can proceed as a narrow Adapter milestone once the core outcomes, state, and status contracts are stable.

## Alternatives considered

- Build a web UI first: useful for visibility, but risks presenting unreliable state as authoritative.
- Add MCP first: useful for remote control, but does not by itself solve leases, recovery, status, or quality gates.
- Move directly to cloud runners: increases isolation and scalability, but adds secrets, sandboxing, tenancy, and operations before local behavior is proven.
- Keep improving ad hoc via issues only: fast initially, but risks losing the north star and repeating implicit-contract review failures.

