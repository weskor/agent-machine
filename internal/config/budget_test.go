package config

import (
	"testing"
	"time"
)

func TestParseBudgetOverrides(t *testing.T) {
	budget := ParseBudget(`budgets:
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
