package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

type handoffCompletion struct {
	client          linearClient
	config          runnerConfig
	stateStore      *state.Store
	candidate       *issue
	states          []workflowState
	workspace       string
	branch          string
	progressStarted time.Time
	startedAt       time.Time
	runtimeUsage    *usage
	review          *reviewResult
	prURL           string
	validation      []string
	scopeResult     scopeGuardResult
	githubAuth      string
}

type handoffPendingPayload struct {
	SchemaVersion    int              `json:"schema_version"`
	IssueID          string           `json:"issue_id,omitempty"`
	IssueIdentifier  string           `json:"issue_identifier"`
	IssueTitle       string           `json:"issue_title,omitempty"`
	IssueURL         string           `json:"issue_url,omitempty"`
	IssueDescription string           `json:"issue_description,omitempty"`
	TeamID           string           `json:"team_id,omitempty"`
	Workspace        string           `json:"workspace"`
	Branch           string           `json:"branch,omitempty"`
	ProgressStarted  time.Time        `json:"progress_started_at"`
	StartedAt        time.Time        `json:"started_at"`
	RuntimeUsage     *usage           `json:"runtime_usage,omitempty"`
	PiUsage          *usage           `json:"pi_usage,omitempty"`
	Review           *reviewResult    `json:"review,omitempty"`
	PRURL            string           `json:"pr_url"`
	Validation       []string         `json:"validation,omitempty"`
	ScopeResult      scopeGuardResult `json:"scope_result,omitempty"`
	GitHubAuth       string           `json:"github_auth,omitempty"`
}

type handoffWorker struct {
	client       linearClient
	config       runnerConfig
	stateStore   *state.Store
	candidate    *issue
	states       []workflowState
	workspace    string
	startedAt    time.Time
	runtimeUsage *usage
	review       *reviewResult
	prURL        string
	validation   []string
	scopeResult  scopeGuardResult
	githubAuth   string
}

type handoffWorkerResult struct {
	Summary  *handoffSummary
	Terminal bool
}

func completeAttemptHandoff(ctx context.Context, input handoffCompletion) (bool, error) {
	writeHandoffPendingStateContext(ctx, input)
	if input.candidate == nil {
		return false, nil
	}
	payload, err := readHandoffPendingPayloadForCompletion(input.config.WorkspaceRoot, input.candidate.Identifier)
	if err != nil {
		return true, err
	}
	return executeHandoffPendingPayload(ctx, input.client, input.config, input.stateStore, payload, input.states)
}

func executeHandoffPendingPayload(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, payload handoffPendingPayload, states []workflowState) (bool, error) {
	return executeAttemptHandoff(ctx, payload.Completion(client, config, stateStore, states))
}

func executeAttemptHandoff(ctx context.Context, input handoffCompletion) (bool, error) {
	handoffResult, err := handoffWorker{
		client:       input.client,
		config:       input.config,
		stateStore:   input.stateStore,
		candidate:    input.candidate,
		states:       input.states,
		workspace:    input.workspace,
		startedAt:    input.startedAt,
		runtimeUsage: input.runtimeUsage,
		review:       input.review,
		prURL:        input.prURL,
		validation:   input.validation,
		scopeResult:  input.scopeResult,
		githubAuth:   input.githubAuth,
	}.Execute(ctx)
	if err != nil || handoffResult.Terminal {
		return true, err
	}
	if err := writeRunRecordWithCommandStateContext(ctx, input.stateStore, input.workspace, runRecordFor(input.candidate, input.workspace, input.config.RuntimeImplementationCommand(), input.githubAuth, input.startedAt, time.Now(), input.runtimeUsage, input.review, input.prURL, runAttemptStatusSuccess, "", input.config.Budget.Active(), "")); err != nil {
		return true, err
	}
	return true, nil
}

