package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

const runProgressPhasePRHandoffPending = "pr_handoff_pending"

type prHandoffPendingPayload struct {
	SchemaVersion    int              `json:"schema_version"`
	IssueID          string           `json:"issue_id,omitempty"`
	IssueIdentifier  string           `json:"issue_identifier"`
	IssueTitle       string           `json:"issue_title,omitempty"`
	IssueURL         string           `json:"issue_url,omitempty"`
	IssueDescription string           `json:"issue_description,omitempty"`
	TeamID           string           `json:"team_id,omitempty"`
	Workspace        string           `json:"workspace"`
	Branch           string           `json:"branch,omitempty"`
	AgentPRURL       string           `json:"agent_pr_url,omitempty"`
	StartedAt        time.Time        `json:"started_at"`
	AttemptStartedAt time.Time        `json:"attempt_started_at,omitempty"`
	ProgressStarted  time.Time        `json:"progress_started_at,omitempty"`
	RuntimeUsage     *usage           `json:"runtime_usage,omitempty"`
	PiUsage          *usage           `json:"pi_usage,omitempty"`
	ScopeResult      scopeGuardResult `json:"scope_result,omitempty"`
	Validation       []string         `json:"validation,omitempty"`
	GitHubAuth       string           `json:"github_auth,omitempty"`
}

func hasUnresolvedReviewFailure(workspaceRoot, identifier string) bool {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, identifier, ".pi-symphony-run.json"))
	if err != nil {
		return false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false
	}
	return record.Status == "review_failed" && record.PRURL != ""
}

type needsInfoResult struct {
	NeedsInfo bool
	Questions []string
}

func parseNeedsInfo(output string) needsInfoResult {
	text := assistantText(output)
	if text == "" {
		text = output
	}
	lines := strings.Split(text, "\n")
	found := false
	var questions []string
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if isNeedsInfoMarker(clean) {
			found = true
			continue
		}
		if !found || clean == "" {
			continue
		}
		trimmed := strings.TrimLeft(clean, "-• \t")
		if isNumberedQuestion(trimmed) {
			questions = append(questions, sanitizeMarkdownLine(trimmed))
		}
	}
	return needsInfoResult{NeedsInfo: found, Questions: questions}
}

func isNeedsInfoMarker(line string) bool {
	return line == "NEEDS_INFO" || strings.HasPrefix(line, "NEEDS_INFO:")
}

func isNumberedQuestion(line string) bool {
	dot := strings.Index(line, ".")
	paren := strings.Index(line, ")")
	idx := dot
	if idx == -1 || (paren != -1 && paren < idx) {
		idx = paren
	}
	if idx <= 0 || idx > 3 || idx >= len(line)-1 {
		return false
	}
	for _, r := range line[:idx] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.TrimSpace(line[idx+1:]) != ""
}

func renderNeedsInfoComment(questions []string) string {
	var builder strings.Builder
	builder.WriteString("Runtime run stopped because the ticket needs additional information. Please answer the questions below, then move the issue back to Ready for Agent.\n\n")
	if len(questions) == 0 {
		builder.WriteString("1. Please clarify the missing requirements so the agent can proceed safely.")
		return builder.String()
	}
	for i, question := range questions {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, strings.TrimSpace(stripQuestionNumber(question)))
	}
	return truncateMarkdown(strings.TrimSpace(builder.String()), 2000)
}

func stripQuestionNumber(question string) string {
	trimmed := strings.TrimSpace(question)
	for i, r := range trimmed {
		if (r == '.' || r == ')') && i > 0 {
			return strings.TrimSpace(trimmed[i+1:])
		}
	}
	return trimmed
}

