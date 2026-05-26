package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/ticketcontract"
)

type reviewEvidence struct {
	IssueIdentifier string
	IssueTitle      string
	PRURL           string
	Workspace       string
	BaseBranch      string
	HeadBranch      string
	HeadSHA         string
	ChangedFiles    int
	Additions       int
	Deletions       int
	ChecksStatus    string
	ChecksSummary   string
	ScopeSummary    string
	ReviewGuidance  string
	Validation      []string
	ProgressPath    string
}

func (e reviewEvidence) PromptBlock() string {
	var b strings.Builder
	b.WriteString("Runner-owned deterministic review evidence:\n")
	writeEvidenceLine(&b, "Issue", strings.TrimSpace(e.IssueIdentifier+" "+e.IssueTitle))
	writeEvidenceLine(&b, "PR", e.PRURL)
	writeEvidenceLine(&b, "Workspace", e.Workspace)
	writeEvidenceLine(&b, "Base branch", e.BaseBranch)
	writeEvidenceLine(&b, "Head branch", e.HeadBranch)
	writeEvidenceLine(&b, "Head SHA", e.HeadSHA)
	if e.ChangedFiles > 0 || e.Additions > 0 || e.Deletions > 0 {
		writeEvidenceLine(&b, "Diff size", fmt.Sprintf("files=%d additions=%d deletions=%d", e.ChangedFiles, e.Additions, e.Deletions))
	}
	writeEvidenceLine(&b, "Code-host checks", emptyAsUnknown(e.ChecksStatus)+" — "+emptyAsUnknown(e.ChecksSummary))
	writeEvidenceLine(&b, "Scope guard", e.ScopeSummary)
	if len(e.Validation) > 0 {
		b.WriteString("- Validation:\n")
		for _, line := range e.Validation {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				b.WriteString("  - " + trimmed + "\n")
			}
		}
	}
	writeEvidenceLine(&b, "Progress snapshot", e.ProgressPath)
	b.WriteString("\nUse this packet as the source of truth for deterministic PR/check/scope facts. Focus review findings on semantic/spec quality that the runner cannot compute deterministically.\n")
	return b.String()
}

func writeEvidenceLine(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.WriteString(fmt.Sprintf("- %s: %s\n", label, strings.TrimSpace(value)))
}

func reviewEvidenceFromPRDetails(candidate *issue, workspace string, details prHandoffDetails, scopeResult scopeGuardResult, validation []string, workspaceRoot string) reviewEvidence {
	progressPath, _ := runProgressPath(workspaceRoot, candidate.Identifier)
	scopeSummary := strings.TrimSpace(scopeResult.Summary())
	if scopeSummary == "" && scopeResult.Checked {
		scopeSummary = "changed files matched the Linear ticket path contract"
	}
	status, summary := reviewChecksStatus(details.StatusCheckRollup)
	return reviewEvidence{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, PRURL: details.URL, Workspace: workspace, BaseBranch: details.BaseRefName, HeadBranch: details.HeadRefName, HeadSHA: details.HeadSHA, ChangedFiles: details.ChangedFiles, Additions: details.Additions, Deletions: details.Deletions, ChecksStatus: status, ChecksSummary: summary, ScopeSummary: scopeSummary, Validation: validation, ProgressPath: progressPath}
}

func collectReviewEvidenceContext(parent context.Context, config runnerConfig, candidate *issue, workspace, prURL string, scopeResult scopeGuardResult, validation []string) (reviewEvidence, error) {
	if err := parent.Err(); err != nil {
		return reviewEvidence{}, err
	}
	github, ctx, cancel, err := codeHostClientWithContextTimeout(parent, config, config.Budget.GitHubTimeout)
	if err != nil {
		return reviewEvidence{}, err
	}
	defer cancel()
	for {
		details, err := github.PullRequestHandoffDetails(ctx, prURL)
		if err != nil {
			return reviewEvidence{}, fmt.Errorf("refresh PR review evidence: %w", err)
		}
		if reason := prHandoffBlockReason(config, candidate, details); reason != "" {
			return reviewEvidence{}, fmt.Errorf("refresh PR review evidence: %s", reason)
		}
		evidence := reviewEvidenceFromPRDetails(candidate, workspace, details, scopeResult, validation, config.WorkspaceRoot)
		evidence.ReviewGuidance = config.ReviewGuidance
		if evidence.ChecksStatus == "success" || evidence.ChecksStatus == "failed" {
			return evidence, nil
		}
		select {
		case <-ctx.Done():
			return evidence, nil
		case <-time.After(reviewEvidencePollInterval):
		}
	}
}

