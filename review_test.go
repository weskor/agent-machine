package main

import (
	"strings"
	"testing"
)

func TestReviewStatusUsesFirstMarkerLine(t *testing.T) {
	output := "REVIEW_PASS\nNo blockers. Historical note mentions REVIEW_FAIL."
	if got := reviewStatus(output); got != "passed" {
		t.Fatalf("expected passed, got %q", got)
	}
}

func TestReviewStatusFailsWhenFailMarkerPrecedesPromptEcho(t *testing.T) {
	output := "review prompt mentions REVIEW_PASS inline\nREVIEW_FAIL\nScope drift found."
	if got := reviewStatus(output); got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
}

func TestReviewStatusFailsOnFailMarker(t *testing.T) {
	if got := reviewStatus("REVIEW_FAIL\nScope drift found."); got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
}

func TestReviewStatusUnknownWithoutMarker(t *testing.T) {
	if got := reviewStatus("No explicit marker."); got != "unknown" {
		t.Fatalf("expected unknown, got %q", got)
	}
}

func TestReviewClassificationPassIsEmpty(t *testing.T) {
	if got := reviewClassification("passed", "REVIEW_PASS"); got != "" {
		t.Fatalf("expected empty pass classification, got %q", got)
	}
}

func TestReviewClassificationBehaviorSpecBlocker(t *testing.T) {
	output := "REVIEW_FAIL\nREVIEW_CLASSIFICATION: behavior_spec_blocker\nScope drift found."
	if got := reviewClassification("failed", output); got != reviewClassificationBehaviorSpecBlocker {
		t.Fatalf("classification = %q", got)
	}
}

func TestReviewClassificationMissingEvidenceOnly(t *testing.T) {
	output := "REVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nPR body needs Behavior Contract Evidence."
	if got := reviewClassification("failed", output); got != reviewClassificationMissingEvidenceOnly {
		t.Fatalf("classification = %q", got)
	}
}

func TestReviewClassificationUnknownWhenMissingOrMalformed(t *testing.T) {
	for _, output := range []string{"REVIEW_FAIL", "REVIEW_FAIL\nREVIEW_CLASSIFICATION: maybe"} {
		if got := reviewClassification("failed", output); got != reviewClassificationUnknown {
			t.Fatalf("classification = %q for %q", got, output)
		}
	}
}

func TestMissingEvidenceOnlyReviewFailureRoutesToHumanHandoffWithPR(t *testing.T) {
	review := &reviewResult{Status: "failed", Classification: reviewClassificationMissingEvidenceOnly}
	if !reviewFailureRoutesToHumanHandoff(review, "https://github.com/acme/repo/pull/1") {
		t.Fatal("expected missing-evidence-only review failure with PR to route to Human Review")
	}
	if reviewFailureRoutesToHumanHandoff(review, "") {
		t.Fatal("expected missing PR to remain blocking")
	}
}

func TestReviewCommandWithHighReasoningUpgradesLow(t *testing.T) {
	command := "pi --mode json --print --no-session --thinking low"
	want := "pi --mode json --print --no-session --thinking xhigh"
	if got := reviewCommandWithHighReasoning(command); got != want {
		t.Fatalf("review command = %q, want %q", got, want)
	}
}

func TestReviewCommandWithHighReasoningAddsMissingFlag(t *testing.T) {
	command := "pi --mode json --print --no-session"
	want := "pi --mode json --print --no-session --thinking xhigh"
	if got := reviewCommandWithHighReasoning(command); got != want {
		t.Fatalf("review command = %q, want %q", got, want)
	}
}

func TestReviewPromptIncludesDomainSemanticChecklist(t *testing.T) {
	prompt := reviewPrompt(&issue{Identifier: "CAG-14"}, "https://github.com/example/repo/pull/407", "/tmp/workspace")

	for _, expected := range []string{
		"tools/compound-client-cli",
		"direct database writes",
		"nearest production flow",
		"enum values, roles, statuses, tenant/org scope, and side effects",
		"hardcoded domain strings",
		"REVIEW_PASS requires evidence that relevant domain source files were checked",
		"packages/auth/src/permissions.ts",
		"apps/dashboard/src/trpc/routers/organization/organization.clients.ts",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewPromptFailsReplacementWithoutBehaviorContractEvidence(t *testing.T) {
	prompt := reviewPrompt(&issue{Identifier: "CAG-38"}, "https://github.com/example/repo/pull/438", "/tmp/workspace")

	for _, expected := range []string{
		"Behavior-contract review requirements",
		"replaces code, dependencies, commands, integrations, workflows, or state-machine logic",
		"existing-behavior inventory",
		"inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts",
		"docs/specs/ and docs/adr/",
		"Behavior Contract Evidence",
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
	prompt := reviewPrompt(&issue{Identifier: "CAG-40"}, "https://github.com/example/repo/pull/440", "/tmp/workspace")

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