func behaviorContractPreflightPrompt() string {
	return `Behavior-contract preflight for refactors, replacements, and rewrites:
- Read the relevant agent docs before planning broad runner work: CONTEXT.md for domain language, LANGUAGE.md for architecture vocabulary, docs/adr/ for durable decisions, docs/specs/ for observable contracts, and docs/agents/review-policy.md for evidence expectations.
- Before changing code, commands, dependencies, integrations, workflows, or state-machine logic, inventory the existing observable contract: inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Add a Behavior Contract Evidence section to the PR body or tracker handoff: cite relevant specs/ADRs, list behavior preserved, behavior intentionally changed with justification, and unknown behavior that needs clarification.
- Update docs/specs/ when observable behavior intentionally changes; for mechanical refactors, state that no spec changes were needed.
- Use TDD or characterization tests for old observable behavior before proving the new abstraction; tests only around the new design are not enough.
- State a complexity/LOC budget before implementation: expected files touched, expected LOC direction, why any net growth is acceptable, what bespoke code is removed, and when the work must split.
- If the existing contract cannot be determined safely, output NEEDS_INFO instead of guessing.`
}

func behaviorContractEvidenceForRun(record runRecord) []string {
	evidence := []string{"implementation_prompt_required_behavior_contract_preflight"}
	if record.ReviewStatus != "" {
		evidence = append(evidence, "review_prompt_required_behavior_contract_parity_check")
	}
	if record.ReviewStatus == "passed" {
		evidence = append(evidence, "review_passed_behavior_contract_gate")
	}
	if record.ReviewStatus == "failed" {
		evidence = append(evidence, "review_failed_behavior_contract_or_scope_gate")
	}
	if record.ReviewClassification != "" {
		evidence = append(evidence, "review_classification_"+record.ReviewClassification)
	}
	if strings.HasPrefix(record.Status, "needs_info") {
		evidence = append(evidence, "needs_info_used_for_unknown_behavior_contract")
	}
	if strings.TrimSpace(record.Error) != "" || strings.TrimSpace(record.ReviewFindings) != "" {
		evidence = append(evidence, "findings_recorded_for_behavior_contract_audit")
	}
	return evidence
}

func expectedWorkspaceBranch(identifier string) string {
	return "symphony/" + strings.TrimSpace(identifier) + "-workspace"
}

func validatePRForHandoff(config runnerConfig, candidate *issue, prURL string) (string, string, error) {
	if prURL != "" {
		owner, repo, ok := parseGitHubPRRepository(prURL)
		if !ok {
			return prURL, "", fmt.Errorf("invalid GitHub PR URL %q", prURL)
		}
		expectedOwner, expectedRepo, err := currentGitHubRepo()
		if err != nil {
			return prURL, "", err
		}
		if !strings.EqualFold(owner, expectedOwner) || !strings.EqualFold(repo, expectedRepo) {
			return prURL, "", fmt.Errorf("PR repository is %s/%s; expected %s/%s", owner, repo, expectedOwner, expectedRepo)
		}
	}
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.GitHubTimeout)
	if err != nil {
		return prURL, "", err
	}
	defer cancel()

	details, err := github.PullRequestHandoffDetails(ctx, prURL)
	if err != nil {
		if !isRecoverablePRLookupError(err) {
			return prURL, "", fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, err)
		}

		fallback, fallbackErr := resolveHandoffPRByBranch(ctx, github, candidate)
		if fallbackErr != nil {
			return prURL, "", fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, fallbackErr)
		}
		details = fallback
	}

	if details.URL == "" {
		details.URL = prURL
	}
	return details.URL, prHandoffBlockReason(config, candidate, details), nil
}

func ensureRunnerPRHandoff(config runnerConfig, candidate *issue, workspace, agentPRURL string, githubEnv map[string]string) (string, error) {
	return ensureRunnerPRHandoffFromInputContext(context.Background(), config, prHandoffInput{Candidate: candidate, Workspace: workspace, AgentPRURL: agentPRURL}, githubEnv)
}

type prHandoffInput struct {
	Candidate        *issue
	Workspace        string
	AgentPRURL       string
	ProgressStarted  time.Time
	AttemptStartedAt time.Time
	RuntimeUsage     *usage
	ScopeResult      scopeGuardResult
	Validation       []string
	GitHubAuth       string
	StateStore       *state.Store
}

