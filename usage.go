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
	"github.com/weskor/agent-machine/internal/reviewpolicy"
)

var prURLPattern = regexp.MustCompile(`https://[^\s"'<>]+/(?:pull|-/merge_requests)/[0-9]+`)
var codexTokensUsedPattern = regexp.MustCompile(`(?im)^tokens used\s*\n\s*([0-9][0-9,]*)\s*$`)

const (
	runtimeProviderPiCLI     = "pi_cli"
	runtimeProviderCodexCLI  = "codex_cli"
	runtimeProviderClaudeCLI = "claude_cli"
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

type claudeUsagePayload struct {
	InputTokens              float64 `json:"input_tokens"`
	OutputTokens             float64 `json:"output_tokens"`
	CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     float64 `json:"cache_read_input_tokens"`
	TotalTokens              float64 `json:"total_tokens"`
}

type claudeResultPayload struct {
	Result       string             `json:"result"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	Usage        claudeUsagePayload `json:"usage"`
}

func claudeUsageToRuntime(output string) *agentruntime.AttemptUsage {
	var parsed *agentruntime.AttemptUsage
	forEachClaudeResult(output, func(result claudeResultPayload) {
		usage := result.Usage
		total := usage.TotalTokens
		if total == 0 {
			total = usage.InputTokens + usage.OutputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		}
		if total == 0 && result.TotalCostUSD == 0 {
			return
		}
		parsed = &agentruntime.AttemptUsage{
			Input:       usage.InputTokens,
			Output:      usage.OutputTokens,
			CacheRead:   usage.CacheReadInputTokens,
			CacheWrite:  usage.CacheCreationInputTokens,
			TotalTokens: total,
			CostTotal:   result.TotalCostUSD,
		}
	})
	return parsed
}

func claudeResultText(output string) string {
	var resultText string
	forEachClaudeResult(output, func(result claudeResultPayload) {
		if strings.TrimSpace(result.Result) != "" {
			resultText = result.Result
		}
	})
	return resultText
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
	if text := claudeResultText(output); text != "" {
		envelope, ok, err := agentruntime.ParseAttemptOutcomeEnvelope(text)
		if ok || err != nil {
			return envelope, ok, err
		}
	}
	return agentruntime.ParseAttemptOutcomeEnvelope(output)
}

func forEachClaudeResult(output string, visit func(claudeResultPayload)) {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoded := false
	for {
		var result claudeResultPayload
		if err := decoder.Decode(&result); err != nil {
			break
		}
		if result.Result != "" || result.Usage != (claudeUsagePayload{}) || result.TotalCostUSD != 0 {
			decoded = true
			visit(result)
		}
	}
	if decoded {
		return
	}
	forEachJSONLLine(output, func(line string) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			return
		}
		var result claudeResultPayload
		if err := json.Unmarshal([]byte(line), &result); err == nil && (result.Result != "" || result.Usage != (claudeUsagePayload{}) || result.TotalCostUSD != 0) {
			visit(result)
		}
	})
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