func (w handoffWorker) Execute(ctx context.Context) (handoffWorkerResult, error) {
	if w.prURL == "" {
		return handoffWorkerResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Terminal: true}, err
	}

	logHandoffRunSummary(w.candidate.Identifier, w.prURL, w.review, w.validation)
	classificationRecord := runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusSuccess, "", w.config.Budget.Active(), "")
	classification := classifyRunRecord(w.workspace, classificationRecord)
	summary := handoffSummary{
		IssueIdentifier:  w.candidate.Identifier,
		IssueTitle:       w.candidate.Title,
		IssueURL:         w.candidate.URL,
		IssueDescription: w.candidate.Description,
		PRURL:            w.prURL,
		RuntimeUsage:     w.runtimeUsage,
		Review:           w.review,
		Duration:         time.Since(w.startedAt),
		Validation:       w.validation,
		ScopeResult:      w.scopeResult,
		FollowUps:        followUpLines(w.review),
		Classification:   &classification,
	}
	if progress, err := readRunProgress(w.config.WorkspaceRoot, w.candidate.Identifier); err == nil {
		summary.Progress = &progress
	}
	if err := updatePRHandoffBodyForWorker(summary); err != nil {
		writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	linearStatus := newLinearStatusWorker(w.client, w.candidate, w.states)
	if stateID(w.states, w.config.HandoffState) != "" {
		if _, err := linearStatus.MoveToContext(ctx, w.config.HandoffState); err != nil {
			writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
			return handoffWorkerResult{Summary: &summary, Terminal: true}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	comment := renderLinearHandoffComment(summary)
	if err := linearStatus.CommentContext(ctx, comment); err != nil {
		log("failed to comment on %s: %v", w.candidate.Identifier, err)
	}
	return handoffWorkerResult{Summary: &summary}, nil
}

func logHandoffRunSummary(issueIdentifier, prURL string, review *reviewResult, validation []string) {
	log("handoff summary: issue=%s pr=%s review=%s validation=%s", emptyAsUnknown(issueIdentifier), emptyAsUnknown(prURL), reviewStatusSummary(review), validationSummary(validation))
}

func reviewStatusSummary(review *reviewResult) string {
	if review == nil || strings.TrimSpace(review.Status) == "" {
		return "not_configured"
	}
	return review.Status
}

func validationSummary(lines []string) string {
	var cleaned []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return "not_reported"
	}
	const maxValidationSummaryLines = 3
	if len(cleaned) > maxValidationSummaryLines {
		cleaned = append(cleaned[:maxValidationSummaryLines], fmt.Sprintf("...+%d more", len(cleaned)-maxValidationSummaryLines))
	}
	return strings.Join(cleaned, " | ")
}

var readHandoffPendingPayloadForCompletion = readHandoffPendingPayload

var updatePRHandoffBodyForWorker = updatePRHandoffBody

func resetHandoffWorkerHooks() {
	updatePRHandoffBodyForWorker = updatePRHandoffBody
	readHandoffPendingPayloadForCompletion = readHandoffPendingPayload
	githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment
	resetLinearStatusWorkerHooks()
}

func handoffPendingPayloadFromCompletion(input handoffCompletion) handoffPendingPayload {
	payload := handoffPendingPayload{
		SchemaVersion:   1,
		Workspace:       input.workspace,
		Branch:          input.branch,
		ProgressStarted: input.progressStarted,
		StartedAt:       input.startedAt,
		RuntimeUsage:    input.runtimeUsage,
		Review:          input.review,
		PRURL:           input.prURL,
		Validation:      boundedHandoffValidation(input.validation),
		ScopeResult:     input.scopeResult,
		GitHubAuth:      input.githubAuth,
	}
	if input.candidate != nil {
		payload.IssueID = input.candidate.ID
		payload.IssueIdentifier = input.candidate.Identifier
		payload.IssueTitle = input.candidate.Title
		payload.IssueURL = input.candidate.URL
		payload.IssueDescription = input.candidate.Description
		payload.TeamID = input.candidate.Team.ID
	}
	return payload
}

func (p handoffPendingPayload) Completion(client linearClient, config runnerConfig, store *state.Store, states []workflowState) handoffCompletion {
	candidate := &issue{ID: p.IssueID, Identifier: p.IssueIdentifier, Title: p.IssueTitle, URL: p.IssueURL, Description: p.IssueDescription}
	candidate.Team.ID = p.TeamID
	return handoffCompletion{
		client:          client,
		config:          config,
		stateStore:      store,
		candidate:       candidate,
		states:          states,
		workspace:       p.Workspace,
		branch:          p.Branch,
		progressStarted: p.ProgressStarted,
		startedAt:       p.StartedAt,
		runtimeUsage:    p.RuntimeUsage,
		review:          p.Review,
		prURL:           p.PRURL,
		validation:      append([]string(nil), p.Validation...),
		scopeResult:     p.ScopeResult,
		githubAuth:      p.GitHubAuth,
	}
}

func writeHandoffPendingState(input handoffCompletion) {
	writeHandoffPendingStateContext(context.Background(), input)
}

func writeHandoffPendingStateContext(ctx context.Context, input handoffCompletion) {
	if err := ctx.Err(); err != nil {
		log("skipping handoff pending state export for canceled context: %v", err)
		return
	}
	payload := handoffPendingPayloadFromCompletion(input)
	if err := writeHandoffPendingPayload(input.config.WorkspaceRoot, payload); err != nil {
		identifier := ""
		if input.candidate != nil {
			identifier = input.candidate.Identifier
		}
		log("failed to write handoff pending payload for %s: %v", emptyAsUnknown(identifier), err)
	} else if path, err := handoffPendingPayloadPath(input.config.WorkspaceRoot, payload.IssueIdentifier); err == nil {
		if err := recordHandoffPendingPayloadRefContext(ctx, input.stateStore, payload, path); err != nil {
			log("failed to write handoff pending state ref for %s: %v", emptyAsUnknown(payload.IssueIdentifier), err)
		}
	}
	writeHandoffPendingProgress(input)
}

func writeHandoffPendingProgress(input handoffCompletion) {
	if input.candidate == nil {
		return
	}
	progress := runProgressForIssue(input.candidate, input.workspace, runProgressPhaseHandoffPending, input.progressStarted)
	progress.Branch = input.branch
	progress.PRURL = input.prURL
	progress.Status = runProgressPhaseHandoffPending
	progress.NextAction = "complete_runner_handoff"
	if path, err := handoffPendingPayloadPath(input.config.WorkspaceRoot, input.candidate.Identifier); err == nil {
		progress.HandoffPayloadPath = path
	}
	if input.review != nil {
		progress.ReviewStatus = input.review.Status
		progress.ReviewClassification = input.review.Classification
	}
	writeRunProgress(input.config.WorkspaceRoot, progress)
}

func writeHandoffPendingPayload(workspaceRoot string, payload handoffPendingPayload) error {
	if strings.TrimSpace(payload.IssueIdentifier) == "" {
		return fmt.Errorf("handoff pending payload issue identifier is required")
	}
	path, err := handoffPendingPayloadPath(workspaceRoot, payload.IssueIdentifier)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readHandoffPendingPayload(workspaceRoot, issueIdentifier string) (handoffPendingPayload, error) {
	path, err := handoffPendingPayloadPath(workspaceRoot, issueIdentifier)
	if err != nil {
		return handoffPendingPayload{}, err
	}
	return readHandoffPendingPayloadFromPath(path)
}

func readHandoffPendingPayloadFromPath(path string) (handoffPendingPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return handoffPendingPayload{}, err
	}
	var payload handoffPendingPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return handoffPendingPayload{}, err
	}
	if payload.SchemaVersion != 1 {
		return handoffPendingPayload{}, fmt.Errorf("unsupported handoff pending payload schema_version %d", payload.SchemaVersion)
	}
	if payload.RuntimeUsage == nil {
		payload.RuntimeUsage = payload.PiUsage
	}
	return payload, nil
}

func boundedHandoffValidation(lines []string) []string {
	const maxLines = 20
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, truncateMarkdown(trimmed, 1000))
		if len(out) == maxLines {
			break
		}
	}
	return out
}