func ensureRunnerPRHandoffFromInput(config runnerConfig, input prHandoffInput, githubEnv map[string]string) (string, error) {
	return ensureRunnerPRHandoffFromInputContext(context.Background(), config, input, githubEnv)
}

func ensureRunnerPRHandoffFromInputContext(ctx context.Context, config runnerConfig, input prHandoffInput, githubEnv map[string]string) (string, error) {
	if err := writePRHandoffPendingStateContext(ctx, config, input); err != nil {
		return "", err
	}
	payload, err := readPRHandoffPendingPayloadForExecution(config.WorkspaceRoot, input.Candidate.Identifier)
	if err != nil {
		return "", err
	}
	prURL, err := executePRHandoffPendingPayloadContext(ctx, config, payload, githubEnv)
	if completeErr := completePRHandoffIntent(ctx, input.StateStore, payload, prURL, err); completeErr != nil {
		return prURL, errors.Join(err, completeErr)
	}
	return prURL, err
}

func executePRHandoffPendingPayload(config runnerConfig, payload prHandoffPendingPayload, githubEnv map[string]string) (string, error) {
	return executePRHandoffPendingPayloadContext(context.Background(), config, payload, githubEnv)
}

func executePRHandoffPendingPayloadContext(ctx context.Context, config runnerConfig, payload prHandoffPendingPayload, githubEnv map[string]string) (string, error) {
	candidate := payload.Issue()
	return executeRunnerPRHandoffContext(ctx, config, candidate, payload.Workspace, payload.AgentPRURL, githubEnv)
}

func executeRunnerPRHandoff(config runnerConfig, candidate *issue, workspace, agentPRURL string, githubEnv map[string]string) (string, error) {
	return executeRunnerPRHandoffContext(context.Background(), config, candidate, workspace, agentPRURL, githubEnv)
}

