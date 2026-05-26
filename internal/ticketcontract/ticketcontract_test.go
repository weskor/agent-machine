package ticketcontract

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/domain"
)

func TestPromptRequiresFiveSectionsAndNeedsInfo(t *testing.T) {
	prompt := Prompt()
	for _, expected := range []string{
		"Goal, Scope, Requirements, Acceptance Criteria, Validation",
		"MUST / MUST NOT",
		"required packages or approaches",
		"allowed paths",
		"out-of-scope items",
		"behavior-preservation notes",
		"output NEEDS_INFO with numbered questions",
		"github.com/google/go-github/v66/github",
		"MUST NOT add bespoke net/http GitHub API wrappers",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("ticket contract prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewPromptHardFailsExplicitConstraints(t *testing.T) {
	prompt := ReviewPrompt()
	for _, expected := range []string{
		"REVIEW_FAIL if the implementation violates explicit MUST / MUST NOT statements",
		"required packages or approaches",
		"allowed paths",
		"out-of-scope items",
		"objective Acceptance Criteria",
		"guessed instead of using NEEDS_INFO",
		"MUST use github.com/google/go-github/v66/github",
		"MUST NOT add bespoke net/http GitHub API wrappers",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("ticket contract review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestEvidenceForRunFlagsHardConstraintFindings(t *testing.T) {
	record := domain.RunRecord{Status: "review_failed", ReviewStatus: "failed", ReviewFindings: "REVIEW_FAIL violated MUST use go-github and MUST NOT add bespoke net/http wrappers"}
	evidence := strings.Join(EvidenceForRun(record), ",")
	for _, expected := range []string{"implementation_prompt_required_five_section_ticket_contract", "review_prompt_enforced_ticket_contract_hard_gates", "findings_recorded_for_ticket_contract_audit"} {
		if !strings.Contains(evidence, expected) {
			t.Fatalf("ticket contract evidence missing %q in %s", expected, evidence)
		}
	}
}
