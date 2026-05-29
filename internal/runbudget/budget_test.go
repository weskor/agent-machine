package runbudget

import (
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
)

func TestExceededChecksTokenCostAndWallClock(t *testing.T) {
	usage := &domain.Usage{TotalTokens: 11, Cost: &domain.UsageCost{Total: 0.25}}
	if got := Exceeded(domain.Budget{MaxTokens: 10}, time.Now(), usage); !strings.Contains(got, "token budget exceeded") {
		t.Fatalf("expected token budget failure, got %q", got)
	}
	if got := Exceeded(domain.Budget{MaxCost: 0.10}, time.Now(), usage); !strings.Contains(got, "cost budget exceeded") {
		t.Fatalf("expected cost budget failure, got %q", got)
	}
	if got := Exceeded(domain.Budget{WallClock: time.Nanosecond, WallClockText: "1ns"}, time.Now().Add(-time.Second), nil); !strings.Contains(got, "wall-clock budget exceeded") {
		t.Fatalf("expected wall-clock budget failure, got %q", got)
	}
}

func TestFailureCommentOmitsRawOutput(t *testing.T) {
	comment := FailureComment("pi timeout after 1s")
	if strings.Contains(comment, "raw subprocess output") && strings.Contains(comment, "intentionally omitted") {
		return
	}
	t.Fatalf("expected concise omission notice, got %q", comment)
}
