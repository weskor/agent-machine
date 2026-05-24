package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Budget struct {
	WallClock      time.Duration `json:"-"`
	WallClockText  string        `json:"wall_clock,omitempty"`
	MaxTokens      float64       `json:"max_tokens,omitempty"`
	MaxCost        float64       `json:"max_cost,omitempty"`
	CommandTimeout time.Duration `json:"-"`
	CommandText    string        `json:"command_timeout,omitempty"`
	RuntimeTimeout time.Duration `json:"-"`
	RuntimeText    string        `json:"runtime_timeout,omitempty"`
	PiTimeout      time.Duration `json:"-"`
	PiText         string        `json:"pi_timeout,omitempty"`
	ReviewTimeout  time.Duration `json:"-"`
	ReviewText     string        `json:"review_timeout,omitempty"`
	MergeTimeout   time.Duration `json:"-"`
	MergeText      string        `json:"merge_timeout,omitempty"`
	GitHubTimeout  time.Duration `json:"-"`
	GitHubText     string        `json:"github_timeout,omitempty"`
}

func (b Budget) Active() *Budget {
	if b.RuntimeTimeout == 0 && b.PiTimeout > 0 {
		b.RuntimeTimeout = b.PiTimeout
	}
	if b.RuntimeText == "" && b.PiText != "" {
		b.RuntimeText = b.PiText
	}
	if b.PiTimeout == 0 && b.RuntimeTimeout > 0 {
		b.PiTimeout = b.RuntimeTimeout
	}
	if b.PiText == "" && b.RuntimeText != "" {
		b.PiText = b.RuntimeText
	}
	if b.WallClock > 0 || b.MaxTokens > 0 || b.MaxCost > 0 || b.CommandTimeout > 0 || b.RuntimeTimeout > 0 || b.PiTimeout > 0 || b.ReviewTimeout > 0 || b.MergeTimeout > 0 || b.GitHubTimeout > 0 {
		return &b
	}
	return nil
}

func (b Budget) RuntimeDuration() time.Duration {
	if b.RuntimeTimeout > 0 || b.PiTimeout == 0 {
		return b.RuntimeTimeout
	}
	return b.PiTimeout
}

func (b Budget) MarshalJSON() ([]byte, error) {
	type budgetJSON struct {
		WallClockText string  `json:"wall_clock,omitempty"`
		MaxTokens     float64 `json:"max_tokens,omitempty"`
		MaxCost       float64 `json:"max_cost,omitempty"`
		CommandText   string  `json:"command_timeout,omitempty"`
		RuntimeText   string  `json:"runtime_timeout,omitempty"`
		ReviewText    string  `json:"review_timeout,omitempty"`
		MergeText     string  `json:"merge_timeout,omitempty"`
		GitHubText    string  `json:"github_timeout,omitempty"`
	}
	runtimeText := b.RuntimeText
	if runtimeText == "" {
		runtimeText = b.PiText
	}
	return json.Marshal(budgetJSON{
		WallClockText: b.WallClockText,
		MaxTokens:     b.MaxTokens,
		MaxCost:       b.MaxCost,
		CommandText:   b.CommandText,
		RuntimeText:   runtimeText,
		ReviewText:    b.ReviewText,
		MergeText:     b.MergeText,
		GitHubText:    b.GitHubText,
	})
}

func (b *Budget) UnmarshalJSON(data []byte) error {
	type budgetJSON struct {
		WallClockText string  `json:"wall_clock,omitempty"`
		MaxTokens     float64 `json:"max_tokens,omitempty"`
		MaxCost       float64 `json:"max_cost,omitempty"`
		CommandText   string  `json:"command_timeout,omitempty"`
		RuntimeText   string  `json:"runtime_timeout,omitempty"`
		PiText        string  `json:"pi_timeout,omitempty"`
		ReviewText    string  `json:"review_timeout,omitempty"`
		MergeText     string  `json:"merge_timeout,omitempty"`
		GitHubText    string  `json:"github_timeout,omitempty"`
	}
	var decoded budgetJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	runtimeText := decoded.RuntimeText
	if runtimeText == "" {
		runtimeText = decoded.PiText
	}
	*b = Budget{
		WallClockText: decoded.WallClockText,
		MaxTokens:     decoded.MaxTokens,
		MaxCost:       decoded.MaxCost,
		CommandText:   decoded.CommandText,
		RuntimeText:   runtimeText,
		PiText:        runtimeText,
		ReviewText:    decoded.ReviewText,
		MergeText:     decoded.MergeText,
		GitHubText:    decoded.GitHubText,
	}
	return nil
}

