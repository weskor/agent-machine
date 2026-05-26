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

func TestClaudeUsageToRuntimeParsesResultJSON(t *testing.T) {
	output := `{"type":"result","result":"done","total_cost_usd":0.1234,"usage":{"input_tokens":100,"output_tokens":25,"cache_creation_input_tokens":7,"cache_read_input_tokens":11}}`
	got := claudeUsageToRuntime(output)
	if got == nil {
		t.Fatal("expected claude usage")
	}
	if got.Input != 100 || got.Output != 25 || got.CacheWrite != 7 || got.CacheRead != 11 || got.TotalTokens != 143 || got.CostTotal != 0.1234 {
		t.Fatalf("unexpected claude usage: %+v", got)
	}
}

func TestClaudeResultTextDrivesMarkersFromJSON(t *testing.T) {
	output := `{"type":"result","result":"NEEDS_INFO\n1. Which tenant?\n\nREVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nhttps://github.com/weskor/agent-machine/pull/123"}`
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	if got := firstPRURLFromClaudeOutput(output); got != "https://github.com/weskor/agent-machine/pull/123" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
	if got := claudeNeedsInfoQuestionsToRuntime(output); len(got) != 1 || got[0] != "1. Which tenant?" {
		t.Fatalf("unexpected needs-info questions: %#v", got)
	}
	if got := claudeReviewStatus(output); got != "failed" {
		t.Fatalf("unexpected review status: %q", got)
	}
	if got := claudeReviewClassification("failed", output); got != "missing_evidence_only" {
		t.Fatalf("unexpected review classification: %q", got)
	}
}

func TestClaudeParseOutcomeEnvelopeReadsResultText(t *testing.T) {
	output := `{"type":"result","result":"done\nAM_OUTCOME: {\"runtime_outcome\":\"needs_info\",\"needs_info_questions\":[\"Which tenant?\"]}"}`
	envelope, ok, err := claudeParseOutcomeEnvelope(output)
	if err != nil {
		t.Fatalf("unexpected envelope error: %v", err)
	}
	if !ok || envelope.RuntimeOutcome != "needs_info" || len(envelope.NeedsInfoQuestions) != 1 || envelope.NeedsInfoQuestions[0] != "Which tenant?" {
		t.Fatalf("unexpected envelope: ok=%v %+v", ok, envelope)
	}
}

func TestNewAgentRuntimeRejectsUnsupportedProvider(t *testing.T) {
	removedSessionProvider := "codex_" + "app_server"
	_, err := newAgentRuntime(removedSessionProvider)
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime.provider") || !strings.Contains(err.Error(), "codex_cli") || !strings.Contains(err.Error(), "claude_cli") {
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

func TestNewAgentRuntimeSupportsClaudeProvider(t *testing.T) {
	runtime, err := newAgentRuntime("claude_cli")
	if err != nil {
		t.Fatalf("newAgentRuntime returned error: %v", err)
	}
	if runtime == nil {
		t.Fatal("expected claude runtime")
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
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	output := "opened https://github.com/weskor/agent-machine/pull/123 and then https://github.com/weskor/agent-machine/pull/456"
	if got := firstPRURL(output); got != "https://github.com/weskor/agent-machine/pull/123" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLPrefersAssistantTextOverRawJSONL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	output := `{"message":{"role":"user","content":[{"type":"text","text":"ignore old https://github.com/weskor/agent-machine/pull/2"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/weskor/agent-machine/pull/400"}]}}
`

	if got := firstPRURL(output); got != "https://github.com/weskor/agent-machine/pull/400" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLDetectsConfiguredAgentMachineRepositoryFromJSONL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	output := `{"message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/weskor/agent-machine/pull/1"}]}}
`

	if got := firstPRURL(output); got != "https://github.com/weskor/agent-machine/pull/1" {
		t.Fatalf("unexpected PR URL: %q", got)
	}
}

func TestFirstPRURLRejectsDifferentRepositoryWhenConfigured(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	output := "opened https://github.com/acme/other-repo/pull/123"

	if got := firstPRURL(output); got != "" {
		t.Fatalf("expected no PR URL, got %q", got)
	}
}

func TestFirstPRURLForRepositorySupportsGitLabMergeRequests(t *testing.T) {
	output := "created https://gitlab.com/weskor/agent-machine/-/merge_requests/123"
	if got := firstPRURLForRepository(output, "weskor", "agent-machine", true); got != "https://gitlab.com/weskor/agent-machine/-/merge_requests/123" {
		t.Fatalf("unexpected GitLab MR URL: %q", got)
	}
}
