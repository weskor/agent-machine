package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseBudgetOverrides(t *testing.T) {
	budget := parseBudget(`budgets:
  wall_clock: 45m
  command_timeout: 3m
  pi_timeout: 25m
  review_timeout: 7m
  merge_timeout: 90s
  github_timeout: 30s
  max_tokens: 120000
  max_cost: 1.25`)

	if budget.WallClock != 45*time.Minute || budget.CommandTimeout != 3*time.Minute || budget.PiTimeout != 25*time.Minute || budget.ReviewTimeout != 7*time.Minute || budget.MergeTimeout != 90*time.Second || budget.GitHubTimeout != 30*time.Second {
		t.Fatalf("unexpected parsed durations: %#v", budget)
	}
	if budget.MaxTokens != 120000 || budget.MaxCost != 1.25 {
		t.Fatalf("unexpected parsed limits: %#v", budget)
	}
}

func TestBudgetExceededChecksTokenCostAndWallClock(t *testing.T) {
	usage := &usage{TotalTokens: 11, Cost: &usageCost{Total: 0.25}}
	if got := budgetExceeded(runBudget{MaxTokens: 10}, time.Now(), usage); !strings.Contains(got, "token budget exceeded") {
		t.Fatalf("expected token budget failure, got %q", got)
	}
	if got := budgetExceeded(runBudget{MaxCost: 0.10}, time.Now(), usage); !strings.Contains(got, "cost budget exceeded") {
		t.Fatalf("expected cost budget failure, got %q", got)
	}
	if got := budgetExceeded(runBudget{WallClock: time.Nanosecond, WallClockText: "1ns"}, time.Now().Add(-time.Second), nil); !strings.Contains(got, "wall-clock budget exceeded") {
		t.Fatalf("expected wall-clock budget failure, got %q", got)
	}
}

func TestBudgetFailureCommentOmitsRawOutput(t *testing.T) {
	comment := renderBudgetFailureComment("pi timeout after 1s")
	if strings.Contains(comment, "raw subprocess output") && strings.Contains(comment, "intentionally omitted") {
		return
	}
	t.Fatalf("expected concise omission notice, got %q", comment)
}
