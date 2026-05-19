package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var prURLPattern = regexp.MustCompile(`https://github\.com/([^/\s"'<>]+)/([^/\s"'<>]+)/pull/[0-9]+`)

func parseUsage(output string) *usage {
	var last *usage
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Message *struct {
				Usage *usage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err == nil && event.Message != nil && event.Message.Usage != nil {
			candidate := event.Message.Usage
			if candidate.TotalTokens > 0 {
				last = candidate
			}
		}
	}
	return last
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
	for _, match := range prURLPattern.FindAllStringSubmatch(output, -1) {
		if len(match) < 3 {
			continue
		}
		if repoKnown && (match[1] != owner || match[2] != repo) {
			continue
		}
		return match[0]
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
