package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
	orchstate "github.com/weskor/pi-symphony/internal/state"
)

type cleanupOptions struct {
	Apply      bool
	DoneIssues map[string]bool
}

func cleanupWorkspaces(workspaceRoot string, options cleanupOptions) error {
	safeRoot, err := safeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	log("mode=cleanup-workspaces; workspace_root=%s; policy=linear_done; apply=%t", workspaceRoot, options.Apply)
	entries, err := os.ReadDir(safeRoot)
	if err != nil {
		return err
	}
	removed := 0
	kept := 0
	eligible := 0
	categories := map[string]int{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		workspace, err := safeWorkspacePath(safeRoot, entry.Name())
		if err != nil {
			return err
		}
		decision, err := cleanupDecisionForRoot(safeRoot, workspace, options.DoneIssues)
		if err != nil {
			return err
		}
		categories[decision.Category]++
		if !decision.Delete {
			kept++
			mirrorCleanupState(safeRoot, decision, false, "kept", true)
			log("keep %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		eligible++
		if !options.Apply {
			mirrorCleanupState(safeRoot, decision, true, "dry_run", true)
			log("would delete %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		if err := assertSafeDeletePath(safeRoot, workspace); err != nil {
			mirrorCleanupState(safeRoot, decision, true, "failed", true)
			return err
		}
		if err := os.RemoveAll(workspace); err != nil {
			mirrorCleanupState(safeRoot, decision, true, "failed", true)
			return err
		}
		removed++
		mirrorCleanupState(safeRoot, decision, true, "deleted", false)
		log("deleted %s [%s]: %s", workspace, decision.Category, decision.Reason)
	}
	if options.Apply {
		log("cleanup summary: deleted=%d eligible=%d kept=%d categories=%s", removed, eligible, kept, formatCleanupCategories(categories))
	} else {
		log("cleanup summary: eligible=%d kept=%d categories=%s; dry run only; pass --apply to delete workspaces for Done issues", eligible, kept, formatCleanupCategories(categories))
	}
	return nil
}

func removeDoneWorkspace(workspaceRoot, identifier string) error {
	workspace, err := safeWorkspacePath(workspaceRoot, identifier)
	if err != nil {
		return err
	}
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			log("done workspace already absent for %s", identifier)
			return nil
		}
		return err
	}
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return err
	}
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	log("deleted Done issue workspace %s", workspace)
	return nil
}

type cleanupResult struct {
	Delete          bool
	Reason          string
	Category        string
	IssueIdentifier string
	ArtifactRef     string
}

func cleanupDecision(workspace string, doneIssues map[string]bool) (cleanupResult, error) {
	root := filepath.Dir(workspace)
	return cleanupDecisionForRoot(root, workspace, doneIssues)
}

func cleanupDecisionForRoot(workspaceRoot, workspace string, doneIssues map[string]bool) (cleanupResult, error) {
	if _, err := safeWorkspaceRoot(workspaceRoot); err != nil {
		return cleanupResult{}, err
	}
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return cleanupResult{Category: "unsafe", Reason: err.Error()}, nil
	}
	identifier := filepath.Base(workspace)
	if dirty, err := workspaceHasChanges(workspace); err != nil {
		return cleanupResult{}, err
	} else if dirty {
		return cleanupResult{Category: "dirty", Reason: "workspace has uncommitted or untracked files"}, nil
	}
	recordPath := filepath.Join(workspace, ".pi-symphony-run.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cleanupResult{Category: "missing-artifact", Reason: "missing run artifact"}, nil
		}
		return cleanupResult{}, err
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return cleanupResult{}, err
	}
	base := cleanupResult{IssueIdentifier: record.IssueIdentifier, ArtifactRef: recordPath}
	if reason := insufficientArtifactReason(record, workspace); reason != "" {
		base.Category = "insufficient-artifact"
		base.Reason = reason
		return base, nil
	}
	category := cleanupCategoryForTerminalStatus(record.Status)
	if !terminalRunStatus(record.Status) {
		base.Category = "non-terminal"
		base.Reason = fmt.Sprintf("run artifact status %s is not terminal", record.Status)
		return base, nil
	}
	if doneIssues[identifier] && identifier != record.IssueIdentifier {
		base.Delete = true
		base.Category = category
		base.Reason = fmt.Sprintf("workspace directory %s is Done and artifact status is %s", identifier, record.Status)
		return base, nil
	}
	if doneIssues[record.IssueIdentifier] {
		base.Delete = true
		base.Category = category
		base.Reason = fmt.Sprintf("Linear issue %s is Done and artifact status is %s", record.IssueIdentifier, record.Status)
		return base, nil
	}
	base.Category = "not-done"
	base.Reason = fmt.Sprintf("Linear issue %s is not Done", record.IssueIdentifier)
	return base, nil
}

