package behaviorcontract

import (
	"strings"

	"github.com/weskor/agent-machine/internal/domain"
)

func PreflightPrompt() string {
	return `Behavior-contract preflight for refactors, replacements, and rewrites:
- Read the relevant agent docs before planning broad runner work: CONTEXT.md for domain language, LANGUAGE.md for architecture vocabulary, docs/adr/ for durable decisions, docs/specs/ for observable contracts, and docs/agents/review-policy.md for evidence expectations.
- Before changing code, commands, dependencies, integrations, workflows, or state-machine logic, inventory the existing observable contract: inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Add a Behavior Contract Evidence section to the PR body or tracker handoff: cite relevant specs/ADRs, list behavior preserved, behavior intentionally changed with justification, and unknown behavior that needs clarification.
- Update docs/specs/ when observable behavior intentionally changes; for mechanical refactors, state that no spec changes were needed.
- Use TDD or characterization tests for old observable behavior before proving the new abstraction; tests only around the new design are not enough.
- State a complexity/LOC budget before implementation: expected files touched, expected LOC direction, why any net growth is acceptable, what bespoke code is removed, and when the work must split.
- If the existing contract cannot be determined safely, output NEEDS_INFO instead of guessing.`
}

func EvidenceForRun(record domain.RunRecord) []string {
	evidence := []string{"implementation_prompt_required_behavior_contract_preflight"}
	if record.ReviewStatus != "" {
		evidence = append(evidence, "review_prompt_required_behavior_contract_parity_check")
	}
	if record.ReviewStatus == "passed" {
		evidence = append(evidence, "review_passed_behavior_contract_gate")
	}
	if record.ReviewStatus == "failed" {
		evidence = append(evidence, "review_failed_behavior_contract_or_scope_gate")
	}
	if record.ReviewClassification != "" {
		evidence = append(evidence, "review_classification_"+record.ReviewClassification)
	}
	if strings.HasPrefix(record.Status, "needs_info") {
		evidence = append(evidence, "needs_info_used_for_unknown_behavior_contract")
	}
	if strings.TrimSpace(record.Error) != "" || strings.TrimSpace(record.ReviewFindings) != "" {
		evidence = append(evidence, "findings_recorded_for_behavior_contract_audit")
	}
	return evidence
}