func executeRunnerPRHandoffContext(ctx context.Context, config runnerConfig, candidate *issue, workspace, agentPRURL string, githubEnv map[string]string) (string, error) {
	branch := expectedWorkspaceBranch(candidate.Identifier)
	current, err := currentGitBranchContext(ctx, workspace)
	if err != nil {
		return "", err
	}
	if current != branch {
		return "", fmt.Errorf("workspace branch is %q; expected %q", emptyAsUnknown(current), branch)
	}
	base := strings.TrimSpace(config.BaseBranch)
	if base == "" {
		base = "main"
	}
	worktreePathspec := "-- . ':!.pi-symphony-*' ':!.pi-symphony/**'"
	status, err := sh.CaptureQuietContext(ctx, "git status --porcelain "+worktreePathspec, workspace)
	if err != nil {
		return "", fmt.Errorf("git status failed before PR handoff: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		if err := sh.RunWithContextTimeout(ctx, "git add -A "+worktreePathspec+" && git commit -m "+sh.Quote(candidate.Identifier+": runner handoff"), workspace, config.Budget.CommandTimeout); err != nil {
			return "", fmt.Errorf("runner commit failed: %w", err)
		}
	}
	if err := sh.RunWithContextTimeout(ctx, "git diff --quiet "+sh.Quote("origin/"+base+"...HEAD"), workspace, config.Budget.CommandTimeout); err == nil {
		return "", fmt.Errorf("no branch changes to hand off for %s", candidate.Identifier)
	}
	if _, err := sh.CaptureEnvWithOutputContextTimeout(ctx, "git push --force-with-lease origin HEAD:refs/heads/"+sh.Quote(branch), workspace, githubEnv, true, config.Budget.CommandTimeout); err != nil {
		return "", fmt.Errorf("git push failed for %s: %w", branch, err)
	}

	github, githubCtx, cancel, err := githubClientWithContextTimeout(ctx, config.Budget.GitHubTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	if strings.TrimSpace(agentPRURL) != "" {
		resolved, reason, used, err := validateAdvisoryPRForHandoff(githubCtx, github, config, candidate, agentPRURL)
		if err != nil {
			return "", err
		}
		if reason != "" {
			return "", fmt.Errorf("PR handoff validation failed: %s", reason)
		}
		if used {
			return resolved, nil
		}
	}
	details, err := resolveHandoffPRByBranch(githubCtx, github, candidate)
	if err != nil {
		if !strings.Contains(err.Error(), "no open PR found") {
			return "", err
		}
		title, body := handoffPRTitleBody(candidate)
		details, err = github.CreatePullRequest(githubCtx, title, body, branch, base)
		if err != nil {
			return "", err
		}
	} else {
		title, body := handoffPRTitleBody(candidate)
		updated, updateErr := github.UpdatePullRequest(githubCtx, details.Number, title, body, base)
		if updateErr != nil {
			return "", updateErr
		}
		if updated.URL != "" {
			details = updated
		}
	}
	if reason := prHandoffBlockReason(config, candidate, details); reason != "" {
		return "", fmt.Errorf("PR handoff validation failed: %s", reason)
	}
	return details.URL, nil
}

func prHandoffPendingPayloadFromInput(input prHandoffInput) prHandoffPendingPayload {
	payload := prHandoffPendingPayload{
		SchemaVersion:    1,
		Workspace:        input.Workspace,
		AgentPRURL:       input.AgentPRURL,
		StartedAt:        time.Now().UTC(),
		AttemptStartedAt: input.AttemptStartedAt,
		ProgressStarted:  input.ProgressStarted,
		RuntimeUsage:     input.RuntimeUsage,
		ScopeResult:      input.ScopeResult,
		Validation:       append([]string(nil), input.Validation...),
		GitHubAuth:       input.GitHubAuth,
	}
	if input.Candidate != nil {
		payload.IssueID = input.Candidate.ID
		payload.IssueIdentifier = input.Candidate.Identifier
		payload.IssueTitle = input.Candidate.Title
		payload.IssueURL = input.Candidate.URL
		payload.IssueDescription = input.Candidate.Description
		payload.TeamID = input.Candidate.Team.ID
		payload.Branch = expectedWorkspaceBranch(input.Candidate.Identifier)
	}
	return payload
}

func (p prHandoffPendingPayload) Issue() *issue {
	candidate := &issue{ID: p.IssueID, Identifier: p.IssueIdentifier, Title: p.IssueTitle, URL: p.IssueURL, Description: p.IssueDescription}
	candidate.Team.ID = p.TeamID
	return candidate
}

func (p prHandoffPendingPayload) ReviewWorker(client linearClient, config runnerConfig, stateStore *state.Store, githubEnv map[string]string) reviewWorker {
	startedAt := p.AttemptStartedAt
	if startedAt.IsZero() {
		startedAt = p.StartedAt
	}
	progressStarted := p.ProgressStarted
	if progressStarted.IsZero() {
		progressStarted = p.StartedAt
	}
	return reviewWorker{
		client:          client,
		config:          config,
		stateStore:      stateStore,
		candidate:       p.Issue(),
		workspace:       p.Workspace,
		branch:          p.Branch,
		progressStarted: progressStarted,
		startedAt:       startedAt,
		runtimeUsage:    p.RuntimeUsage,
		prURL:           p.AgentPRURL,
		githubEnv:       githubEnv,
		githubAuth:      p.GitHubAuth,
		scopeResult:     p.ScopeResult,
		validation:      append([]string(nil), p.Validation...),
	}
}

func (p prHandoffPendingPayload) HandoffCompletion(client linearClient, config runnerConfig, stateStore *state.Store, review *reviewResult, prURL, githubAuth string) handoffCompletion {
	startedAt := p.AttemptStartedAt
	if startedAt.IsZero() {
		startedAt = p.StartedAt
	}
	progressStarted := p.ProgressStarted
	if progressStarted.IsZero() {
		progressStarted = p.StartedAt
	}
	return handoffCompletion{
		client:          client,
		config:          config,
		stateStore:      stateStore,
		candidate:       p.Issue(),
		workspace:       p.Workspace,
		branch:          p.Branch,
		progressStarted: progressStarted,
		startedAt:       startedAt,
		runtimeUsage:    p.RuntimeUsage,
		review:          review,
		prURL:           prURL,
		validation:      append([]string(nil), p.Validation...),
		githubAuth:      githubAuth,
	}
}

func writePRHandoffPendingState(config runnerConfig, input prHandoffInput) error {
	return writePRHandoffPendingStateContext(context.Background(), config, input)
}

func writePRHandoffPendingStateContext(ctx context.Context, config runnerConfig, input prHandoffInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload := prHandoffPendingPayloadFromInput(input)
	if err := writePRHandoffPendingPayloadState(config, payload); err != nil {
		return err
	}
	if path, err := prHandoffPendingPayloadPath(config.WorkspaceRoot, payload.IssueIdentifier); err == nil {
		if err := recordPRHandoffPendingPayloadRefContext(ctx, input.StateStore, payload, path); err != nil {
			return err
		}
	}
	return nil
}

func writePRHandoffPendingPayloadState(config runnerConfig, payload prHandoffPendingPayload) error {
	if err := writePRHandoffPendingPayload(config.WorkspaceRoot, payload); err != nil {
		return err
	}
	candidate := payload.Issue()
	progressStarted := payload.ProgressStarted
	if progressStarted.IsZero() {
		progressStarted = payload.StartedAt
	}
	progress := runProgressForIssue(candidate, payload.Workspace, runProgressPhasePRHandoffPending, progressStarted)
	progress.Branch = payload.Branch
	progress.PRURL = payload.AgentPRURL
	progress.Status = runProgressPhasePRHandoffPending
	progress.NextAction = "complete_runner_pr_handoff"
	if path, err := prHandoffPendingPayloadPath(config.WorkspaceRoot, candidate.Identifier); err == nil {
		progress.PRHandoffPayloadPath = path
	}
	return writeRunProgressResult(config.WorkspaceRoot, progress)
}

func writePRHandoffPendingPayload(workspaceRoot string, payload prHandoffPendingPayload) error {
	if payload.SchemaVersion == 0 {
		payload.SchemaVersion = 1
	}
	if strings.TrimSpace(payload.IssueIdentifier) == "" {
		return fmt.Errorf("PR handoff pending payload issue identifier is required")
	}
	path, err := prHandoffPendingPayloadPath(workspaceRoot, payload.IssueIdentifier)
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

func readPRHandoffPendingPayload(workspaceRoot, issueIdentifier string) (prHandoffPendingPayload, error) {
	path, err := prHandoffPendingPayloadPath(workspaceRoot, issueIdentifier)
	if err != nil {
		return prHandoffPendingPayload{}, err
	}
	return readPRHandoffPendingPayloadFromPath(path)
}

func readPRHandoffPendingPayloadFromPath(path string) (prHandoffPendingPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return prHandoffPendingPayload{}, err
	}
	var payload prHandoffPendingPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return prHandoffPendingPayload{}, err
	}
	if payload.SchemaVersion != 1 {
		return prHandoffPendingPayload{}, fmt.Errorf("unsupported PR handoff pending payload schema_version %d", payload.SchemaVersion)
	}
	if payload.RuntimeUsage == nil {
		payload.RuntimeUsage = payload.PiUsage
	}
	return payload, nil
}

