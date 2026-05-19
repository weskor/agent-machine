package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	cfg "github.com/weskor/pi-symphony/internal/config"
	sh "github.com/weskor/pi-symphony/internal/shell"
	orchstate "github.com/weskor/pi-symphony/internal/state"
)

func closeInvalidPR(prURL, reason string) error {
	comment := fmt.Sprintf("Closing because the Pi Symphony runner PR sanity check failed before handoff.\n\nReason: %s\n\nDo not merge this PR as-is; retry the Linear issue only after fixing branch/base/scope controls.", reason)
	return sh.RunWithTimeout(fmt.Sprintf("gh pr close %s --comment %s", sh.Quote(prURL), sh.Quote(comment)), "", defaultGitHubCommandTimeout)
}

func ensureIsolatedWorkspace(workspaceRoot, workspace, identifier string) error {
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return err
	}
	topLevel, err := sh.CaptureQuiet("git rev-parse --show-toplevel", workspace)
	if err != nil {
		return fmt.Errorf("workspace %s is not a git checkout: %w", workspace, err)
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if filepath.Clean(topAbs) != filepath.Clean(workspaceAbs) {
		return fmt.Errorf("refusing shared git checkout: top-level %s does not match workspace %s", strings.TrimSpace(topLevel), workspace)
	}
	branch := expectedWorkspaceBranch(identifier)
	current, err := currentGitBranch(workspace)
	if err != nil {
		return err
	}
	if current == branch {
		return nil
	}
	if current != "" && strings.HasPrefix(current, "symphony/") {
		return fmt.Errorf("workspace %s is on unexpected Symphony branch %q; expected %q", workspace, current, branch)
	}
	if err := sh.Run("git switch -C "+sh.Quote(branch), workspace); err != nil {
		return err
	}
	return nil
}

func writeRunRecord(workspace string, record runRecord) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log("failed to encode run record: %v", err)
		return
	}
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		log("failed to write run record: %v", err)
		return
	}
	log("wrote run record: %s", path)
	writeEvaluationArtifact(workspace, record)
	mirrorRunRecordToState(workspace, record)
}

func mirrorRunRecordToState(workspace string, record runRecord) {
	dbPath := orchstate.DefaultDBPath(record.WorkspaceRoot)
	if dbPath == "" {
		return
	}
	ctx := context.Background()
	store, err := orchstate.Open(ctx, dbPath)
	if err != nil {
		log("failed to mirror run record into SQLite state at %s: %v", dbPath, err)
		return
	}
	defer store.Close()
	evaluation := evaluationForRun(workspace, record)
	repo, prNumber := parseGitHubPR(record.PRURL)
	reviewHash := ""
	if strings.TrimSpace(record.ReviewFindings) != "" {
		sum := sha256.Sum256([]byte(record.ReviewFindings))
		reviewHash = fmt.Sprintf("%x", sum[:])
	}
	retryReason := ""
	retryNextState := ""
	if evaluation.ShouldRetry {
		retryReason = evaluation.RootCause
		if retryReason == "" {
			retryReason = evaluation.Outcome
		}
		retryNextState = evaluation.NextAction
	}
	terminalOutcome := ""
	if terminalRunStatus(record.Status) {
		terminalOutcome = evaluation.Outcome
	}
	if err := store.UpsertRunArtifact(ctx, orchstate.RunArtifactSnapshot{
		IssueKey:             record.IssueIdentifier,
		IssueID:              record.IssueID,
		Attempt:              1,
		WorkspacePath:        record.Workspace,
		BranchName:           firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:           baseBranchForWorkspace(workspace),
		Status:               record.Status,
		StartedAt:            record.StartedAt,
		UpdatedAt:            record.EndedAt,
		Repository:           repo,
		PRNumber:             prNumber,
		PRURL:                record.PRURL,
		ReviewStatus:         record.ReviewStatus,
		ReviewPassed:         record.ReviewStatus == "passed",
		ReviewClassification: record.ReviewClassification,
		ReviewOutputRef:      filepath.Join(workspace, evaluationArtifactName),
		ReviewOutputHash:     reviewHash,
		MergeEligible:        evaluation.MergeEligible,
		FeedbackHash:         record.FeedbackHash,
		FeedbackNextAction:   evaluation.NextAction,
		RetryCount:           evaluation.FeedbackRetryCount,
		RetryBudgetState:     record.BudgetExceeded,
		RetryReason:          retryReason,
		RetryInputHash:       record.FeedbackHash,
		RetryNextState:       retryNextState,
		TerminalOutcome:      terminalOutcome,
		TerminalReason:       evaluation.RootCause,
		RunArtifactRef:       filepath.Join(workspace, ".pi-symphony-run.json"),
		EvaluationRef:        filepath.Join(workspace, evaluationArtifactName),
	}); err != nil {
		log("failed to mirror run record into SQLite state at %s: %v", dbPath, err)
	}
}

func baseBranchForWorkspace(workspace string) string {
	wf, err := cfg.ReadWorkflow(filepath.Join(workspace, "WORKFLOW.md"))
	if err != nil {
		return "main"
	}
	base := cfg.BaseBranchFromWorkflow(wf.YAML)
	if strings.TrimSpace(base) == "" {
		return "main"
	}
	return base
}

var githubPRPattern = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/(\d+)`)

func parseGitHubPR(prURL string) (string, int) {
	matches := githubPRPattern.FindStringSubmatch(strings.TrimSpace(prURL))
	if len(matches) != 3 {
		return "", 0
	}
	n, _ := strconv.Atoi(matches[2])
	return matches[1], n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stateID(states []workflowState, name string) string {
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	return ""
}

func renderPrompt(template string, issue issue, attempt int) string {
	replacer := strings.NewReplacer("{{issue.identifier}}", issue.Identifier, "{{issue.title}}", issue.Title, "{{issue.description}}", issue.Description, "{{issue.url}}", issue.URL, "{{issue.state}}", issue.State.Name, "{{attempt}}", fmt.Sprint(attempt))
	return replacer.Replace(template)
}
