package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	"github.com/weskor/agent-machine/internal/attemptlifecycle"
	mergegate "github.com/weskor/agent-machine/internal/mergegate"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/reviewprompt"
)

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
	return mergegate.ChecksStatus(mergegateStatusChecks(checks))
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
	return reviewprompt.Prompt(candidate, prURL, workspace, guidance, evidence)
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
	return attemptlifecycle.ReviewFailureRoutesToHumanHandoff(review, prURL)
}