func reviewChecksStatus(checks []statusCheck) (string, string) {
	if len(checks) == 0 {
		return "unavailable", "no status checks were reported by the code host"
	}
	if reason := checksBlockReason(checks); reason == "" {
		return "success", summarizeStatusChecks(checks)
	}
	for _, check := range checks {
		if check.Typename == "CheckRun" && (strings.EqualFold(check.Status, "UNKNOWN") || strings.EqualFold(check.Conclusion, "UNKNOWN")) {
			return "unavailable", checksBlockReason(checks)
		}
		if check.Typename == "StatusContext" && strings.EqualFold(check.State, "UNKNOWN") {
			return "unavailable", checksBlockReason(checks)
		}
		if check.Typename == "CheckRun" && !strings.EqualFold(check.Status, "COMPLETED") {
			return "pending", checksBlockReason(checks)
		}
		if check.Typename == "StatusContext" && strings.EqualFold(check.State, "PENDING") {
			return "pending", checksBlockReason(checks)
		}
	}
	return "failed", checksBlockReason(checks)
}

func summarizeStatusChecks(checks []statusCheck) string {
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		label := checkLabel(check)
		switch check.Typename {
		case "CheckRun":
			parts = append(parts, fmt.Sprintf("%s=%s/%s", label, emptyAsUnknown(check.Status), emptyAsUnknown(check.Conclusion)))
		case "StatusContext":
			parts = append(parts, fmt.Sprintf("%s=%s", label, emptyAsUnknown(check.State)))
		default:
			parts = append(parts, fmt.Sprintf("%s=%s", label, emptyAsUnknown(check.Typename)))
		}
	}
	return strings.Join(parts, "; ")
}

func reviewEvidenceNotReadyError(e reviewEvidence) error {
	if e.ChecksStatus == "" || e.ChecksStatus == "success" {
		return nil
	}
	return fmt.Errorf("review not ready: code-host checks %s: %s", e.ChecksStatus, e.ChecksSummary)
}

const reviewPassMarker = reviewpolicy.PassMarker
const reviewFailMarker = reviewpolicy.FailMarker

const (
	reviewClassificationBehaviorSpecBlocker = reviewpolicy.BehaviorSpecBlocker
	reviewClassificationMissingEvidenceOnly = reviewpolicy.MissingEvidenceOnly
	reviewClassificationUnknown             = reviewpolicy.Unknown
)

var reviewEvidencePollInterval = 5 * time.Second

func reviewPrompt(candidate *issue, prURL, workspace, guidance string, evidence *reviewEvidence) string {
	evidenceBlock := "Runner-owned deterministic review evidence: unavailable; use the local git diff and PR details available in the workspace."
	if evidence != nil {
		evidenceBlock = evidence.PromptBlock()
	}
	guidanceBlock := reviewGuidancePromptBlock(guidance)
	return fmt.Sprintf(`Review the final Agent Machine/Pi runner output for %s.

PR: %s
Workspace: %s

%s

Review only. Do not edit files, commit, push, merge, or comment on the code host or Linear.

Check for:
- scope drift from the Linear issue description
- runtime app code, secrets, environment variable, or unrelated-file changes
- missing requested validation evidence
- obvious tenant/security risk
- for refactors/replacements/rewrites: missing behavior-contract evidence, dropped observable behavior, lost side effects or cleanup, weakened error handling, changed security/ownership assumptions, or unverified state transitions

Behavior-contract review requirements for refactors, replacements, and rewrites:
- REVIEW_FAIL if the PR replaces code, dependencies, commands, integrations, workflows, or state-machine logic without an existing-behavior inventory covering inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Use docs/specs/ and docs/adr/ as the behavior-contract source when present, including docs/specs/harness-behavior.md and docs/agents/review-policy.md; REVIEW_FAIL if observable behavior contradicts a spec or ADR without an explicit issue-backed update.
- REVIEW_FAIL if the Behavior Contract Evidence section is missing for broad refactors, replacements, or rewrites: relevant specs/ADRs cited, behavior preserved, behavior intentionally changed with justification, and unknown behavior needing clarification.
- REVIEW_FAIL if a broad mechanical move does not cite relevant specs/ADRs or explicitly state that no spec changes were needed.
- REVIEW_FAIL if observable behavior lacks TDD or characterization-test evidence, or if tests cover only the new abstraction while prior observable behavior is unprotected.
- REVIEW_FAIL if broad replacement work lacks a complexity/LOC budget: expected files touched, expected LOC direction, why net growth is acceptable, bespoke code removed, and when to split.
- REVIEW_FAIL if prior side effects, cleanup, or state transitions are dropped without explicit issue-backed justification.

%s
%s

Use the local git diff and PR details available in the workspace.

Respond with exactly one marker line:
%s — if the PR is safe to hand off
%s — if the PR should go back to Ready for Agent

If you return %s, also include exactly one classification line:
REVIEW_CLASSIFICATION: behavior_spec_blocker
REVIEW_CLASSIFICATION: missing_evidence_only
REVIEW_CLASSIFICATION: unknown

Classification rules:
- behavior_spec_blocker: behavior/spec mismatch, scope drift, validation blocker, security risk, out-of-scope changes, missing required validation, or any finding that means the implementation should be repaired before Human Review.
- missing_evidence_only: the PR exists and validation appears green, but the only concern is missing or insufficient Behavior Contract Evidence in the PR body/handoff notes. Use only when there is no behavior/spec/scope/validation blocker.
- unknown: malformed, ambiguous, or mixed findings. Use unknown unless missing_evidence_only is clearly the only issue.

Then add concise findings.
`, candidate.Identifier, prURL, workspace, evidenceBlock, ticketcontract.ReviewPrompt(), guidanceBlock, reviewPassMarker, reviewFailMarker, reviewFailMarker)
}

