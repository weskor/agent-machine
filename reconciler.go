package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

type lifecycleState string

const (
	lifecycleReady         lifecycleState = "Ready"
	lifecycleRunning       lifecycleState = "Running"
	lifecycleNeedsInfo     lifecycleState = "Needs Info"
	lifecycleReviewFailed  lifecycleState = "Review Failed"
	lifecycleHandoffReady  lifecycleState = "Handoff Ready"
	lifecycleFeedbackRetry lifecycleState = "Feedback Retry"
	lifecycleMergeReady    lifecycleState = "Merge Ready"
	lifecycleDone          lifecycleState = "Done"
	lifecycleBlocked       lifecycleState = "Blocked"
	lifecycleQuarantined   lifecycleState = "Quarantined"
)

type reconciliationDecision struct {
	IssueIdentifier      string
	StateName            string
	Lifecycle            lifecycleState
	CanRun               bool
	CanMerge             bool
	ShouldRetry          bool
	ShouldQuarantine     bool
	NextAction           string
	Blockers             []string
	RunRecord            *runRecord
	PR                   *pullRequestSummary
	Artifact             *artifactSummary
	DBFacts              *state.ReconciliationFacts
	StateStoreError      error
	ReconciliationNeeded bool
}

type reconciliationStateReader interface {
	ReconciliationFacts(context.Context, string) (state.ReconciliationFacts, bool, error)
	Lease(context.Context, string) (state.Lease, bool, error)
}

type reconciliationModule struct {
	reader reconciliationStateReader
	now    func() time.Time
}

func newReconciliationModule(reader reconciliationStateReader) reconciliationModule {
	if reader == nil || reflect.ValueOf(reader).Kind() == reflect.Ptr && reflect.ValueOf(reader).IsNil() {
		return reconciliationModule{now: func() time.Time { return time.Now().UTC() }}
	}
	return reconciliationModule{reader: reader, now: func() time.Time { return time.Now().UTC() }}
}

func reconcileIssue(config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return newReconciliationModule(nil).ReconcileIssue(config, candidate, pr)
}

func reconcileIssueWithArtifact(config runnerConfig, candidate issue, pr *pullRequestSummary, artifact *artifactSummary) reconciliationDecision {
	return newReconciliationModule(nil).ReconcileIssueWithArtifact(config, candidate, pr, artifact)
}

func (m reconciliationModule) ReconcileIssue(config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return m.ReconcileIssueWithArtifact(config, candidate, pr, nil)
}

func (m reconciliationModule) ReconcileIssueWithArtifact(config runnerConfig, candidate issue, pr *pullRequestSummary, artifact *artifactSummary) reconciliationDecision {
	workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
	decision := reconciliationDecision{IssueIdentifier: candidate.Identifier, StateName: candidate.State.Name, Lifecycle: lifecycleReady, NextAction: "run_agent"}
	if pr != nil {
		copy := *pr
		decision.PR = &copy
	}
	if artifact != nil {
		copy := *artifact
		decision.Artifact = &copy
		if copy.HasArtifact && terminalRunStatus(copy.Status) {
			decision.RunRecord = &runRecord{Status: copy.Status, PRURL: copy.PRURL, ReviewStatus: copy.Review}
		}
	}
	if facts, ok, err := m.reconciliationFacts(context.Background(), candidate.Identifier); err != nil {
		decision.StateStoreError = err
		decision.ReconciliationNeeded = true
	} else if ok {
		decision.DBFacts = &facts
		if facts.Status != "" && terminalRunStatus(facts.Status) && decision.RunRecord == nil {
			decision.RunRecord = &runRecord{Status: facts.Status, PRURL: facts.PRURL}
		}
		if facts.PRURL != "" && pr == nil {
			decision.ReconciliationNeeded = true
		}
		if artifact != nil && artifact.HasArtifact && facts.Status != "" && artifact.Status != "" && artifact.Status != facts.Status {
			decision.ReconciliationNeeded = true
		}
	}
	if record, ok := readRunArtifact(workspace); ok {
		if decision.DBFacts == nil || decision.DBFacts.Status == "" {
			decision.RunRecord = &record
		} else if record.Status != decision.DBFacts.Status || record.PRURL != decision.DBFacts.PRURL {
			decision.ReconciliationNeeded = true
		}
	}
	if isBlockedCandidate(candidate) {
		decision.block(lifecycleBlocked, "issue has blocked label", "operator_unblock_issue")
		return decision
	}
	if hasRunLock(workspace) {
		decision.block(lifecycleRunning, "active run lock exists", "wait_for_or_clear_run_lock")
		return decision
	}
	if decision.StateStoreError != nil {
		decision.block(lifecycleBlocked, fmt.Sprintf("SQLite reconciliation state unavailable: %v", decision.StateStoreError), "repair_sqlite_state_store")
		return decision
	}
	if active, err := m.activeRunLease(context.Background(), candidate.Identifier); err != nil {
		decision.StateStoreError = err
		decision.ReconciliationNeeded = true
		decision.block(lifecycleBlocked, fmt.Sprintf("SQLite run lease unavailable: %v", err), "repair_sqlite_state_store")
		return decision
	} else if active {
		decision.block(lifecycleRunning, "active SQLite run lease exists", "wait_for_or_release_run_lease")
		return decision
	}
	if candidate.State.Name == config.NeedsInfoState {
		decision.block(lifecycleNeedsInfo, "Linear issue needs more information", "answer_questions_then_move_ready")
		return decision
	}
	if candidate.State.Name == config.DoneState {
		decision.block(lifecycleDone, "Linear issue is done", "cleanup_workspace")
		return decision
	}
	if pr != nil {
		if decision.DBFacts != nil && decision.DBFacts.PRURL != "" && decision.DBFacts.PRURL != pr.URL {
			decision.ReconciliationNeeded = true
		}
		decision.applyPRInvariants(config, candidate, workspace, *pr)
		return decision
	}
	if decision.DBFacts != nil && decision.DBFacts.PRURL != "" && pr == nil {
		decision.block(lifecycleBlocked, "SQLite PR mapping has no current open PR", "reconcile_missing_or_closed_pr_mapping")
		return decision
	}
	if hasUnresolvedReviewFailure(config.WorkspaceRoot, candidate.Identifier) {
		decision.block(lifecycleReviewFailed, "prior review-failure findings are unresolved", "repair_review_findings_before_retry")
		return decision
	}
	if decision.RunRecord != nil && terminalRunStatus(decision.RunRecord.Status) && pr == nil {
		decision.applyTerminalArtifact(config, candidate, workspace)
	}
	if len(decision.Blockers) == 0 && stateIsRunnable(candidate.State.Name, config) {
		decision.CanRun = true
	}
	return decision
}

