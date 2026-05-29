package mergegate

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/gate"
)

func TestEvaluatePullRequestBlocksConflicts(t *testing.T) {
	pr := PullRequest{Subject: "https://example.test/pr/1", HeadRefName: "am/CAG-1-workspace", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}

	decision := EvaluatePullRequest(pr)

	if decision.Eligible || decision.Status != gate.StatusBlocked {
		t.Fatalf("decision = eligible %t status %q, want blocked", decision.Eligible, decision.Status)
	}
	if !contains(decision.Codes(), "merge_conflict") {
		t.Fatalf("codes = %#v, want merge_conflict", decision.Codes())
	}
	if !strings.Contains(decision.Reason(), "conflicts with the base branch") {
		t.Fatalf("reason = %q", decision.Reason())
	}
}

func TestChecksBlockReason(t *testing.T) {
	tests := []struct {
		name   string
		checks []StatusCheck
		want   string
	}{
		{
			name:   "green check run passes",
			checks: []StatusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
		},
		{
			name:   "neutral check blocks",
			checks: []StatusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "NEUTRAL", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=NEUTRAL`,
		},
		{
			name:   "pending status context blocks",
			checks: []StatusCheck{{Typename: "StatusContext", State: "PENDING", Context: "Vercel"}},
			want:   `status context "Vercel" is state=PENDING`,
		},
		{
			name:   "unknown check shape blocks",
			checks: []StatusCheck{{Typename: "MysteryCheck", Name: "deploy"}},
			want:   `unknown status check shape "MysteryCheck" for "deploy"`,
		},
		{
			name: "no checks blocks",
			want: "no status checks were reported by GitHub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChecksBlockReason(tt.checks)
			if got != tt.want {
				t.Fatalf("ChecksBlockReason() = %q, want %q", got, tt.want)
			}
			if (got == "") != ChecksPassed(tt.checks) {
				t.Fatalf("ChecksPassed mismatch for reason %q", got)
			}
		})
	}
}

func TestChecksStatusClassifiesReviewReadiness(t *testing.T) {
	tests := []struct {
		name        string
		checks      []StatusCheck
		wantStatus  string
		wantSummary string
	}{
		{
			name:        "none unavailable",
			wantStatus:  "unavailable",
			wantSummary: "no status checks were reported by the code host",
		},
		{
			name:        "success summarizes all checks",
			checks:      []StatusCheck{{Typename: "CheckRun", Name: "go-ci", Status: "COMPLETED", Conclusion: "SUCCESS"}, {Typename: "StatusContext", Context: "Vercel", State: "SUCCESS"}},
			wantStatus:  "success",
			wantSummary: "go-ci=COMPLETED/SUCCESS; Vercel=SUCCESS",
		},
		{
			name:        "pending check run",
			checks:      []StatusCheck{{Typename: "CheckRun", Name: "go-ci", Status: "IN_PROGRESS"}},
			wantStatus:  "pending",
			wantSummary: `check run "go-ci" is status=IN_PROGRESS conclusion=UNKNOWN`,
		},
		{
			name:        "unknown status context unavailable",
			checks:      []StatusCheck{{Typename: "StatusContext", Context: "GitHub commit statuses", State: "UNKNOWN"}},
			wantStatus:  "unavailable",
			wantSummary: `status context "GitHub commit statuses" is state=UNKNOWN`,
		},
		{
			name:        "failed check run",
			checks:      []StatusCheck{{Typename: "CheckRun", Name: "go-ci", Status: "COMPLETED", Conclusion: "FAILURE"}},
			wantStatus:  "failed",
			wantSummary: `check run "go-ci" is status=COMPLETED conclusion=FAILURE`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, summary := ChecksStatus(tt.checks)
			if status != tt.wantStatus || summary != tt.wantSummary {
				t.Fatalf("ChecksStatus() = %q, %q; want %q, %q", status, summary, tt.wantStatus, tt.wantSummary)
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
