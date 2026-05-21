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

	orchstate "github.com/weskor/pi-symphony/internal/state"
	ws "github.com/weskor/pi-symphony/internal/workspace"
)

type cleanupOptions struct {
	Apply      bool
	DoneIssues map[string]bool
	StateStore *orchstate.Store
}

type cleanupPRFacts struct {
	State       string
	Merged      bool
	HeadRefName string
	BaseRefName string
}

var cleanupPRFactsForURL = githubCleanupPRFactsForURL

func cleanupWorkspaces(workspaceRoot string, options cleanupOptions) error {
	safeRoot, err := safeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	store := options.StateStore
	if store == nil {
		opened, dbPath, openErr := openStateProjectionStore(context.Background(), safeRoot)
		if openErr != nil {
			if options.Apply {
				return fmt.Errorf("cleanup requires SQLite for mutating apply: %w", openErr)
			}
			if dbPath != "" {
				log("SQLite cleanup degraded: open path=%s error=%q", dbPath, openErr.Error())
			} else {
				log("SQLite cleanup degraded: %v", openErr)
			}
		} else {
			store = opened
			defer store.Close()
		}
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
		if entry.Name() == "state" {
			continue
		}
		workspace, err := safeWorkspacePath(safeRoot, entry.Name())
		if err != nil {
			return err
		}
		decision, err := cleanupDecisionForRoot(safeRoot, workspace, options.DoneIssues)
		if store != nil {
			decision, err = cleanupDecisionFromSQLite(context.Background(), store, safeRoot, workspace, options.DoneIssues, decision)
		}
		if err != nil {
			return err
		}
		categories[decision.Category]++
		if !decision.Delete {
			kept++
			mirrorCleanupState(store, safeRoot, decision, false, cleanupDeletionResult(decision, "kept"), true)
			log("keep %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		eligible++
		if !options.Apply {
			mirrorCleanupState(store, safeRoot, decision, true, "dry_run", true)
			log("would delete %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		if err := assertSafeDeletePath(safeRoot, workspace); err != nil {
			mirrorCleanupState(store, safeRoot, decision, true, "failed", true)
			return err
		}
		if err := os.RemoveAll(workspace); err != nil {
			mirrorCleanupState(store, safeRoot, decision, true, "failed", true)
			return err
		}
		removed++
		mirrorCleanupState(store, safeRoot, decision, true, "deleted", false)
		log("deleted %s [%s]: %s", workspace, decision.Category, decision.Reason)
	}
	if options.Apply {
		log("cleanup summary: deleted=%d eligible=%d kept=%d categories=%s", removed, eligible, kept, formatCleanupCategories(categories))
	} else {
		log("cleanup summary: eligible=%d kept=%d categories=%s; dry run only; pass --apply to delete workspaces for Done issues", eligible, kept, formatCleanupCategories(categories))
	}
	return nil
}

func cleanupDeletionResult(decision cleanupResult, fallback string) string {
	if decision.Category == "reconciliation-needed" {
		return "reconciliation_needed"
	}
	return fallback
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
	return cleanupDecisionForRootWithChanges(workspaceRoot, workspace, doneIssues, workspaceHasChanges)
}

type workspaceChangeChecker func(string) (bool, error)

func cleanupDecisionForRootWithChanges(workspaceRoot, workspace string, doneIssues map[string]bool, hasChanges workspaceChangeChecker) (cleanupResult, error) {
	if _, err := safeWorkspaceRoot(workspaceRoot); err != nil {
		return cleanupResult{}, err
	}
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return cleanupResult{Category: "unsafe", Reason: err.Error()}, nil
	}
	identifier := filepath.Base(workspace)
	if dirty, err := hasChanges(workspace); err != nil {
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

func cleanupDecisionFromSQLite(ctx context.Context, store *orchstate.Store, workspaceRoot, workspace string, doneIssues map[string]bool, artifactDecision cleanupResult) (cleanupResult, error) {
	identifier := filepath.Base(workspace)
	facts, ok, err := store.CleanupFacts(ctx, identifier)
	if err != nil {
		return cleanupResult{}, err
	}
	if !ok {
		return cleanupResult{IssueIdentifier: identifier, Category: "reconciliation-needed", Reason: fmt.Sprintf("SQLite has no issue attempt row for workspace %s", identifier)}, nil
	}
	base := cleanupResult{IssueIdentifier: facts.IssueKey, ArtifactRef: firstNonEmpty(facts.ArtifactRef, artifactDecision.ArtifactRef)}
	if facts.WorkspacePath != "" && filepath.Clean(facts.WorkspacePath) != filepath.Clean(workspace) {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite workspace path %s conflicts with workspace %s", facts.WorkspacePath, workspace)
		return base, nil
	}
	if artifactDecision.IssueIdentifier != "" && artifactDecision.IssueIdentifier != facts.IssueKey {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("run artifact issue %s conflicts with SQLite issue %s", artifactDecision.IssueIdentifier, facts.IssueKey)
		return base, nil
	}
	if artifactDecision.Category == "dirty" || artifactDecision.Category == "unsafe" || artifactDecision.Category == "missing-artifact" || artifactDecision.Category == "insufficient-artifact" {
		artifactDecision.IssueIdentifier = facts.IssueKey
		return artifactDecision, nil
	}
	status := facts.Status
	if status == "" {
		status = facts.TerminalOutcome
	}
	category := cleanupCategoryForTerminalStatus(status)
	if !terminalRunStatus(status) {
		if doneIssues[facts.IssueKey] || (doneIssues[identifier] && identifier != facts.IssueKey) {
			return cleanupDecisionFromMergedPR(facts, base, status)
		}
		base.Category = "non-terminal"
		base.Reason = fmt.Sprintf("SQLite status %s is not terminal", status)
		return base, nil
	}
	if doneIssues[facts.IssueKey] || (doneIssues[identifier] && identifier != facts.IssueKey) {
		base.Delete = true
		base.Category = category
		base.Reason = fmt.Sprintf("SQLite issue %s is Done and durable status is %s", facts.IssueKey, status)
		return base, nil
	}
	base.Category = "not-done"
	base.Reason = fmt.Sprintf("SQLite issue %s is not Done", facts.IssueKey)
	return base, nil
}

func cleanupDecisionFromMergedPR(facts orchstate.CleanupFacts, base cleanupResult, status string) (cleanupResult, error) {
	if strings.TrimSpace(facts.PRURL) == "" {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done but non-terminal status %s has no PR mapping", facts.IssueKey, status)
		return base, nil
	}
	expectedBranch := expectedWorkspaceBranch(facts.IssueKey)
	if branch := strings.TrimSpace(facts.BranchName); branch == "" || branch != expectedBranch {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done but PR mapping branch %s does not match expected branch %s", facts.IssueKey, emptyAsNA(facts.BranchName), expectedBranch)
		return base, nil
	}
	prFacts, err := cleanupPRFactsForURL(facts.PRURL)
	if err != nil {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done but PR state lookup failed for %s: %v", facts.IssueKey, facts.PRURL, err)
		return base, nil
	}
	if prFacts.HeadRefName != expectedBranch {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done but PR %s head branch %s does not match expected branch %s", facts.IssueKey, facts.PRURL, emptyAsNA(prFacts.HeadRefName), expectedBranch)
		return base, nil
	}
	if baseBranch := strings.TrimSpace(facts.BaseBranch); baseBranch != "" && prFacts.BaseRefName != "" && prFacts.BaseRefName != baseBranch {
		base.Category = "reconciliation-needed"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done but PR %s base branch %s does not match expected base %s", facts.IssueKey, facts.PRURL, prFacts.BaseRefName, baseBranch)
		return base, nil
	}
	if prFacts.Merged || strings.EqualFold(prFacts.State, "MERGED") {
		base.Delete = true
		base.Category = "merged-pr"
		base.Reason = fmt.Sprintf("SQLite issue %s is Done, PR %s is merged, and durable status is %s", facts.IssueKey, facts.PRURL, status)
		return base, nil
	}
	base.Category = "reconciliation-needed"
	base.Reason = fmt.Sprintf("SQLite issue %s is Done but PR %s is state %s and durable status is %s", facts.IssueKey, facts.PRURL, emptyAsUnknown(prFacts.State), status)
	return base, nil
}

func githubCleanupPRFactsForURL(prURL string) (cleanupPRFacts, error) {
	owner, repo, ok := parseGitHubPRRepository(prURL)
	if !ok {
		return cleanupPRFacts{}, fmt.Errorf("invalid GitHub PR URL %q", prURL)
	}
	expectedOwner, expectedRepo, err := currentGitHubRepo()
	if err != nil {
		return cleanupPRFacts{}, err
	}
	if !strings.EqualFold(owner, expectedOwner) || !strings.EqualFold(repo, expectedRepo) {
		return cleanupPRFacts{}, fmt.Errorf("PR repository is %s/%s; expected %s/%s", owner, repo, expectedOwner, expectedRepo)
	}
	github, ctx, cancel, err := githubClientWithTimeout(defaultGitHubCommandTimeout)
	if err != nil {
		return cleanupPRFacts{}, err
	}
	defer cancel()
	details, err := github.PullRequestHandoffDetails(ctx, prURL)
	if err != nil {
		return cleanupPRFacts{}, err
	}
	state, merged, err := github.PullRequestState(ctx, prURL)
	if err != nil {
		return cleanupPRFacts{}, err
	}
	return cleanupPRFacts{State: state, Merged: merged, HeadRefName: details.HeadRefName, BaseRefName: details.BaseRefName}, nil
}

func mirrorCleanupState(store *orchstate.Store, workspaceRoot string, decision cleanupResult, eligible bool, deletionResult string, workspaceExists bool) {
	if strings.TrimSpace(decision.IssueIdentifier) == "" {
		return
	}
	ctx := context.Background()
	if store != nil {
		if err := store.UpsertCleanupState(ctx, stateProjection{}.Cleanup(decision, eligible, deletionResult, workspaceExists, time.Now())); err != nil {
			log("skipping sqlite cleanup mirror: %v", err)
		}
		return
	}
	dbPath := orchstate.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		log("skipping sqlite cleanup mirror: state db path is empty")
		return
	}
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
	return ws.SafeRoot(root)
}

func safeWorkspacePath(root, name string) (string, error) {
	return ws.SafePath(root, name)
}

func assertSafeDeletePath(root, workspace string) error {
	return ws.AssertSafeDeletePath(root, workspace)
}

func currentGitBranch(workspace string) (string, error) {
	return ws.CurrentGitBranch(workspace)
}

func workspaceHasChanges(workspace string) (bool, error) {
	return ws.HasChanges(workspace)
}
