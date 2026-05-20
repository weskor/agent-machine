package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestReviewStatusUsesFirstMarkerLine(t *testing.T) {
	output := "REVIEW_PASS\nNo blockers. Historical note mentions REVIEW_FAIL."
	if got := reviewStatus(output); got != "passed" {
		t.Fatalf("expected passed, got %q", got)
	}
}

func TestRunReviewCharacterizesInvocationAndOutcome(t *testing.T) {
	for _, tc := range []struct {
		name           string
		script         string
		timeout        time.Duration
		wantStatus     string
		wantClass      string
		wantErrTimeout bool
	}{
		{
			name:       "pass records prompt arg cwd env usage and high reasoning flag",
			script:     `printf 'cwd=%s env=%s args=%s\n' "$PWD" "$REVIEW_ENV" "$*"; printf 'REVIEW_PASS\n'; printf '{"message":{"usage":{"input":4,"output":2,"totalTokens":6,"cost":{"total":0.02}}}}\n'`,
			wantStatus: "passed",
		},
		{
			name:       "fail propagates classification and findings",
			script:     `printf 'REVIEW_FAIL\nREVIEW_CLASSIFICATION: behavior_spec_blocker\nScope drift.\n'`,
			wantStatus: "failed",
			wantClass:  reviewClassificationBehaviorSpecBlocker,
		},
		{
			name:           "timeout returns partial error result",
			script:         `printf 'REVIEW_PASS\n'; sleep 1`,
			timeout:        time.Millisecond,
			wantStatus:     "error",
			wantClass:      reviewClassificationUnknown,
			wantErrTimeout: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			script := filepath.Join(workspace, "fake-pi")
			if err := os.WriteFile(script, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			result, err := runReview(sh.Quote(script), workspace, &issue{Identifier: "CAG-94", Title: "Runtime", URL: "https://linear.test/CAG-94"}, "https://github.com/weskor/pi-symphony/pull/94", map[string]string{"REVIEW_ENV": "from-test"}, tc.timeout, nil)
			if tc.wantErrTimeout {
				if !errors.Is(err, sh.ErrCommandTimeout) {
					t.Fatalf("expected timeout, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("runReview returned error: %v", err)
			}
			if result == nil || result.Status != tc.wantStatus {
				t.Fatalf("status = %#v, want %q", result, tc.wantStatus)
			}
			if result.Classification != tc.wantClass {
				t.Fatalf("classification = %q, want %q", result.Classification, tc.wantClass)
			}
			if tc.wantStatus == "passed" {
				for _, want := range []string{"cwd=" + workspace, "env=from-test", "--thinking xhigh", "@" + filepath.Join(workspace, ".pi-symphony-review-prompt.md")} {
					if !strings.Contains(result.Findings, want) {
						t.Fatalf("findings %q missing %q", result.Findings, want)
					}
				}
				if result.Usage == nil || result.Usage.TotalTokens != 6 || result.Usage.TotalCost() != 0.02 {
					t.Fatalf("usage = %#v", result.Usage)
				}
			}
		})
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
	prompt := reviewPrompt(&issue{Identifier: "CAG-14"}, "https://github.com/example/repo/pull/407", "/tmp/workspace", nil)

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
	prompt := reviewPrompt(&issue{Identifier: "CAG-38"}, "https://github.com/example/repo/pull/438", "/tmp/workspace", nil)

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
	prompt := reviewPrompt(&issue{Identifier: "CAG-40"}, "https://github.com/example/repo/pull/440", "/tmp/workspace", nil)

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
	evidence := reviewEvidence{IssueIdentifier: "CAG-120", IssueTitle: "Review evidence", PRURL: "https://github.com/weskor/pi-symphony/pull/120", Workspace: "/tmp/workspace", BaseBranch: "main", HeadBranch: "symphony/CAG-120-workspace", HeadSHA: "abc123", ChangedFiles: 3, Additions: 42, Deletions: 7, ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS", ScopeSummary: "changed files matched the Linear ticket path contract", Validation: []string{"mise exec go -- make ci", "git diff --check"}, ProgressPath: "/repo/.symphony/state/run-progress/CAG-120/progress.json"}
	prompt := reviewPrompt(&issue{Identifier: "CAG-120", Title: "Review evidence"}, evidence.PRURL, evidence.Workspace, &evidence)

	for _, expected := range []string{"Runner-owned deterministic review evidence", "Head SHA: abc123", "GitHub checks: success", "go-ci=COMPLETED/SUCCESS", "Scope guard: changed files matched", "mise exec go -- make ci", "Progress snapshot", "source of truth for deterministic PR/check/scope facts", "semantic/spec quality"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestReviewEvidenceClassifiesPendingChecksAsNotReady(t *testing.T) {
	status, summary := reviewChecksStatus([]statusCheck{{Typename: "CheckRun", Name: "go-ci", Status: "IN_PROGRESS"}})
	if status != "pending" || !strings.Contains(summary, "go-ci") {
		t.Fatalf("status=%q summary=%q, want pending go-ci summary", status, summary)
	}
	err := reviewEvidenceNotReadyError(reviewEvidence{ChecksStatus: status, ChecksSummary: summary})
	if err == nil || !strings.Contains(err.Error(), "review not ready") || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("not-ready error = %v", err)
	}
}

func TestReviewEvidenceClassifiesUnknownChecksAsUnavailable(t *testing.T) {
	status, summary := reviewChecksStatus([]statusCheck{{Typename: "StatusContext", Context: "GitHub commit statuses", State: "UNKNOWN"}})
	if status != "unavailable" || !strings.Contains(summary, "UNKNOWN") {
		t.Fatalf("status=%q summary=%q, want unavailable UNKNOWN summary", status, summary)
	}
}

func TestReviewEvidenceFromPRDetailsIncludesChecksAndProgressPath(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-120")
	details := prHandoffDetails{URL: "https://github.com/weskor/pi-symphony/pull/120", BaseRefName: "main", HeadRefName: "symphony/CAG-120-workspace", HeadSHA: "abc123", ChangedFiles: 2, Additions: 10, Deletions: 1, StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Name: "go-ci", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
	evidence := reviewEvidenceFromPRDetails(&issue{Identifier: "CAG-120", Title: "Review evidence"}, workspace, details, scopeGuardResult{Checked: true}, []string{"git diff --check"}, root)

	if evidence.ChecksStatus != "success" || !strings.Contains(evidence.ChecksSummary, "go-ci") {
		t.Fatalf("checks = %q %q", evidence.ChecksStatus, evidence.ChecksSummary)
	}
	if !strings.Contains(evidence.ProgressPath, filepath.Join("run-progress", "CAG-120", "progress.json")) {
		t.Fatalf("progress path = %q", evidence.ProgressPath)
	}
}
