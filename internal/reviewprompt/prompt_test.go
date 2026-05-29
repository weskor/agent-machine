package reviewprompt

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/domain"
)

func TestReviewPromptOmitsTargetRepositoryGuidanceByDefault(t *testing.T) {
	prompt := Prompt(&domain.Issue{Identifier: "CAG-14"}, "https://github.com/example/repo/pull/407", "/tmp/workspace", "", nil)

	for _, unexpected := range []string{
		"Target-repository review guidance from project configuration",
		"Review direct data writes against the repository domain docs.",
		"Require tenant isolation evidence for billing mutations.",
	} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("review prompt unexpectedly included %q:\n%s", unexpected, prompt)
		}
	}
}

func TestReviewPromptIncludesConfiguredTargetRepositoryGuidance(t *testing.T) {
	guidance := "Review direct data writes against the repository domain docs.\nRequire tenant isolation evidence for billing mutations."
	prompt := Prompt(&domain.Issue{Identifier: "CAG-14"}, "https://github.com/example/repo/pull/407", "/tmp/workspace", guidance, nil)

	for _, expected := range []string{
		"Target-repository review guidance from project configuration",
		"Review direct data writes against the repository domain docs.",
		"Require tenant isolation evidence for billing mutations.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewPromptFailsReplacementWithoutBehaviorContractEvidence(t *testing.T) {
	prompt := Prompt(&domain.Issue{Identifier: "CAG-38"}, "https://github.com/example/repo/pull/438", "/tmp/workspace", "", nil)

	for _, expected := range []string{
		"Behavior-contract review requirements",
		"replaces code, dependencies, commands, integrations, workflows, or state-machine logic",
		"existing-behavior inventory",
		"inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts",
		"docs/specs/ and docs/adr/",
		"Behavior Contract Evidence",
		"PR body or handoff evidence source",
		"relevant specs/ADRs cited",
		"no spec changes were needed",
		"TDD or characterization-test evidence",
		"complexity/LOC budget",
		"prior side effects, cleanup, or state transitions are dropped",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewPromptIncludesTicketContractHardFailGates(t *testing.T) {
	prompt := Prompt(&domain.Issue{Identifier: "CAG-40"}, "https://github.com/example/repo/pull/440", "/tmp/workspace", "", nil)

	for _, expected := range []string{
		"Ticket-contract review requirements",
		"REVIEW_FAIL if the implementation violates explicit MUST / MUST NOT statements",
		"required packages or approaches",
		"out-of-scope items",
		"objective Acceptance Criteria",
		"MUST use github.com/google/go-github/v66/github",
		"MUST NOT add bespoke net/http GitHub API wrappers",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewPromptIncludesRunnerOwnedEvidencePacket(t *testing.T) {
	evidence := Evidence{IssueIdentifier: "CAG-120", IssueTitle: "Review evidence", PRURL: "https://github.com/weskor/agent-machine/pull/120", Workspace: "/tmp/workspace", BaseBranch: "main", HeadBranch: "am/CAG-120-workspace", HeadSHA: "abc123", ChangedFiles: 3, Additions: 42, Deletions: 7, ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS", ScopeSummary: "changed files matched the Linear ticket path contract", Validation: []string{"mise exec go -- make ci", "git diff --check"}, ProgressPath: "/repo/.am/state/run-progress/CAG-120/progress.json"}
	prompt := Prompt(&domain.Issue{Identifier: "CAG-120", Title: "Review evidence"}, evidence.PRURL, evidence.Workspace, "", &evidence)

	for _, expected := range []string{"Runner-owned deterministic review evidence", "Head SHA: abc123", "Code-host checks: success", "go-ci=COMPLETED/SUCCESS", "Scope guard: changed files matched", "mise exec go -- make ci", "Progress snapshot", "source of truth for deterministic PR/check/scope facts", "semantic/spec quality"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}
