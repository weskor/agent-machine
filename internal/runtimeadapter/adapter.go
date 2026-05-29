package runtimeadapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/rawoutput"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
)

const (
	ProviderPiCLI     = "pi_cli"
	ProviderCodexCLI  = "codex_cli"
	ProviderClaudeCLI = "claude_cli"
)

type Dependencies struct {
	CurrentRepository  func() (string, string, error)
	NeedsInfoQuestions func(string) []string
	Logf               func(string, ...any)
}

func New(provider string, deps Dependencies) (agentruntime.AgentRuntime, error) {
	runCommand := func(ctx context.Context, command, workspace string, env map[string]string, timeout time.Duration, phase string) (string, error) {
		return rawoutput.Capture(ctx, command, workspace, env, timeout, phase, deps.Logf)
	}
	firstPRURL := func(output string) string {
		return FirstPRURL(output, deps.CurrentRepository)
	}
	switch strings.TrimSpace(provider) {
	case "", ProviderPiCLI:
		return agentruntime.PiCLIAdapter{
			RunCommand:           runCommand,
			ParseUsage:           UsageToRuntime,
			FirstPRURL:           firstPRURL,
			NeedsInfoQuestions:   deps.NeedsInfoQuestions,
			AssistantText:        AssistantText,
			ReviewStatus:         reviewpolicy.Status,
			ReviewClassification: reviewpolicy.Classification,
		}, nil
	case ProviderCodexCLI:
		return agentruntime.CodexCLIAdapter{
			RunCommand:           runCommand,
			ParseUsage:           agentruntime.CodexUsage,
			FirstPRURL:           firstPRURL,
			NeedsInfoQuestions:   deps.NeedsInfoQuestions,
			ReviewStatus:         reviewpolicy.Status,
			ReviewClassification: reviewpolicy.Classification,
		}, nil
	case ProviderClaudeCLI:
		return agentruntime.ClaudeCLIAdapter{
			RunCommand:           runCommand,
			ParseUsage:           agentruntime.ClaudeUsage,
			FirstPRURL:           func(output string) string { return FirstPRURLFromClaudeOutput(output, deps.CurrentRepository) },
			NeedsInfoQuestions:   func(output string) []string { return ClaudeNeedsInfoQuestions(output, deps.NeedsInfoQuestions) },
			ParseOutcomeEnvelope: agentruntime.ClaudeOutcomeEnvelope,
			ReviewFindings:       agentruntime.ClaudeResultText,
			ReviewStatus:         ClaudeReviewStatus,
			ReviewClassification: ClaudeReviewClassification,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime.provider %q; supported providers: %s, %s, %s", provider, ProviderPiCLI, ProviderCodexCLI, ProviderClaudeCLI)
	}
}

func ParseUsage(output string) *domain.Usage {
	return artifactio.ParseUsage(output)
}

func RecordRuntimeUsage(record domain.RunRecord) *domain.Usage {
	if record.RuntimeUsage != nil {
		return record.RuntimeUsage
	}
	return record.PiUsage
}

func UsageToRuntime(output string) *agentruntime.AttemptUsage {
	return UsageToRuntimeUsage(ParseUsage(output))
}

func UsageToRuntimeUsage(u *domain.Usage) *agentruntime.AttemptUsage {
	if u == nil {
		return nil
	}
	return &agentruntime.AttemptUsage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: u.TotalTokens, CostTotal: u.TotalCost()}
}

func UsageFromRuntime(u *agentruntime.AttemptUsage) *domain.Usage {
	if u == nil {
		return nil
	}
	return &domain.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: u.TotalTokens, Cost: &domain.UsageCost{Total: u.CostTotal}}
}

func ReviewResultFromRuntime(result agentruntime.ReviewResult) *domain.ReviewResult {
	return &domain.ReviewResult{Status: result.Status, Classification: result.Classification, Findings: result.Findings, Usage: UsageFromRuntime(result.Usage)}
}

func FirstPRURL(output string, currentRepository func() (string, string, error)) string {
	owner, repo, err := "", "", error(nil)
	if currentRepository != nil {
		owner, repo, err = currentRepository()
	}
	repoKnown := err == nil
	if text := AssistantText(output); text != "" {
		if prURL := agentruntime.FirstPRURLForRepository(text, owner, repo, repoKnown); prURL != "" {
			return prURL
		}
	}
	return agentruntime.FirstPRURLForRepository(output, owner, repo, repoKnown)
}

func FirstPRURLFromClaudeOutput(output string, currentRepository func() (string, string, error)) string {
	if text := agentruntime.ClaudeResultText(output); text != "" {
		if prURL := FirstPRURL(text, currentRepository); prURL != "" {
			return prURL
		}
	}
	return FirstPRURL(output, currentRepository)
}

func ClaudeNeedsInfoQuestions(output string, needsInfoQuestions func(string) []string) []string {
	if needsInfoQuestions == nil {
		return nil
	}
	if text := agentruntime.ClaudeResultText(output); text != "" {
		return needsInfoQuestions(text)
	}
	return needsInfoQuestions(output)
}

func ClaudeReviewStatus(output string) string {
	if text := agentruntime.ClaudeResultText(output); text != "" {
		return reviewpolicy.Status(text)
	}
	return reviewpolicy.Status(output)
}

func ClaudeReviewClassification(status, output string) string {
	if text := agentruntime.ClaudeResultText(output); text != "" {
		return reviewpolicy.Classification(status, text)
	}
	return reviewpolicy.Classification(status, output)
}

func UsageSummary(u *domain.Usage) string {
	if u == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f total tokens, estimated cost $%.4f", u.TotalTokens, u.TotalCost())
}

func AssistantText(output string) string {
	return agentruntime.AssistantText(output)
}

func ReviewSummary(r *domain.ReviewResult) string {
	if r == nil {
		return "not configured"
	}
	if r.Usage == nil {
		return r.Status
	}
	return fmt.Sprintf("%s (%s)", r.Status, UsageSummary(r.Usage))
}
