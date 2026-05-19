package main

import (
	"fmt"
	"time"
)

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
		cost += usage.TotalCost()
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
