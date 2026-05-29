package agentruntime

import (
	"bufio"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/weskor/agent-machine/internal/codehost"
)

var (
	prURLPattern           = regexp.MustCompile(`https://[^\s"'<>]+/(?:pull|-/merge_requests)/[0-9]+`)
	codexTokensUsedPattern = regexp.MustCompile(`(?im)^tokens used\s*\n\s*([0-9][0-9,]*)\s*$`)
)

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

func CodexUsage(output string) *AttemptUsage {
	match := codexTokensUsedPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return nil
	}
	total, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	if err != nil {
		return nil
	}
	return &AttemptUsage{TotalTokens: total}
}

func ClaudeUsage(output string) *AttemptUsage {
	var parsed *AttemptUsage
	forEachClaudeResult(output, func(result claudeResultPayload) {
		usage := result.Usage
		total := usage.TotalTokens
		if total == 0 {
			total = usage.InputTokens + usage.OutputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		}
		if total == 0 && result.TotalCostUSD == 0 {
			return
		}
		parsed = &AttemptUsage{
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

func ClaudeResultText(output string) string {
	var resultText string
	forEachClaudeResult(output, func(result claudeResultPayload) {
		if strings.TrimSpace(result.Result) != "" {
			resultText = result.Result
		}
	})
	return resultText
}

func ClaudeOutcomeEnvelope(output string) (AttemptOutcomeEnvelope, bool, error) {
	if text := ClaudeResultText(output); text != "" {
		envelope, ok, err := ParseAttemptOutcomeEnvelope(text)
		if ok || err != nil {
			return envelope, ok, err
		}
	}
	return ParseAttemptOutcomeEnvelope(output)
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
	ForEachJSONLLine(output, func(line string) {
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

func AssistantText(output string) string {
	var last string
	ForEachJSONLLine(output, func(line string) {
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

func ForEachJSONLLine(output string, visit func(string)) {
	reader := bufio.NewReader(strings.NewReader(output))
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			visit(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return
		}
	}
}

func FirstPRURLForRepository(output, owner, repo string, repoKnown bool) string {
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