func mirrorCleanupState(workspaceRoot string, decision cleanupResult, eligible bool, deletionResult string, workspaceExists bool) {
	if strings.TrimSpace(decision.IssueIdentifier) == "" {
		return
	}
	dbPath := orchstate.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		log("skipping sqlite cleanup mirror: state db path is empty")
		return
	}
	ctx := context.Background()
	store, err := orchstate.Open(ctx, dbPath)
	if err != nil {
		log("skipping sqlite cleanup mirror: %v", err)
		return
	}
	defer store.Close()
	if err := store.UpsertCleanupState(ctx, stateProjection{}.Cleanup(decision, eligible, deletionResult, workspaceExists, time.Now())); err != nil {
		log("skipping sqlite cleanup mirror: %v", err)
	}
}

func cleanupCategoryForTerminalStatus(status string) string {
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

func formatCleanupCategories(categories map[string]int) string {
	if len(categories) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(categories))
	for key := range categories {
		if key == "" {
			key = "unknown"
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, categories[key]))
	}
	return strings.Join(parts, ",")
}

func terminalRunStatus(status string) bool {
	switch status {
	case "success", "review_failed", "failed", "github_app_error", "canceled", "cancelled", "needs_info", "needs_info_failed", "timeout", "budget_exceeded", "merged", "superseded", "manual_repair":
		return true
	default:
		return false
	}
}

func insufficientArtifactReason(record runRecord, workspace string) string {
	if strings.TrimSpace(record.IssueIdentifier) == "" || strings.TrimSpace(record.IssueURL) == "" || strings.TrimSpace(record.Status) == "" {
		return "run artifact missing required issue/status fields"
	}
	if record.Workspace != "" {
		recorded, err := filepath.Abs(record.Workspace)
		if err != nil {
			return "run artifact has invalid workspace path"
		}
		actual, err := filepath.Abs(workspace)
		if err != nil {
			return "workspace path is invalid"
		}
		if recorded != actual {
			return fmt.Sprintf("run artifact workspace mismatch %q", record.Workspace)
		}
	}
	if record.WorkspaceRoot != "" {
		recordedRoot, err := filepath.Abs(record.WorkspaceRoot)
		if err != nil {
			return "run artifact has invalid workspace root path"
		}
		actualRoot, err := filepath.Abs(filepath.Dir(workspace))
		if err != nil {
			return "workspace root path is invalid"
		}
		if filepath.Clean(recordedRoot) != filepath.Clean(actualRoot) {
			return fmt.Sprintf("run artifact workspace root mismatch %q", record.WorkspaceRoot)
		}
	}
	if record.ExpectedBranch != "" && record.Branch != "" && record.Branch != record.ExpectedBranch {
		return fmt.Sprintf("run artifact branch mismatch %q expected %q", record.Branch, record.ExpectedBranch)
	}
	if record.Status == "review_failed" && (record.PRURL == "" || record.ReviewStatus == "" || record.ReviewFindings == "") {
		return "review-failed artifact missing PR review evidence"
	}
	return ""
}

func safeWorkspaceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) || clean == filepath.Dir(clean) {
		return "", fmt.Errorf("unsafe workspace root %q", root)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory: %s", clean)
	}
	return clean, nil
}

func safeWorkspacePath(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) || strings.Contains(name, string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe workspace name %q", name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("unsafe hidden workspace name %q", name)
	}
	safeRoot, err := safeWorkspaceRoot(root)
	if err != nil {
		return "", err
	}
	workspace := filepath.Clean(filepath.Join(safeRoot, name))
	if err := assertSafeDeletePath(safeRoot, workspace); err != nil {
		return "", err
	}
	return workspace, nil
}

func assertSafeDeletePath(root, workspace string) error {
	safeRoot, err := safeWorkspaceRoot(root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if abs == safeRoot || filepath.Dir(abs) != safeRoot {
		return fmt.Errorf("refusing unsafe workspace path %q outside root %q", workspace, safeRoot)
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink workspace path %q", workspace)
	}
	return nil
}

func currentGitBranch(workspace string) (string, error) {
	output, err := sh.CaptureQuiet("git branch --show-current", workspace)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func workspaceHasChanges(workspace string) (bool, error) {
	output, err := sh.CaptureQuiet("git status --porcelain", workspace)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "??"))
		switch path {
		case ".pi-symphony-run.json", ".pi-symphony-evaluation.json", ".pi-symphony-prompt.md", ".pi-symphony-review-prompt.md":
			continue
		default:
			return true, nil
		}
	}
	return false, nil
}
