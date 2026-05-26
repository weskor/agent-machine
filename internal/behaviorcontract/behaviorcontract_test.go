package behaviorcontract

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/domain"
)

func TestPreflightPromptCoversGenericReplacementContracts(t *testing.T) {
	prompt := PreflightPrompt()

	for _, expected := range []string{
		"refactors, replacements, and rewrites",
		"CONTEXT.md",
		"LANGUAGE.md",
		"docs/adr/",
		"docs/specs/",
		"code, commands, dependencies, integrations, workflows, or state-machine logic",
		"inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts",
		"Behavior Contract Evidence",
		"cite relevant specs/ADRs",
		"behavior preserved",
		"behavior intentionally changed",
		"unknown behavior that needs clarification",
		"no spec changes were needed",
		"TDD or characterization tests",
		"complexity/LOC budget",
		"expected files touched",
		"what bespoke code is removed",
		"NEEDS_INFO",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("preflight prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestEvidenceForRunRecordsReviewFailureFindings(t *testing.T) {
	record := domain.RunRecord{Status: "review_failed", ReviewStatus: "failed", ReviewFindings: "REVIEW_FAIL missing parity checklist"}
	joined := strings.Join(EvidenceForRun(record), ",")
	for _, expected := range []string{"implementation_prompt_required_behavior_contract_preflight", "review_prompt_required_behavior_contract_parity_check", "review_failed_behavior_contract_or_scope_gate", "findings_recorded_for_behavior_contract_audit"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("behavior contract evidence missing %q in %s", expected, joined)
		}
	}
}