func (m reconciliationModule) reconciliationFacts(ctx context.Context, issueKey string) (state.ReconciliationFacts, bool, error) {
	if m.reader == nil {
		return state.ReconciliationFacts{}, false, nil
	}
	return m.reader.ReconciliationFacts(ctx, issueKey)
}

func (m reconciliationModule) activeRunLease(ctx context.Context, issueKey string) (bool, error) {
	if m.reader == nil {
		return false, nil
	}
	lease, ok, err := m.reader.Lease(ctx, "run:"+issueKey)
	if err != nil || !ok {
		return false, err
	}
	now := time.Now().UTC()
	if m.now != nil {
		now = m.now()
	}
	return lease.ReleasedAt.IsZero() && lease.ExpiresAt.After(now), nil
}

func reconcileIssues(config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, artifactsByIssue map[string]artifactSummary) []reconciliationDecision {
	return newReconciliationModule(nil).ReconcileIssues(config, issues, prsByIssue, artifactsByIssue)
}

func (m reconciliationModule) ReconcileIssues(config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, artifactsByIssue map[string]artifactSummary) []reconciliationDecision {
	decisions := make([]reconciliationDecision, 0, len(issues))
	for _, issue := range issues {
		var artifact *artifactSummary
		if artifactsByIssue != nil {
			if summary, ok := artifactsByIssue[issue.Identifier]; ok {
				artifact = &summary
			}
		}
		var pr *pullRequestSummary
		if prsByIssue != nil {
			pr = prsByIssue[issue.Identifier]
		}
		decisions = append(decisions, m.ReconcileIssueWithArtifact(config, issue, pr, artifact))
	}
	return decisions
}

func stateIsRunnable(state string, config runnerConfig) bool {
	if state == "" || state == config.ReadyState || state == config.RunningState {
		return true
	}
	for _, active := range config.ActiveStates {
		if state == active {
			return true
		}
	}
	return false
}

func (d *reconciliationDecision) applyTerminalArtifact(config runnerConfig, candidate issue, workspace string) {
	record := d.RunRecord
	if record.Status == "success" && record.PRURL != "" {
		if feedbackRetryAvailable(workspace, &candidate, *record, config) {
			d.Lifecycle = lifecycleFeedbackRetry
			d.CanRun = true
			d.ShouldRetry = true
			d.NextAction = "retry_with_unresolved_pr_feedback"
			return
		}
		d.block(lifecycleHandoffReady, "terminal success artifact already has a PR", "await_review_or_reconcile_artifact")
		return
	}
	if record.Status == "needs_info" || record.Status == "needs_info_failed" {
		d.block(lifecycleNeedsInfo, "terminal needs-info artifact exists", "answer_questions_then_retry")
		return
	}
	if record.Status == "review_failed" {
		d.block(lifecycleReviewFailed, "terminal review_failed artifact exists", "repair_review_findings_before_handoff")
		return
	}
	if record.Status == "merged" || record.Status == "superseded" {
		d.block(lifecycleDone, fmt.Sprintf("terminal %s artifact exists", record.Status), "cleanup_workspace")
		return
	}
	d.block(lifecycleBlocked, fmt.Sprintf("terminal %s artifact exists", emptyAsUnknown(record.Status)), "operator_repair_or_clear_artifact")
}