func reviewGuidancePromptBlock(guidance string) string {
	trimmed := strings.TrimSpace(guidance)
	if trimmed == "" {
		return ""
	}
	return "\nTarget-repository review guidance from project configuration:\n" + trimmed + "\n"
}

func reviewCommandWithHighReasoning(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "--thinking ") {
		fields := strings.Fields(trimmed)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "--thinking" {
				fields[i+1] = "xhigh"
				return strings.Join(fields, " ")
			}
		}
	}
	return trimmed + " --thinking xhigh"
}

func runReview(reviewCommand, workspace string, candidate *issue, prURL string, env map[string]string, timeout time.Duration, evidence *reviewEvidence) (*reviewResult, error) {
	return runReviewWithProviderContext(context.Background(), runtimeProviderPiCLI, reviewCommand, workspace, candidate, prURL, env, timeout, evidence)
}

func runReviewWithProviderContext(ctx context.Context, provider, reviewCommand, workspace string, candidate *issue, prURL string, env map[string]string, timeout time.Duration, evidence *reviewEvidence) (*reviewResult, error) {
	if strings.TrimSpace(reviewCommand) == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := reviewPrompt(candidate, prURL, workspace, reviewGuidanceFromEvidence(evidence), evidence)
	runtime, err := newAgentRuntime(provider)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	runtimeResult, err := runtime.ReviewAttempt(ctx, candidate.Identifier, agentruntime.ReviewAttemptInput{Command: reviewCommandForProvider(provider, reviewCommand), WorkingDir: workspace, Prompt: prompt, PullRequest: prURL, Timeout: timeout, Environment: env}, agentruntime.NoopSink{})
	log("review duration: %s", time.Since(started).Round(time.Second))
	result := reviewResultFromRuntime(runtimeResult)
	if result.Usage != nil {
		log("review usage: input=%.0f output=%.0f cacheRead=%.0f total=%.0f cost=$%.4f", result.Usage.Input, result.Usage.Output, result.Usage.CacheRead, result.Usage.TotalTokens, result.Usage.TotalCost())
	}
	if err != nil {
		result.Status = "error"
		return result, err
	}
	return result, nil
}

func reviewCommandForProvider(provider, command string) string {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(provider) == runtimeProviderPiCLI {
		return reviewCommandWithHighReasoning(command)
	}
	return command
}

func reviewGuidanceFromEvidence(evidence *reviewEvidence) string {
	if evidence == nil {
		return ""
	}
	return evidence.ReviewGuidance
}

func reviewFailureRoutesToHumanHandoff(review *reviewResult, prURL string) bool {
	return review != nil && review.Status == "failed" && review.Classification == reviewClassificationMissingEvidenceOnly && strings.TrimSpace(prURL) != ""
}
