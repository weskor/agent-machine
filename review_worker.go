package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

const runProgressPhaseReviewPending = "review_pending"

type reviewWorker struct {
	client          linearClient
	config          runnerConfig
	stateStore      *state.Store
	candidate       *issue
	states          []workflowState
	workspace       string
	branch          string
	progressStarted time.Time
	startedAt       time.Time
	piUsage         *usage
	prURL           string
	githubEnv       map[string]string
	githubAuth      string
	scopeResult     scopeGuardResult
	validation      []string
	resume          bool
}

type reviewWorkerResult struct {
	Review   *reviewResult
	Terminal bool
}

type reviewPendingPayload struct {
	SchemaVersion    int              `json:"schema_version"`
	IssueID          string           `json:"issue_id,omitempty"`
	IssueIdentifier  string           `json:"issue_identifier"`
	IssueTitle       string           `json:"issue_title,omitempty"`
	IssueURL         string           `json:"issue_url,omitempty"`
	IssueDescription string           `json:"issue_description,omitempty"`
	TeamID           string           `json:"team_id,omitempty"`
	Workspace        string           `json:"workspace"`
	Branch           string           `json:"branch,omitempty"`
	ProgressStarted  time.Time        `json:"progress_started"`
	StartedAt        time.Time        `json:"started_at"`
	PiUsage          *usage           `json:"pi_usage,omitempty"`
	PRURL            string           `json:"pr_url"`
	GitHubAuth       string           `json:"github_auth,omitempty"`
	ScopeResult      scopeGuardResult `json:"scope_result,omitempty"`
	Validation       []string         `json:"validation,omitempty"`
	Resume           bool             `json:"resume,omitempty"`
}

func (w reviewWorker) Execute(ctx context.Context) (reviewWorkerResult, error) {
	if w.prURL == "" || w.config.ReviewCommand == "" {
		return reviewWorkerResult{}, nil
	}
	if w.candidate == nil {
		return reviewWorkerResult{Terminal: true}, fmt.Errorf("review worker candidate is required")
	}
	if err := writeReviewPendingState(w); err != nil {
		return reviewWorkerResult{Terminal: true}, err
	}
	payload, err := readReviewPendingPayloadForExecution(w.config.WorkspaceRoot, w.candidate.Identifier)
	if err != nil {
		return reviewWorkerResult{Terminal: true}, err
	}
	return executeReviewPendingPayload(ctx, w.client, w.config, w.stateStore, payload, w.states, w.githubEnv)
}

func executeReviewPendingPayload(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, payload reviewPendingPayload, states []workflowState, githubEnv map[string]string) (reviewWorkerResult, error) {
	return executeSemanticReview(ctx, payload.Worker(client, config, stateStore, states, githubEnv))
}

func executeSemanticReview(ctx context.Context, w reviewWorker) (reviewWorkerResult, error) {
	if w.prURL == "" || w.config.ReviewCommand == "" {
		return reviewWorkerResult{}, nil
	}
	linearStatus := linearStatusWorker{client: w.client, candidate: w.candidate, states: w.states}
	readiness := newReviewReadinessModule(w.config.WorkspaceRoot)
	evidence, err := collectReviewEvidenceForWorker(w.config, w.candidate, w.workspace, w.prURL, w.scopeResult, w.validation)
	if err != nil {
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, nil, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Terminal: true}, err
	}
	if err := reviewEvidenceNotReadyError(evidence); err != nil {
		decision := readiness.NotReadyDecision(w.prURL, evidence)
		notReady := readiness.NotReadyProgress(w.candidate, w.workspace, w.branch, w.prURL, w.progressStarted, evidence)
		if w.resume {
			notReady = readiness.ResumeNotReadyProgress(w.candidate, w.workspace, w.branch, w.prURL, w.progressStarted, evidence)
		}
		writeRunProgress(w.config.WorkspaceRoot, notReady)
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, nil, w.prURL, decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Terminal: true}, nil
	}

	reviewing := runProgressForIssue(w.candidate, w.workspace, "reviewing", w.progressStarted)
	reviewing.Branch = w.branch
	reviewing.PRURL = w.prURL
	reviewing.ChecksStatus = evidence.ChecksStatus
	writeRunProgress(w.config.WorkspaceRoot, reviewing)
	review, err := runReviewForWorker(w.config.ReviewCommand, w.workspace, w.candidate, w.prURL, w.githubEnv, w.config.Budget.ReviewTimeout, &evidence)
	if err != nil {
		status := runAttemptStatusReviewFailed
		if errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			if commentErr := linearStatus.Comment(renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", w.candidate.Identifier, commentErr)
			}
		}
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, status, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Review: review, Terminal: true}, err
	}
	if exceeded := budgetExceeded(w.config.Budget, w.startedAt, w.piUsage, review.Usage); exceeded != "" {
		decision := budgetLifecycleDecision(attemptLifecyclePhaseReview, w.prURL, exceeded)
		if err := linearStatus.Comment(renderBudgetFailureComment(exceeded)); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, decision.Status, exceeded, w.config.Budget.Active(), exceeded))
		return reviewWorkerResult{Review: review, Terminal: true}, fmt.Errorf("%s", exceeded)
	}
	if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, w.prURL) {
		if _, err := linearStatus.MoveTo(w.config.ReadyState); err != nil {
			writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, runAttemptStatusReviewFailed, err.Error(), w.config.Budget.Active(), ""))
			return reviewWorkerResult{Review: review, Terminal: true}, err
		}
		comment := fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", w.config.ReadyState, w.prURL, review.Status, review.Findings)
		if err := linearStatus.Comment(comment); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, runAttemptStatusReviewFailed, "review did not pass", w.config.Budget.Active(), "")); err != nil {
			return reviewWorkerResult{Review: review, Terminal: true}, err
		}
		log("review did not pass for %s; moved back to %s", w.candidate.Identifier, w.config.ReadyState)
		return reviewWorkerResult{Review: review, Terminal: true}, nil
	}
	if reviewFailureRoutesToHumanHandoff(review, w.prURL) {
		log("review failed for %s with missing evidence only; routing to %s", w.candidate.Identifier, w.config.HandoffState)
	}
	return reviewWorkerResult{Review: review}, nil
}

