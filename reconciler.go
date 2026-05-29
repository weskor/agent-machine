package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/codeownership"
	"github.com/weskor/agent-machine/internal/state"
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

var candidatesForReconciliationWorker = func(client linearClient, config runnerConfig) ([]issue, error) {
	return client.candidates(config.ProjectSlug, statusIssueStates(config))
}
var openPRsForReconciliationWorker = openAgentMachinePRs
var artifactSummariesForReconciliationWorker = workspaceArtifactSummaries

func newReconciliationModule(reader reconciliationStateReader) reconciliationModule {
	if reader == nil || reflect.ValueOf(reader).Kind() == reflect.Ptr && reflect.ValueOf(reader).IsNil() {
		return reconciliationModule{now: func() time.Time { return time.Now().UTC() }}
	}
	return reconciliationModule{reader: reader, now: func() time.Time { return time.Now().UTC() }}
}

func reconcileIssue(config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return reconcileIssueContext(context.Background(), config, candidate, pr)
}

func reconcileIssueContext(ctx context.Context, config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return newReconciliationModule(nil).ReconcileIssueContext(ctx, config, candidate, pr)
}

func (m reconciliationModule) ReconcileIssue(config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return m.ReconcileIssueContext(context.Background(), config, candidate, pr)
}

func (m reconciliationModule) ReconcileIssueContext(ctx context.Context, config runnerConfig, candidate issue, pr *pullRequestSummary) reconciliationDecision {
	return m.ReconcileIssueWithArtifactContext(ctx, config, candidate, pr, nil)
}

func (m reconciliationModule) ReconcileIssueWithArtifact(config runnerConfig, candidate issue, pr *pullRequestSummary, artifact *artifactSummary) reconciliationDecision {
	return m.ReconcileIssueWithArtifactContext(context.Background(), config, candidate, pr, artifact)
}