var readPRHandoffPendingPayloadForExecution = readPRHandoffPendingPayload

func validateAdvisoryPRForHandoff(ctx context.Context, github githubAPI, config runnerConfig, candidate *issue, prURL string) (string, string, bool, error) {
	owner, repo, ok := parseGitHubPRRepository(prURL)
	if !ok {
		return prURL, "", false, fmt.Errorf("invalid GitHub PR URL %q", prURL)
	}
	expectedOwner, expectedRepo, err := currentGitHubRepo()
	if err != nil {
		return prURL, "", false, err
	}
	if !strings.EqualFold(owner, expectedOwner) || !strings.EqualFold(repo, expectedRepo) {
		return prURL, "", false, fmt.Errorf("PR repository is %s/%s; expected %s/%s", owner, repo, expectedOwner, expectedRepo)
	}

	details, err := github.PullRequestHandoffDetails(ctx, prURL)
	if err != nil {
		if !isRecoverablePRLookupError(err) {
			return prURL, "", false, fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, err)
		}

		fallback, fallbackErr := resolveHandoffPRByBranch(ctx, github, candidate)
		if fallbackErr != nil {
			if strings.Contains(fallbackErr.Error(), "no open PR found") {
				return "", "", false, nil
			}
			return prURL, "", false, fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, fallbackErr)
		}
		details = fallback
	}

	if details.URL == "" {
		details.URL = prURL
	}
	if details.HeadRefName != expectedWorkspaceBranch(candidate.Identifier) {
		return "", "", false, nil
	}
	return details.URL, prHandoffBlockReason(config, candidate, details), true, nil
}