func (d *reconciliationDecision) applyPRInvariants(config runnerConfig, candidate issue, workspace string, pr pullRequestSummary) {
	if reason := prInvariantBlockReason(config, candidate, pr); reason != "" {
		d.block(lifecycleQuarantined, reason, "close_or_repair_invalid_pr")
		d.ShouldQuarantine = true
		return
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		d.Lifecycle = lifecycleFeedbackRetry
		d.CanRun = stateIsRunnable(candidate.State.Name, config)
		d.CanMerge = false
		d.Blockers = append(d.Blockers, "GitHub review requested changes")
		d.NextAction = "capture_feedback_and_retry"
		d.ShouldRetry = true
		return
	}
	if canRetryReviewReadiness(config, candidate, pr, d.RunRecord) {
		d.Lifecycle = lifecycleRunning
		d.CanRun = true
		d.CanMerge = false
		d.NextAction = "run_semantic_review_after_checks_ready"
		d.ShouldRetry = true
		return
	}
	if candidate.State.Name != config.HandoffState {
		d.block(lifecycleBlocked, fmt.Sprintf("PR exists while Linear state is %q", candidate.State.Name), "reconcile_linear_state")
		return
	}
	if pr.ReviewDecision != "APPROVED" {
		d.block(lifecycleHandoffReady, fmt.Sprintf("waiting for approval; reviewDecision=%s", emptyAsUnknown(pr.ReviewDecision)), "await_human_approval")
		return
	}
	if reason := mergeGateBlockReason(pr); reason != "" {
		d.block(lifecycleHandoffReady, reason, "wait_for_green_mergeable_pr")
		return
	}
	if reason := runArtifactMergeBlockReason(config.WorkspaceRoot, candidate.Identifier, pr.URL); reason != "" {
		d.block(lifecycleQuarantined, reason, "repair_run_artifact_before_merge")
		d.ShouldQuarantine = true
		return
	}
	if locked, reason := workspaceLockedOrModified(config.WorkspaceRoot, candidate.Identifier, pr.HeadRefName); locked {
		d.block(lifecycleBlocked, reason, "clear_workspace_lock_or_changes")
		return
	}
	d.Lifecycle = lifecycleMergeReady
	d.CanMerge = true
	d.NextAction = "merge_approved_pr"
}

func (d *reconciliationDecision) block(state lifecycleState, reason, next string) {
	d.Lifecycle = state
	d.CanRun = false
	d.CanMerge = false
	d.Blockers = append(d.Blockers, reason)
	d.NextAction = next
}

func canRetryReviewReadiness(config runnerConfig, candidate issue, pr pullRequestSummary, record *runRecord) bool {
	if !stateIsRunnable(candidate.State.Name, config) {
		return false
	}
	snapshot, err := readRunProgress(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return false
	}
	if snapshot.Phase != "review_not_ready" {
		return false
	}
	if strings.TrimSpace(snapshot.PRURL) != "" && snapshot.PRURL != pr.URL {
		return false
	}
	if record != nil && record.Status != runAttemptStatusReviewNotReady {
		return false
	}
	status, _ := reviewChecksStatus(pr.StatusCheckRollup)
	return status == "success"
}

func prInvariantBlockReason(config runnerConfig, candidate issue, pr pullRequestSummary) string {
	var reasons []string
	baseBranch := strings.TrimSpace(config.BaseBranch)
	if baseBranch == "" {
		baseBranch = "develop"
	}
	if pr.BaseRefName != "" && !strings.EqualFold(pr.BaseRefName, baseBranch) {
		reasons = append(reasons, fmt.Sprintf("PR base branch is %q; expected %q", pr.BaseRefName, baseBranch))
	}
	if pr.HeadRefName != expectedWorkspaceBranch(candidate.Identifier) {
		reasons = append(reasons, fmt.Sprintf("PR head branch is %q; expected %q", emptyAsUnknown(pr.HeadRefName), expectedWorkspaceBranch(candidate.Identifier)))
	}
	ownership := newGitHubOwnershipPolicy(config)
	if !ownership.AllowsPRAuthor(pr.AuthorLogin()) {
		reasons = append(reasons, fmt.Sprintf("PR author is %q; expected %s", emptyAsUnknown(pr.AuthorLogin()), ownership.ExpectedPRAuthorSource()))
	}
	if reason := commitAuthorInvariantBlockReason(pr); reason != "" {
		reasons = append(reasons, reason)
	}
	return strings.Join(reasons, "; ")
}
