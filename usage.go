package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/weskor/agent-machine/internal/agentruntime"
	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/codehost"
)

var prURLPattern = regexp.MustCompile(`https://[^\s"'<>]+/(?:pull|-/merge_requests)/[0-9]+`)
var codexTokensUsedPattern = regexp.MustCompile(`(?im)^tokens used\s*\n\s*([0-9][0-9,]*)\s*$`)

const (
	runtimeProviderPiCLI    = "pi_cli"
	runtimeProviderCodexCLI = "codex_cli"
)

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
		ReviewStatus:         reviewStatus,
		ReviewClassification: reviewClassification,
	}
}

func newCodexCLIRuntime() agentruntime.AgentRuntime {
	return agentruntime.CodexCLIAdapter{
		RunCommand:           captureAgentOutput,
		ParseUsage:           codexUsageToRuntime,
		FirstPRURL:           firstPRURL,
		NeedsInfoQuestions:   needsInfoQuestionsToRuntime,
		ReviewStatus:         reviewStatus,
		ReviewClassification: reviewClassification,
	}
}

func newAgentRuntime(provider string) (agentruntime.AgentRuntime, error) {
	switch strings.TrimSpace(provider) {
	case "", runtimeProviderPiCLI:
		return newPiCLIRuntime(), nil
	case runtimeProviderCodexCLI:
		return newCodexCLIRuntime(), nil
	default:
		return nil, fmt.Errorf("unsupported runtime.provider %q; supported providers: %s, %s", provider, runtimeProviderPiCLI, runtimeProviderCodexCLI)
	}
}

func configuredRuntimeCommand(config runnerConfig) string {
	if strings.TrimSpace(config.RuntimeCommand) != "" {
		return config.RuntimeCommand
	}
	return config.PiCommand
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
	match := codexTokensUsedPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return nil
	}
	total, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	if err != nil {
		return nil
	}
	return &agentruntime.AttemptUsage{TotalTokens: total}
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
	for _, match := range prURLPattern.FindAllString(output, -1) {
		parsed, ok := codehost.ParsePullRequestURL(match)
		if !ok {
			continue
		}
		if repoKnown && (parsed.Owner != owner || parsed.Repo != repo) {
			continue
		}
		return match
	}
	return ""
}

func usageSummary(u *usage) string {
	if u == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f total tokens, estimated cost $%.4f", u.TotalTokens, u.TotalCost())
}

func assistantText(output string) string {
	var last string
	forEachJSONLLine(output, func(line string) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			return
		}
		var event struct {
			Message *struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Message == nil || event.Message.Role != "assistant" {
			return
		}
		var parts []string
		for _, content := range event.Message.Content {
			if content.Type == "text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
		if len(parts) > 0 {
			last = strings.Join(parts, "\n")
		}
	})
	return strings.TrimSpace(last)
}

func forEachJSONLLine(output string, visit func(string)) {
	reader := bufio.NewReader(strings.NewReader(output))
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			visit(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if err != io.EOF {
				log("warning: failed to read Pi JSONL output: %v", err)
			}
			return
		}
	}
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