func handoffPRTitleBody(candidate *issue) (string, string) {
	title := strings.TrimSpace(candidate.Identifier + ": " + candidate.Title)
	body := "Runner-owned handoff PR for " + candidate.Identifier + ".\n\nThe implementation agent owns the scoped diff and validation notes; Pi Symphony created or updated this PR deterministically."
	return title, body
}

func parseGitHubPRRepository(prURL string) (string, string, bool) {
	parts := strings.Split(strings.TrimRight(strings.TrimSpace(prURL), "/"), "/")
	if len(parts) < 7 || parts[0] != "https:" || parts[2] != "github.com" || parts[5] != "pull" {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func isRecoverablePRLookupError(err error) bool {
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return message == "" || strings.Contains(message, "404") || strings.Contains(message, "not found") || strings.Contains(message, "invalid github pr url")
}

func resolveHandoffPRByBranch(ctx context.Context, github githubAPI, candidate *issue) (prHandoffDetails, error) {
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		return prHandoffDetails{}, err
	}
	expectedBranch := expectedWorkspaceBranch(candidate.Identifier)
	var matches []prHandoffDetails
	for _, pr := range prs {
		if pr.HeadRefName != expectedBranch {
			continue
		}
		details, detailsErr := github.PullRequestHandoffDetails(ctx, pr.URL)
		if detailsErr != nil {
			return prHandoffDetails{}, detailsErr
		}
		matches = append(matches, details)
	}
	if len(matches) == 0 {
		return prHandoffDetails{}, fmt.Errorf("no open PR found with head branch %q for %s", expectedBranch, candidate.Identifier)
	}
	if len(matches) > 1 {
		numbers := make([]string, 0, len(matches))
		for _, match := range matches {
			numbers = append(numbers, fmt.Sprintf("#%d", match.Number))
		}
		return prHandoffDetails{}, fmt.Errorf("found %d open PRs for head branch %q: %s", len(matches), expectedBranch, strings.Join(numbers, ", "))
	}
	return matches[0], nil
}

func prHandoffBlockReason(config runnerConfig, candidate *issue, details prHandoffDetails) string {
	var reasons []string
	baseBranch := strings.TrimSpace(config.BaseBranch)
	if baseBranch == "" {
		baseBranch = "develop"
	}
	if !strings.EqualFold(details.BaseRefName, baseBranch) {
		reasons = append(reasons, fmt.Sprintf("PR base branch is %q; expected %q", emptyAsUnknown(details.BaseRefName), baseBranch))
	}
	expectedBranch := expectedWorkspaceBranch(candidate.Identifier)
	if details.HeadRefName != expectedBranch {
		reasons = append(reasons, fmt.Sprintf("PR head branch is %q; expected %q", emptyAsUnknown(details.HeadRefName), expectedBranch))
	}
	if details.ChangedFiles > 80 {
		reasons = append(reasons, fmt.Sprintf("PR changes %d files, exceeding the scoped-run limit of 80", details.ChangedFiles))
	}
	if details.Additions > 5000 {
		reasons = append(reasons, fmt.Sprintf("PR adds %d lines, exceeding the scoped-run limit of 5000", details.Additions))
	}
	return strings.Join(reasons, "; ")
}
