package main

import (
	"strings"
	"testing"
)

func TestParseUsageReturnsLastTokenUsageEvent(t *testing.T) {
	output := `plain log line
{"message":{"usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":0,"totalTokens":15,"cost":{"input":0.1,"output":0.2,"cacheRead":0.03,"cacheWrite":0,"total":0.33}}}}
{"message":{"usage":{"input":20,"output":4,"cacheRead":6,"cacheWrite":1,"totalTokens":31,"cost":{"total":0.44}}}}
`

	got := parseUsage(output)
	if got == nil {
		t.Fatal("expected usage")
	}
	if got.TotalTokens != 31 || got.Input != 20 || got.Output != 4 || got.CacheRead != 6 || got.CacheWrite != 1 {
		t.Fatalf("unexpected usage: %+v", got)
	}
	if got.TotalCost() != 0.44 {
		t.Fatalf("unexpected cost: %v", got.TotalCost())
	}
}

func TestParseUsageIgnoresEmptyUsageEvents(t *testing.T) {
	output := `{"message":{"usage":{"input":10,"totalTokens":0}}}`
	if got := parseUsage(output); got != nil {
		t.Fatalf("expected nil usage, got %+v", got)
	}
}

func TestCodexUsageToRuntimeParsesTokensUsedSummary(t *testing.T) {
	output := "final answer\n\ntokens used\n70,500\n"
	got := codexUsageToRuntime(output)
	if got == nil || got.TotalTokens != 70500 {
		t.Fatalf("unexpected codex usage: %+v", got)
	}
}

func TestNewAgentRuntimeRejectsUnsupportedProvider(t *testing.T) {
	removedSessionProvider := "codex_" + "app_server"
	_, err := newAgentRuntime(removedSessionProvider)
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime.provider") || !strings.Contains(err.Error(), "codex_cli") {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
	supportedList := err.Error()
	if marker := strings.Index(supportedList, "supported providers:"); marker >= 0 {
		supportedList = supportedList[marker:]
	}
	if strings.Contains(supportedList, removedSessionProvider) {
		t.Fatalf("removed provider should not be advertised as supported, got %v", err)
	}
}

func TestAssistantTextReturnsLastAssistantText(t *testing.T) {
	output := `{"message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}
{"message":{"role":"user","content":[{"type":"text","text":"ignore me"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"second"},{"type":"text","text":"part"}]}}
`

	if got := assistantText(output); got != "second\npart" {
		t.Fatalf("unexpected assistant text: %q", got)
	}
}

func TestAssistantTextHandlesLargeJSONLToolResultBeforeFinalAnswer(t *testing.T) {
	largeLine := `{"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 128*1024) + `"}]}}`
	output := largeLine + "\n" + `{"message":{"role":"assistant","content":[{"type":"text","text":"REVIEW_PASS\n\nFindings:\n- ok"}]}}`

	if got := assistantText(output); got != "REVIEW_PASS\n\nFindings:\n- ok" {
		t.Fatalf("unexpected assistant text: %q", got)
	}
}

func TestFirstPRURL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")
	output := "opened https://github.com/weskor/pi-symphony/pull/123 and then https://github.com/weskor/pi-symphony/pull/456"
	if got := firstPRURL(output); got != "https://github.com/weskor/pi-symphony/pull/123" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLPrefersAssistantTextOverRawJSONL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")
	output := `{"message":{"role":"user","content":[{"type":"text","text":"ignore old https://github.com/weskor/pi-symphony/pull/2"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/weskor/pi-symphony/pull/400"}]}}
`

	if got := firstPRURL(output); got != "https://github.com/weskor/pi-symphony/pull/400" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLDetectsConfiguredPiSymphonyRepositoryFromJSONL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")
	output := `{"message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/weskor/pi-symphony/pull/1"}]}}
`

	if got := firstPRURL(output); got != "https://github.com/weskor/pi-symphony/pull/1" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLRejectsDifferentRepositoryWhenConfigured(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")
	output := "opened https://github.com/acme/other-repo/pull/123"

	if got := firstPRURL(output); got != "" {
		t.Fatalf("expected no PR URL, got %q", got)
	}
}
