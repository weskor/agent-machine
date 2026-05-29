package mergegate

import (
	"fmt"
	"strings"

	"github.com/weskor/agent-machine/internal/gate"
)

type PullRequest struct {
	Subject          string
	HeadRefName      string
	Mergeable        string
	MergeStateStatus string
	StatusChecks     []StatusCheck
}

type StatusCheck struct {
	Typename   string
	Status     string
	Conclusion string
	State      string
	Name       string
	Context    string
}

type RunRecord struct {
	Subject string
	Status  string
	Error   string
}

type Decision struct {
	Eligible bool
	gate.Result
}

func EvaluatePullRequest(pr PullRequest) Decision {
	decision := NewDecision(pr.Subject)
	if reason := ConflictReason(pr); reason != "" {
		decision.BlockMerge("merge_conflict", reason)
		return decision
	}
	if !strings.EqualFold(pr.Mergeable, "MERGEABLE") {
		decision.BlockMerge("mergeable_unknown", fmt.Sprintf("GitHub reports mergeable=%s; waiting for a fresh mergeable result before merging %s.", emptyAsUnknown(pr.Mergeable), pr.HeadRefName))
		return decision
	}
	if reason := ChecksBlockReason(pr.StatusChecks); reason != "" {
		decision.BlockMerge("status_checks", reason)
		return decision
	}
	return decision
}

func EvaluateRunRecord(record RunRecord) Decision {
	decision := NewDecision(record.Subject)
	if record.Status == "review_failed" {
		decision.BlockMerge("review_decision", "review did not pass")
		return decision
	}
	if strings.Contains(strings.ToLower(record.Error), "check") {
		decision.BlockMerge("status_checks", record.Error)
		return decision
	}
	return decision
}

func NewDecision(subject string) Decision {
	return Decision{Eligible: true, Result: gate.NewResult("merge", subject)}
}

func (d *Decision) BlockMerge(code, reason string) {
	d.Eligible = false
	d.Block(code, reason)
}

func ConflictReason(pr PullRequest) string {
	if strings.EqualFold(pr.Mergeable, "CONFLICTING") || strings.EqualFold(pr.MergeStateStatus, "DIRTY") {
		return fmt.Sprintf("GitHub reports mergeable=%s mergeStateStatus=%s; branch %s has conflicts with the base branch.", emptyAsUnknown(pr.Mergeable), emptyAsUnknown(pr.MergeStateStatus), pr.HeadRefName)
	}
	return ""
}

func ChecksPassed(checks []StatusCheck) bool {
	return ChecksBlockReason(checks) == ""
}

func ChecksStatus(checks []StatusCheck) (string, string) {
	if len(checks) == 0 {
		return "unavailable", "no status checks were reported by the code host"
	}
	if reason := ChecksBlockReason(checks); reason == "" {
		return "success", SummarizeStatusChecks(checks)
	}
	for _, check := range checks {
		if check.Typename == "CheckRun" && (strings.EqualFold(check.Status, "UNKNOWN") || strings.EqualFold(check.Conclusion, "UNKNOWN")) {
			return "unavailable", ChecksBlockReason(checks)
		}
		if check.Typename == "StatusContext" && strings.EqualFold(check.State, "UNKNOWN") {
			return "unavailable", ChecksBlockReason(checks)
		}
		if check.Typename == "CheckRun" && !strings.EqualFold(check.Status, "COMPLETED") {
			return "pending", ChecksBlockReason(checks)
		}
		if check.Typename == "StatusContext" && strings.EqualFold(check.State, "PENDING") {
			return "pending", ChecksBlockReason(checks)
		}
	}
	return "failed", ChecksBlockReason(checks)
}

func SummarizeStatusChecks(checks []StatusCheck) string {
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		label := CheckLabel(check)
		switch check.Typename {
		case "CheckRun":
			parts = append(parts, fmt.Sprintf("%s=%s/%s", label, emptyAsUnknown(check.Status), emptyAsUnknown(check.Conclusion)))
		case "StatusContext":
			parts = append(parts, fmt.Sprintf("%s=%s", label, emptyAsUnknown(check.State)))
		default:
			parts = append(parts, fmt.Sprintf("%s=%s", label, emptyAsUnknown(check.Typename)))
		}
	}
	return strings.Join(parts, "; ")
}

func ChecksBlockReason(checks []StatusCheck) string {
	if len(checks) == 0 {
		return "no status checks were reported by GitHub"
	}
	for _, check := range checks {
		switch check.Typename {
		case "CheckRun":
			if !strings.EqualFold(check.Status, "COMPLETED") || !strings.EqualFold(check.Conclusion, "SUCCESS") {
				return fmt.Sprintf("check run %q is status=%s conclusion=%s", CheckLabel(check), emptyAsUnknown(check.Status), emptyAsUnknown(check.Conclusion))
			}
		case "StatusContext":
			if !strings.EqualFold(check.State, "SUCCESS") {
				return fmt.Sprintf("status context %q is state=%s", CheckLabel(check), emptyAsUnknown(check.State))
			}
		default:
			return fmt.Sprintf("unknown status check shape %q for %q", emptyAsUnknown(check.Typename), CheckLabel(check))
		}
	}
	return ""
}

func CheckLabel(check StatusCheck) string {
	if strings.TrimSpace(check.Name) != "" {
		return check.Name
	}
	if strings.TrimSpace(check.Context) != "" {
		return check.Context
	}
	return "unnamed"
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UNKNOWN"
	}
	return value
}
