package ticketcontract

import (
	"strings"

	"github.com/weskor/agent-machine/internal/domain"
)

func Prompt() string {
	return `Ticket-contract preflight:
- Restate the five Linear ticket sections before planning: Goal, Scope, Requirements, Acceptance Criteria, Validation.
- Treat explicit MUST / MUST NOT statements, required packages or approaches, allowed paths, and out-of-scope items as hard constraints.
- Prefer machine-readable path bullets under Allowed paths: and Out of scope:. Use exact paths or simple globs such as run_one.go, internal/state/*, or docs/specs/*.md.
- For refactors, replacements, and rewrites, require behavior-preservation notes in Requirements or Acceptance Criteria before changing code.
- If Scope, Requirements, or Acceptance Criteria are incomplete or unsafe for a new refactor/replacement ticket, output NEEDS_INFO with numbered questions instead of guessing.
- Example hard constraint: MUST use github.com/google/go-github/v66/github; MUST NOT add bespoke net/http GitHub API wrappers.`
}

func ReviewPrompt() string {
	return `Ticket-contract review requirements:
- REVIEW_FAIL if the implementation violates explicit MUST / MUST NOT statements, required packages or approaches, allowed paths, out-of-scope items, or objective Acceptance Criteria.
- REVIEW_FAIL if a refactor/replacement ticket lacked enough Scope, Requirements, or Acceptance Criteria to proceed safely and the implementation guessed instead of using NEEDS_INFO.
- REVIEW_FAIL for the CAG-40-style failure mode: ticket says MUST use github.com/google/go-github/v66/github and MUST NOT add bespoke net/http GitHub API wrappers, but the PR adds a bespoke net/http GitHub API wrapper or avoids go-github.
- Existing legacy CAG issues may proceed when they already contain enough detail to verify scope, requirements, acceptance criteria, and validation.`
}

func EvidenceForRun(record domain.RunRecord) []string {
	evidence := []string{"implementation_prompt_required_five_section_ticket_contract"}
	if record.ReviewStatus != "" {
		evidence = append(evidence, "review_prompt_enforced_ticket_contract_hard_gates")
	}
	if strings.HasPrefix(record.Status, "needs_info") {
		evidence = append(evidence, "needs_info_used_for_incomplete_ticket_contract")
	}
	findings := strings.ToLower(record.ReviewFindings + "\n" + record.Error)
	for _, needle := range []string{"must", "must not", "out-of-scope", "acceptance criteria", "required package", "bespoke net/http", "go-github"} {
		if strings.Contains(findings, needle) {
			evidence = append(evidence, "findings_recorded_for_ticket_contract_audit")
			break
		}
	}
	return evidence
}
