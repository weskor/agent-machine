package main

import (
	"strings"
	"testing"
	"time"
)

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
