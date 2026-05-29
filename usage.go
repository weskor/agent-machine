package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/rawoutput"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
)

const (
	runtimeProviderPiCLI     = "pi_cli"
	runtimeProviderCodexCLI  = "codex_cli"
	runtimeProviderClaudeCLI = "claude_cli"
)

func captureAgentOutput(ctx context.Context, command, workspace string, env map[string]string, timeout time.Duration, phase string) (string, error) {
	return rawoutput.Capture(ctx, command, workspace, env, timeout, phase, log)
}

func parseUsage(output string) *usage {
	return artifactio.ParseUsage(output)
}

func newPiCLIRuntime() agentruntime.AgentRuntime {
	return agentruntime.PiCLIAdapter{
		RunCommand:           captureAgentOutput,
		ParseUsage:           usageToRuntime,
		FirstPRURL:           firstPRURL,
		NeedsInfoQuestions:   needsInfoQuestionsToRuntime,
		AssistantText:        assistantText,
		ReviewStatus:         reviewpolicy.Status,
		ReviewClassification: reviewpolicy.Classification,
	}
}

func newCodexCLIRuntime() agentruntime.AgentRuntime {
	return agentruntime.CodexCLIAdapter{
		RunCommand:           captureAgentOutput,
		ParseUsage:           codexUsageToRuntime,
		FirstPRURL:           firstPRURL,
		NeedsInfoQuestions:   needsInfoQuestionsToRuntime,
		ReviewStatus:         reviewpolicy.Status,
		ReviewClassification: reviewpolicy.Classification,
	}
}

func newClaudeCLIRuntime() agentruntime.AgentRuntime {
	return agentruntime.ClaudeCLIAdapter{
		RunCommand:           captureAgentOutput,
		ParseUsage:           claudeUsageToRuntime,
		FirstPRURL:           firstPRURLFromClaudeOutput,
		NeedsInfoQuestions:   claudeNeedsInfoQuestionsToRuntime,
		ParseOutcomeEnvelope: claudeParseOutcomeEnvelope,
		ReviewFindings:       claudeResultText,
		ReviewStatus:         claudeReviewStatus,
		ReviewClassification: claudeReviewClassification,
	}
}

func newAgentRuntime(provider string) (agentruntime.AgentRuntime, error) {
	switch strings.TrimSpace(provider) {
	case "", runtimeProviderPiCLI:
		return newPiCLIRuntime(), nil
	case runtimeProviderCodexCLI:
		return newCodexCLIRuntime(), nil
	case runtimeProviderClaudeCLI:
		return newClaudeCLIRuntime(), nil
	default:
		return nil, fmt.Errorf("unsupported runtime.provider %q; supported providers: %s, %s, %s", provider, runtimeProviderPiCLI, runtimeProviderCodexCLI, runtimeProviderClaudeCLI)
	}
}

func recordRuntimeUsage(record runRecord) *usage {
	if record.RuntimeUsage != nil {
		return record.RuntimeUsage
	}
	return record.PiUsage
}

func usageToRuntime(output string) *agentruntime.AttemptUsage {
	return usageToRuntimeUsage(parseUsage(output))
}

func codexUsageToRuntime(output string) *agentruntime.AttemptUsage {
	return agentruntime.CodexUsage(output)
}

func claudeUsageToRuntime(output string) *agentruntime.AttemptUsage {
	return agentruntime.ClaudeUsage(output)
}

func claudeResultText(output string) string {
	return agentruntime.ClaudeResultText(output)
}

func firstPRURLFromClaudeOutput(output string) string {
	if text := claudeResultText(output); text != "" {
		if prURL := firstPRURL(text); prURL != "" {
			return prURL
		}
	}
	return firstPRURL(output)
}

func claudeNeedsInfoQuestionsToRuntime(output string) []string {
	if text := claudeResultText(output); text != "" {
		return needsInfoQuestionsToRuntime(text)
	}
	return needsInfoQuestionsToRuntime(output)
}

func claudeReviewStatus(output string) string {
	if text := claudeResultText(output); text != "" {
		return reviewpolicy.Status(text)
	}
	return reviewpolicy.Status(output)
}

func claudeReviewClassification(status, output string) string {
	if text := claudeResultText(output); text != "" {
		return reviewpolicy.Classification(status, text)
	}
	return reviewpolicy.Classification(status, output)
}

func claudeParseOutcomeEnvelope(output string) (agentruntime.AttemptOutcomeEnvelope, bool, error) {
	return agentruntime.ClaudeOutcomeEnvelope(output)
}

func usageToRuntimeUsage(u *usage) *agentruntime.AttemptUsage {
	if u == nil {
		return nil
	}
	return &agentruntime.AttemptUsage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: u.TotalTokens, CostTotal: u.TotalCost()}
}

func usageFromRuntime(u *agentruntime.AttemptUsage) *usage {
	if u == nil {
		return nil
	}
	return &usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: u.TotalTokens, Cost: &usageCost{Total: u.CostTotal}}
}

func needsInfoQuestionsToRuntime(output string) []string {
	needsInfo := parseNeedsInfo(output)
	if !needsInfo.NeedsInfo {
		return nil
	}
	return needsInfo.Questions
}

func reviewResultFromRuntime(result agentruntime.ReviewResult) *reviewResult {
	return &reviewResult{Status: result.Status, Classification: result.Classification, Findings: result.Findings, Usage: usageFromRuntime(result.Usage)}
}

func firstPRURL(output string) string {
	owner, repo, err := currentGitHubRepo()
	repoKnown := err == nil
	if text := assistantText(output); text != "" {
		if prURL := firstPRURLForRepository(text, owner, repo, repoKnown); prURL != "" {
			return prURL
		}
	}
	return firstPRURLForRepository(output, owner, repo, repoKnown)
}

func firstPRURLForRepository(output, owner, repo string, repoKnown bool) string {
	return agentruntime.FirstPRURLForRepository(output, owner, repo, repoKnown)
}

func usageSummary(u *usage) string {
	if u == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f total tokens, estimated cost $%.4f", u.TotalTokens, u.TotalCost())
}

func assistantText(output string) string {
	return agentruntime.AssistantText(output)
}

func reviewSummary(r *reviewResult) string {
	if r == nil {
		return "not configured"
	}
	if r.Usage == nil {
		return r.Status
	}
	return fmt.Sprintf("%s (%s)", r.Status, usageSummary(r.Usage))
}
