package cleanup

import (
	"strings"

	gatepkg "github.com/weskor/agent-machine/internal/gate"
)

type Decision struct {
	Delete          bool
	Reason          string
	Category        string
	IssueIdentifier string
	ArtifactRef     string
	WorkspacePath   string
}

func GateResult(decision Decision) gatepkg.Result {
	subject := firstNonEmpty(decision.IssueIdentifier, decision.WorkspacePath)
	result := gatepkg.NewResult("cleanup", subject)
	result.ReasonText = decision.Reason
	result.Metadata = map[string]string{}
	if decision.Category != "" {
		result.Metadata["category"] = decision.Category
	}
	if decision.ArtifactRef != "" {
		result.Metadata["artifact_ref"] = decision.ArtifactRef
	}
	if decision.WorkspacePath != "" {
		result.Metadata["workspace_path"] = decision.WorkspacePath
	}
	if len(result.Metadata) == 0 {
		result.Metadata = nil
	}
	if decision.Delete {
		result.NextAction = "delete_workspace"
		return result
	}
	code := GateCode(decision.Category)
	if decision.Category == "reconciliation-needed" {
		result.ReconciliationNeeded(code, decision.Reason, "repair_or_reconcile_cleanup_state")
		return result
	}
	result.Block(code, decision.Reason)
	result.NextAction = "keep_workspace"
	return result
}

func GateCode(category string) string {
	category = strings.TrimSpace(category)
	if category == "" {
		return "cleanup_blocked"
	}
	return "cleanup_" + strings.ReplaceAll(category, "-", "_")
}

func DeletionResult(decision Decision, fallback string) string {
	if decision.Category == "reconciliation-needed" {
		return "reconciliation_needed"
	}
	return fallback
}

func CategoryForTerminalStatus(status string) string {
	switch status {
	case "success":
		return "completed"
	case "canceled", "cancelled":
		return "canceled"
	case "review_failed":
		return "failed-review"
	case "needs_info", "needs_info_failed":
		return "needs-info"
	case "timeout":
		return "timeout"
	case "budget_exceeded":
		return "budget-exceeded"
	case "merged":
		return "merged"
	case "superseded":
		return "superseded"
	case "manual_repair":
		return "manual-repair"
	default:
		return "failed"
	}
}

func TerminalRunStatus(status string) bool {
	switch status {
	case "success", "review_failed", "failed", "github_app_error", "canceled", "cancelled", "needs_info", "needs_info_failed", "timeout", "budget_exceeded", "merged", "superseded", "manual_repair":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
