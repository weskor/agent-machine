package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseBudgetOverrides(t *testing.T) {
	budget := ParseBudget(`budgets:
  wall_clock: 45m
  command_timeout: 3m
  runtime_timeout: 25m
  review_timeout: 7m
  merge_timeout: 90s
  github_timeout: 30s
  max_tokens: 120000
  max_cost: 1.25`)

	if budget.WallClock != 45*time.Minute || budget.CommandTimeout != 3*time.Minute || budget.RuntimeTimeout != 25*time.Minute || budget.PiTimeout != 25*time.Minute || budget.ReviewTimeout != 7*time.Minute || budget.MergeTimeout != 90*time.Second || budget.GitHubTimeout != 30*time.Second {
		t.Fatalf("unexpected parsed durations: %#v", budget)
	}
	if budget.MaxTokens != 120000 || budget.MaxCost != 1.25 {
		t.Fatalf("unexpected parsed limits: %#v", budget)
	}
}

func TestParseBudgetAcceptsLegacyPiTimeout(t *testing.T) {
	budget := ParseBudget(`budgets:
  pi_timeout: 25m`)

	if budget.RuntimeTimeout != 25*time.Minute || budget.PiTimeout != 25*time.Minute || budget.RuntimeText != "25m" || budget.PiText != "25m" {
		t.Fatalf("legacy pi_timeout was not normalized: %#v", budget)
	}
}

func TestParseBudgetRuntimeTimeoutOverridesLegacyPiTimeout(t *testing.T) {
	budget := ParseBudget(`budgets:
  pi_timeout: 25m
  runtime_timeout: 30m`)

	if budget.RuntimeTimeout != 30*time.Minute || budget.PiTimeout != 30*time.Minute || budget.RuntimeText != "30m" || budget.PiText != "30m" {
		t.Fatalf("runtime_timeout should override pi_timeout: %#v", budget)
	}
}

func TestBudgetJSONWritesRuntimeTimeoutAndReadsLegacyPiTimeout(t *testing.T) {
	data, err := json.Marshal(Budget{CommandText: "10m", RuntimeText: "25m", PiText: "25m"})
	if err != nil {
		t.Fatalf("marshal budget: %v", err)
	}
	if !strings.Contains(string(data), `"runtime_timeout":"25m"`) || strings.Contains(string(data), "pi_timeout") {
		t.Fatalf("unexpected budget JSON: %s", string(data))
	}

	var decoded Budget
	if err := json.Unmarshal([]byte(`{"pi_timeout":"20m"}`), &decoded); err != nil {
		t.Fatalf("unmarshal legacy budget: %v", err)
	}
	if decoded.RuntimeText != "20m" || decoded.PiText != "20m" {
		t.Fatalf("legacy pi_timeout was not normalized after unmarshal: %#v", decoded)
	}
}

func TestBudgetRuntimeDurationFallsBackToLegacyPiTimeout(t *testing.T) {
	budget := Budget{PiTimeout: 25 * time.Minute}

	if got := budget.RuntimeDuration(); got != 25*time.Minute {
		t.Fatalf("RuntimeDuration() = %v, want legacy pi_timeout fallback", got)
	}
}
