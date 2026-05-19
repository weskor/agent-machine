package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseBudget(yaml string) runBudget {
	budgetYAML := section(yaml, "budgets")
	if budgetYAML == "" {
		budgetYAML = section(yaml, "resource_budgets")
	}
	budget := runBudget{
		WallClock:      2 * time.Hour,
		WallClockText:  "2h",
		CommandTimeout: 10 * time.Minute,
		CommandText:    "10m",
		PiTimeout:      90 * time.Minute,
		PiText:         "90m",
		ReviewTimeout:  30 * time.Minute,
		ReviewText:     "30m",
		MergeTimeout:   10 * time.Minute,
		MergeText:      "10m",
		GitHubTimeout:  2 * time.Minute,
		GitHubText:     "2m",
	}
	budget.WallClock, budget.WallClockText = durationFromYAML(budgetYAML, "wall_clock", budget.WallClock, budget.WallClockText)
	budget.CommandTimeout, budget.CommandText = durationFromYAML(budgetYAML, "command_timeout", budget.CommandTimeout, budget.CommandText)
	budget.PiTimeout, budget.PiText = durationFromYAML(budgetYAML, "pi_timeout", budget.PiTimeout, budget.PiText)
	budget.ReviewTimeout, budget.ReviewText = durationFromYAML(budgetYAML, "review_timeout", budget.ReviewTimeout, budget.ReviewText)
	budget.MergeTimeout, budget.MergeText = durationFromYAML(budgetYAML, "merge_timeout", budget.MergeTimeout, budget.MergeText)
	budget.GitHubTimeout, budget.GitHubText = durationFromYAML(budgetYAML, "github_timeout", budget.GitHubTimeout, budget.GitHubText)
	budget.MaxTokens = floatFromYAML(budgetYAML, "max_tokens", 0)
	budget.MaxCost = floatFromYAML(budgetYAML, "max_cost", 0)
	return budget
}

func durationFromYAML(yaml, key string, fallback time.Duration, fallbackText string) (time.Duration, string) {
	value := scalar(yaml, "  "+key, "")
	if value == "" {
		return fallback, fallbackText
	}
	if value == "0" || strings.EqualFold(value, "none") || strings.EqualFold(value, "disabled") {
		return 0, ""
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fallbackText
	}
	return duration, value
}

func floatFromYAML(yaml, key string, fallback float64) float64 {
	value := scalar(yaml, "  "+key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func budgetExceeded(b runBudget, started time.Time, usages ...*usage) string {
	if b.WallClock > 0 && time.Since(started) > b.WallClock {
		return fmt.Sprintf("wall-clock budget exceeded (%s)", b.WallClockText)
	}
	var tokens, cost float64
	for _, usage := range usages {
		if usage == nil {
			continue
		}
		tokens += usage.TotalTokens
		cost += usage.totalCost()
	}
	if b.MaxTokens > 0 && tokens > b.MaxTokens {
		return fmt.Sprintf("token budget exceeded (%.0f > %.0f)", tokens, b.MaxTokens)
	}
	if b.MaxCost > 0 && cost > b.MaxCost {
		return fmt.Sprintf("cost budget exceeded ($%.4f > $%.4f)", cost, b.MaxCost)
	}
	return ""
}

func renderBudgetFailureComment(reason string) string {
	return truncateMarkdown(fmt.Sprintf("Go/Pi run stopped before handoff because a runner budget or subprocess timeout was exceeded.\n\nReason: %s\n\nThe run artifact records the terminal status. Prompts and raw subprocess output are intentionally omitted.", sanitizeMarkdownLine(reason)), 1000)
}