func ParseBudget(yaml string) Budget {
	budget, _ := parseBudget(yaml, false)
	return budget
}

func ParseBudgetValidated(yaml string) (Budget, error) {
	return parseBudget(yaml, true)
}

func parseBudget(yaml string, strict bool) (Budget, error) {
	budgetYAML := Section(yaml, "budgets")
	if budgetYAML == "" {
		budgetYAML = Section(yaml, "resource_budgets")
	}
	budget := Budget{
		WallClock:      2 * time.Hour,
		WallClockText:  "2h",
		CommandTimeout: 10 * time.Minute,
		CommandText:    "10m",
		RuntimeTimeout: 90 * time.Minute,
		RuntimeText:    "90m",
		PiTimeout:      90 * time.Minute,
		PiText:         "90m",
		ReviewTimeout:  30 * time.Minute,
		ReviewText:     "30m",
		MergeTimeout:   10 * time.Minute,
		MergeText:      "10m",
		GitHubTimeout:  2 * time.Minute,
		GitHubText:     "2m",
	}
	var err error
	if budget.WallClock, budget.WallClockText, err = durationFromYAML(budgetYAML, "wall_clock", budget.WallClock, budget.WallClockText, strict); err != nil {
		return Budget{}, err
	}
	if budget.CommandTimeout, budget.CommandText, err = durationFromYAML(budgetYAML, "command_timeout", budget.CommandTimeout, budget.CommandText, strict); err != nil {
		return Budget{}, err
	}
	if budget.PiTimeout, budget.PiText, err = durationFromYAML(budgetYAML, "pi_timeout", budget.PiTimeout, budget.PiText, strict); err != nil {
		return Budget{}, err
	}
	if budget.RuntimeTimeout, budget.RuntimeText, err = durationFromYAML(budgetYAML, "runtime_timeout", budget.PiTimeout, budget.PiText, strict); err != nil {
		return Budget{}, err
	}
	budget.PiTimeout = budget.RuntimeTimeout
	budget.PiText = budget.RuntimeText
	if budget.ReviewTimeout, budget.ReviewText, err = durationFromYAML(budgetYAML, "review_timeout", budget.ReviewTimeout, budget.ReviewText, strict); err != nil {
		return Budget{}, err
	}
	if budget.MergeTimeout, budget.MergeText, err = durationFromYAML(budgetYAML, "merge_timeout", budget.MergeTimeout, budget.MergeText, strict); err != nil {
		return Budget{}, err
	}
	if budget.GitHubTimeout, budget.GitHubText, err = durationFromYAML(budgetYAML, "github_timeout", budget.GitHubTimeout, budget.GitHubText, strict); err != nil {
		return Budget{}, err
	}
	budget.MaxTokens = floatFromYAML(budgetYAML, "max_tokens", 0)
	budget.MaxCost = floatFromYAML(budgetYAML, "max_cost", 0)
	return budget, nil
}

func durationFromYAML(yaml, key string, fallback time.Duration, fallbackText string, strict bool) (time.Duration, string, error) {
	value := Scalar(yaml, "  "+key, "")
	if value == "" {
		return fallback, fallbackText, nil
	}
	if value == "0" || strings.EqualFold(value, "none") || strings.EqualFold(value, "disabled") {
		return 0, "", nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		if strict {
			return 0, "", fmt.Errorf("symphony.yaml budgets.%s must be a Go duration such as 10m or 2h", key)
		}
		return fallback, fallbackText, nil
	}
	return duration, value, nil
}

func floatFromYAML(yaml, key string, fallback float64) float64 {
	value := Scalar(yaml, "  "+key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}