func (m reconciliationModule) ReconcileIssueWithArtifactContext(ctx context.Context, config runnerConfig, candidate issue, pr *pullRequestSummary, artifact *artifactSummary) reconciliationDecision {
	workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
	decision := reconciliationDecision{IssueIdentifier: candidate.Identifier, StateName: candidate.State.Name, Lifecycle: lifecycleReady, NextAction: "run_agent"}
	if err := ctx.Err(); err != nil {
		decision.StateStoreError = err
		decision.ReconciliationNeeded = true
		decision.block(lifecycleBlocked, fmt.Sprintf("reconciliation context canceled: %v", err), "retry_when_worker_context_active")
		return decision
	}
	stateBacked := m.reader != nil
	artifactOnlyEvidence := false
	artifactConflict := false
	missingStateBackedPRFacts := false
	if pr != nil {
		copy := *pr
		decision.PR = &copy
	}
	if artifact != nil {
		copy := *artifact
		decision.Artifact = &copy
		if !stateBacked && copy.HasArtifact && terminalRunStatus(copy.Status) {
			decision.RunRecord = &runRecord{Status: copy.Status, PRURL: copy.PRURL, ReviewStatus: copy.Review}
		}
	}
	if facts, ok, err := m.reconciliationFacts(ctx, candidate.Identifier); err != nil {
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
	} else if stateBacked && pr != nil {
		missingStateBackedPRFacts = true
		decision.ReconciliationNeeded = true
	}
	if stateBacked && artifact != nil && artifact.HasArtifact {
		if decision.DBFacts == nil || decision.DBFacts.Status == "" {
			artifactOnlyEvidence = true
			decision.ReconciliationNeeded = true
		} else if artifact.Status != "" && artifact.Status != decision.DBFacts.Status {
			artifactConflict = true
			decision.ReconciliationNeeded = true
		}
	}
	if !stateBacked {
		if record, ok := readRunArtifact(workspace); ok {
			decision.RunRecord = &record
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
	if missingStateBackedPRFacts {
		decision.block(lifecycleBlocked, "SQLite has no attempt state for current PR", "repair_sqlite_state_store")
		return decision
	}
	if active, err := m.activeRunLease(ctx, candidate.Identifier); err != nil {
		decision.StateStoreError = err
		decision.ReconciliationNeeded = true
		decision.block(lifecycleBlocked, fmt.Sprintf("SQLite run lease unavailable: %v", err), "repair_sqlite_state_store")
		return decision
	} else if active {
		decision.block(lifecycleRunning, "active SQLite run lease exists", "wait_for_or_release_run_lease")
		return decision
	}
	if artifactOnlyEvidence {
		decision.block(lifecycleBlocked, "artifact exists without SQLite attempt state", "repair_sqlite_state_store")
		return decision
	}
	if artifactConflict {
		decision.block(lifecycleBlocked, "artifact state conflicts with SQLite attempt state", "repair_artifact_or_sqlite_state")
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
		if decision.DBFacts != nil && strings.TrimSpace(decision.DBFacts.Status) == "" {
			decision.ReconciliationNeeded = true
			decision.block(lifecycleBlocked, "SQLite attempt status is missing for current PR", "repair_sqlite_state_store")
			return decision
		}
		decision.applyPRInvariants(config, candidate, workspace, *pr)
		return decision
	}
	if decision.DBFacts != nil && decision.DBFacts.PRURL != "" && pr == nil {
		decision.block(lifecycleBlocked, "SQLite PR mapping has no current open PR", "reconcile_missing_or_closed_pr_mapping")
		return decision
	}
	if stateBacked && decision.DBFacts != nil && decision.DBFacts.Status == runAttemptStatusReviewFailed {
		decision.block(lifecycleReviewFailed, "SQLite review-failed attempt is unresolved", "repair_review_findings_before_retry")
		return decision
	}
	if !stateBacked && hasUnresolvedReviewFailure(config.WorkspaceRoot, candidate.Identifier) {
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

func runReconciliationScan(client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	return runReconciliationScanContext(context.Background(), client, config, store)
}

func runReconciliationScanContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for reconciliation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	issues, err := candidatesForReconciliationWorker(client, config)
	if err != nil {
		return false, err
	}
	prs, err := openPRsForReconciliationWorker(config)
	if err != nil {
		return false, err
	}
	artifacts, err := artifactSummariesForReconciliationWorker(config.WorkspaceRoot)
	if err != nil {
		return false, err
	}
	prsByIssue := indexPRsByIssue(prs)
	artifactIndex := indexArtifacts(artifacts)
	decisions := newReconciliationModule(store).ReconcileIssuesContext(ctx, config, issues, prsByIssue, artifactIndex.byIssue)
	decisions = repairableReviewFailedReconciliationDecisions(config, issues, prsByIssue, decisions)
	issueIDs := map[string]string{}
	for _, issue := range issues {
		issueIDs[issue.Identifier] = issue.ID
	}
	recorded := 0
	for _, decision := range decisions {
		if !decision.ReconciliationNeeded && !decision.ShouldQuarantine {
			continue
		}
		if err := recordReconciliationNeededEventContext(ctx, store, decision, issueIDs[decision.IssueIdentifier]); err != nil {
			return true, err
		}
		recorded++
	}
	log("reconciliation scan completed: issues=%d findings=%d", len(issues), recorded)
	return true, nil
}

func recordReconciliationNeededEventContext(ctx context.Context, store *state.Store, decision reconciliationDecision, issueID string) error {
	if store == nil {
		return nil
	}
	attempt := 0
	if decision.DBFacts != nil {
		attempt = decision.DBFacts.Attempt
	}
	payload := map[string]any{
		"state":                 decision.StateName,
		"lifecycle":             string(decision.Lifecycle),
		"next_action":           decision.NextAction,
		"blockers":              append([]string(nil), decision.Blockers...),
		"reconciliation_needed": decision.ReconciliationNeeded,
		"should_quarantine":     decision.ShouldQuarantine,
	}
	if decision.DBFacts != nil {
		payload["sqlite_status"] = decision.DBFacts.Status
		payload["sqlite_pr_url"] = decision.DBFacts.PRURL
		payload["sqlite_terminal_outcome"] = decision.DBFacts.TerminalOutcome
	}
	if decision.PR != nil {
		payload["pr_url"] = decision.PR.URL
		payload["pr_number"] = decision.PR.Number
		payload["pr_head_ref"] = decision.PR.HeadRefName
	}
	if decision.Artifact != nil {
		payload["artifact_status"] = decision.Artifact.Status
		payload["artifact_pr_url"] = decision.Artifact.PRURL
		payload["artifact_outcome"] = decision.Artifact.Outcome
	}
	_, err := store.AppendEvent(ctx, state.EventInput{
		OccurredAt: time.Now().UTC(),
		IssueKey:   decision.IssueIdentifier,
		IssueID:    issueID,
		Attempt:    attempt,
		Source:     "reconciliation",
		Type:       state.EventReconciliationNeeded,
		Payload:    payload,
	})
	return err
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
	return reconcileIssuesContext(context.Background(), config, issues, prsByIssue, artifactsByIssue)
}

func reconcileIssuesContext(ctx context.Context, config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, artifactsByIssue map[string]artifactSummary) []reconciliationDecision {
	return newReconciliationModule(nil).ReconcileIssuesContext(ctx, config, issues, prsByIssue, artifactsByIssue)
}

func (m reconciliationModule) ReconcileIssues(config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, artifactsByIssue map[string]artifactSummary) []reconciliationDecision {
	return m.ReconcileIssuesContext(context.Background(), config, issues, prsByIssue, artifactsByIssue)
}

func (m reconciliationModule) ReconcileIssuesContext(ctx context.Context, config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, artifactsByIssue map[string]artifactSummary) []reconciliationDecision {
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
		decisions = append(decisions, m.ReconcileIssueWithArtifactContext(ctx, config, issue, pr, artifact))
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
	if canRetryReviewReadiness(config, candidate, pr, d.DBFacts) {
		d.Lifecycle = lifecycleRunning
		d.CanRun = true
		d.CanMerge = false
		d.NextAction = "run_semantic_review_after_checks_ready"
		d.ShouldRetry = true
		return
	}
	if d.RunRecord != nil && d.RunRecord.Status == runAttemptStatusSuccess && feedbackRetryAvailable(workspace, &candidate, *d.RunRecord, config, &pr) {
		d.Lifecycle = lifecycleFeedbackRetry
		d.CanRun = true
		d.CanMerge = false
		d.NextAction = "retry_with_unresolved_pr_feedback"
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
	if reason := d.mergeStateBlockReason(config, candidate, pr); reason != "" {
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

func (d reconciliationDecision) mergeStateBlockReason(config runnerConfig, candidate issue, pr pullRequestSummary) string {
	if d.DBFacts != nil {
		return sqliteMergeBlockReason(*d.DBFacts, pr.URL)
	}
	return runArtifactMergeBlockReason(config.WorkspaceRoot, candidate.Identifier, pr.URL)
}

func sqliteMergeBlockReason(facts state.ReconciliationFacts, prURL string) string {
	if strings.TrimSpace(facts.PRURL) != "" && strings.TrimSpace(prURL) != "" && strings.TrimSpace(facts.PRURL) != strings.TrimSpace(prURL) {
		return fmt.Sprintf("SQLite PR URL %s does not match candidate PR %s", facts.PRURL, prURL)
	}
	if facts.Status != runAttemptStatusSuccess {
		return fmt.Sprintf("run status is %s", emptyAsUnknown(facts.Status))
	}
	if facts.ReviewStatus == "passed" {
		return ""
	}
	if facts.ReviewStatus == "failed" && facts.ReviewClassification == reviewClassificationMissingEvidenceOnly {
		return ""
	}
	return fmt.Sprintf("review status is %s", emptyAsUnknown(facts.ReviewStatus))
}

func (d *reconciliationDecision) block(state lifecycleState, reason, next string) {
	d.Lifecycle = state
	d.CanRun = false
	d.CanMerge = false
	d.Blockers = append(d.Blockers, reason)
	d.NextAction = next
}

func canRetryReviewReadiness(config runnerConfig, candidate issue, pr pullRequestSummary, facts *state.ReconciliationFacts) bool {
	if !stateIsRunnable(candidate.State.Name, config) {
		return false
	}
	status, _ := reviewChecksStatus(pr.StatusCheckRollup)
	if status != "success" {
		return false
	}
	if facts == nil || facts.Status == "" {
		return false
	}
	return facts.Status == runAttemptStatusReviewNotReady && (strings.TrimSpace(facts.PRURL) == "" || facts.PRURL == pr.URL)
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
	appIdentity := githubAppIdentityFromConfig(config)
	ownership := codeownership.NewPolicy(codeownership.Input{
		Provider:                  codeHostProvider(config),
		GitHubPRAuthorOverride:    config.GitHubPRAuthorOverride,
		GitHubAppExpectedLogins:   appIdentity.ExpectedPRAuthorLogins(),
		GitHubAppSource:           appIdentity.Source,
		GitLabPRAuthorOverride:    config.GitLabPRAuthorOverride,
		GitLabEnvPRAuthorOverride: os.Getenv("GITLAB_PR_AUTHOR_OVERRIDE"),
	})
	if !ownership.AllowsPRAuthor(pr.AuthorLogin()) {
		reasons = append(reasons, fmt.Sprintf("PR author is %q; expected %s", emptyAsUnknown(pr.AuthorLogin()), ownership.ExpectedPRAuthorSource()))
	}
	if codeHostProvider(config) != "gitlab" {
		if reason := commitAuthorInvariantBlockReason(config, pr); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return strings.Join(reasons, "; ")
}
