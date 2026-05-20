package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	artifactio "github.com/weskor/pi-symphony/internal/artifacts"
	cfg "github.com/weskor/pi-symphony/internal/config"
	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
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
	_ = writeRunRecordWithState(nil, workspace, record)
}

func writeRunRecordWithState(store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithStateFallback(store, true, workspace, record)
}

func writeRunRecordWithCommandState(store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithStateFallback(store, false, workspace, record)
}

func writeRunRecordWithStateFallback(store *state.Store, fallbackOpen bool, workspace string, record runRecord) error {
	evaluation := evaluationForRun(workspace, record)
	stateStore, dbPath, closeStore, err := stateStoreForRunRecordExport(store, fallbackOpen, record.WorkspaceRoot)
	if err != nil {
		if dbPath != "" {
			log("failed to persist run record into SQLite state at %s before artifact export: %v", dbPath, err)
		} else {
			log("failed to persist run record into SQLite state before artifact export: %v", err)
		}
		return err
	}
	if closeStore != nil {
		defer closeStore()
	}
	if stateStore != nil {
		if err := stateStore.UpsertRunArtifact(context.Background(), stateProjection{}.RunArtifact(workspace, record, evaluation)); err != nil {
			log("failed to persist run record into SQLite state before artifact export: %v", err)
			return err
		}
	}
	path, err := artifactManager().WriteRunRecord(workspace, record)
	if err != nil {
		log("failed to write run record: %v", err)
		recordArtifactExportFailure(stateStore, record, "run_record", err)
		return err
	}
	log("wrote run record: %s", path)
	evaluationPath, evaluation, err := writeEvaluationArtifactResult(workspace, record)
	if err != nil {
		recordArtifactExportFailure(stateStore, record, "evaluation", err)
		return err
	}
	logRunArtifactSummary(path, evaluationPath, record, evaluation)
	return nil
}
func stateStoreForRunRecordExport(store *state.Store, fallbackOpen bool, workspaceRoot string) (*state.Store, string, func(), error) {
	if store != nil {
		return store, "", nil, nil
	}
	if !fallbackOpen {
		return nil, "", nil, nil
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, "", nil, nil
	}
	opened, dbPath, err := openStateProjectionStore(context.Background(), workspaceRoot)
	if err != nil {
		return nil, dbPath, nil, err
	}
	return opened, dbPath, func() { _ = opened.Close() }, nil
}

func recordArtifactExportFailure(store *state.Store, record runRecord, artifact string, exportErr error) {
	if store == nil || exportErr == nil {
		return
	}
	if err := store.RecordArtifactExportFailure(context.Background(), record.IssueIdentifier, 1, artifact, exportErr.Error(), time.Now().UTC()); err != nil {
		log("failed to record artifact export failure in SQLite state: %v", err)
	}
}

func artifactManager() artifactio.Manager {
	return artifactio.Manager{
		Evaluate:       evaluationForRun,
		PRStateForURL:  prStateForURL,
		TerminalStatus: terminalRunStatus,
	}
}

func logRunArtifactSummary(runRecordPath, evaluationPath string, record runRecord, evaluation evaluationArtifact) {
	log("run summary: issue=%s status=%s outcome=%s pr=%s review=%s checks=%s next_action=%s duration_ms=%d run_record=%s evaluation=%s", emptyAsUnknown(record.IssueIdentifier), emptyAsUnknown(record.Status), emptyAsUnknown(evaluation.Outcome), emptyAsUnknown(record.PRURL), emptyAsUnknown(record.ReviewStatus), emptyAsUnknown(evaluation.ChecksStatus), emptyAsUnknown(evaluation.NextAction), record.DurationMS, runRecordPath, evaluationPath)
}

func mirrorRunRecordToState(store *state.Store, workspace string, record runRecord) {
	if store != nil {
		evaluation := evaluationForRun(workspace, record)
		if err := store.UpsertRunArtifact(context.Background(), stateProjection{}.RunArtifact(workspace, record, evaluation)); err != nil {
			log("failed to mirror run record into SQLite state: %v", err)
		}
		return
	}
	store, dbPath, err := openStateProjectionStore(context.Background(), record.WorkspaceRoot)
	if err != nil {
		if dbPath != "" {
			log("failed to mirror run record into SQLite state at %s: %v", dbPath, err)
		}
		return
	}
	defer store.Close()
	evaluation := evaluationForRun(workspace, record)
	if err := store.UpsertRunArtifact(context.Background(), stateProjection{}.RunArtifact(workspace, record, evaluation)); err != nil {
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