func reviewPendingPayloadFromWorker(w reviewWorker) reviewPendingPayload {
	payload := reviewPendingPayload{
		SchemaVersion:   1,
		Workspace:       w.workspace,
		Branch:          w.branch,
		ProgressStarted: w.progressStarted,
		StartedAt:       w.startedAt,
		PiUsage:         w.piUsage,
		PRURL:           w.prURL,
		GitHubAuth:      w.githubAuth,
		ScopeResult:     w.scopeResult,
		Validation:      append([]string(nil), w.validation...),
		Resume:          w.resume,
	}
	if w.candidate != nil {
		payload.IssueID = w.candidate.ID
		payload.IssueIdentifier = w.candidate.Identifier
		payload.IssueTitle = w.candidate.Title
		payload.IssueURL = w.candidate.URL
		payload.IssueDescription = w.candidate.Description
		payload.TeamID = w.candidate.Team.ID
	}
	return payload
}

func (p reviewPendingPayload) Worker(client linearClient, config runnerConfig, stateStore *state.Store, states []workflowState, githubEnv map[string]string) reviewWorker {
	candidate := &issue{ID: p.IssueID, Identifier: p.IssueIdentifier, Title: p.IssueTitle, URL: p.IssueURL, Description: p.IssueDescription}
	candidate.Team.ID = p.TeamID
	return reviewWorker{
		client:          client,
		config:          config,
		stateStore:      stateStore,
		candidate:       candidate,
		states:          states,
		workspace:       p.Workspace,
		branch:          p.Branch,
		progressStarted: p.ProgressStarted,
		startedAt:       p.StartedAt,
		piUsage:         p.PiUsage,
		prURL:           p.PRURL,
		githubEnv:       githubEnv,
		githubAuth:      p.GitHubAuth,
		scopeResult:     p.ScopeResult,
		validation:      append([]string(nil), p.Validation...),
		resume:          p.Resume,
	}
}

func writeReviewPendingState(w reviewWorker) error {
	payload := reviewPendingPayloadFromWorker(w)
	if err := writeReviewPendingPayload(w.config.WorkspaceRoot, payload); err != nil {
		return err
	}
	return writeReviewPendingProgress(w)
}

func writeReviewPendingProgress(w reviewWorker) error {
	pending := runProgressForIssue(w.candidate, w.workspace, runProgressPhaseReviewPending, w.progressStarted)
	pending.Branch = w.branch
	pending.PRURL = w.prURL
	pending.Status = runProgressPhaseReviewPending
	pending.NextAction = "run_semantic_review"
	if path, err := reviewPendingPayloadPath(w.config.WorkspaceRoot, w.candidate.Identifier); err == nil {
		pending.ReviewPayloadPath = path
	}
	return writeRunProgressResult(w.config.WorkspaceRoot, pending)
}

func writeReviewPendingPayload(workspaceRoot string, payload reviewPendingPayload) error {
	if payload.SchemaVersion == 0 {
		payload.SchemaVersion = 1
	}
	if payload.IssueIdentifier == "" {
		return fmt.Errorf("review pending payload issue identifier is required")
	}
	path, err := reviewPendingPayloadPath(workspaceRoot, payload.IssueIdentifier)
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

func readReviewPendingPayload(workspaceRoot, issueIdentifier string) (reviewPendingPayload, error) {
	path, err := reviewPendingPayloadPath(workspaceRoot, issueIdentifier)
	if err != nil {
		return reviewPendingPayload{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return reviewPendingPayload{}, err
	}
	var payload reviewPendingPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return reviewPendingPayload{}, err
	}
	if payload.SchemaVersion != 1 {
		return reviewPendingPayload{}, fmt.Errorf("unsupported review pending payload schema_version %d", payload.SchemaVersion)
	}
	return payload, nil
}

var collectReviewEvidenceForWorker = collectReviewEvidence
var runReviewForWorker = runReview
var readReviewPendingPayloadForExecution = readReviewPendingPayload

func resetReviewWorkerHooks() {
	collectReviewEvidenceForWorker = collectReviewEvidence
	runReviewForWorker = runReview
	readReviewPendingPayloadForExecution = readReviewPendingPayload
}
