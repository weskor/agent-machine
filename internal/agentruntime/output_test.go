package agentruntime

import (
	"strings"
	"testing"
)

func TestCodexUsageParsesTokensUsedSummary(t *testing.T) {
	output := "final answer\n\ntokens used\n70,500\n"
	got := CodexUsage(output)
	if got == nil || got.TotalTokens != 70500 {
		t.Fatalf("unexpected codex usage: %+v", got)
	}
}

func TestClaudeUsageParsesResultJSON(t *testing.T) {
	output := `{"type":"result","result":"done","total_cost_usd":0.1234,"usage":{"input_tokens":100,"output_tokens":25,"cache_creation_input_tokens":7,"cache_read_input_tokens":11}}`
	got := ClaudeUsage(output)
	if got == nil {
		t.Fatal("expected claude usage")
	}
	if got.Input != 100 || got.Output != 25 || got.CacheWrite != 7 || got.CacheRead != 11 || got.TotalTokens != 143 || got.CostTotal != 0.1234 {
		t.Fatalf("unexpected claude usage: %+v", got)
	}
}

func TestClaudeResultTextReturnsLastResult(t *testing.T) {
	output := `{"type":"result","result":"first"}
{"type":"result","result":"second"}`

	if got := ClaudeResultText(output); got != "second" {
		t.Fatalf("unexpected result text: %q", got)
	}
}

func TestClaudeOutcomeEnvelopeReadsResultText(t *testing.T) {
	output := `{"type":"result","result":"done\nAM_OUTCOME: {\"runtime_outcome\":\"needs_info\",\"needs_info_questions\":[\"Which tenant?\"]}"}`
	envelope, ok, err := ClaudeOutcomeEnvelope(output)
	if err != nil {
		t.Fatalf("unexpected envelope error: %v", err)
	}
	if !ok || envelope.RuntimeOutcome != "needs_info" || len(envelope.NeedsInfoQuestions) != 1 || envelope.NeedsInfoQuestions[0] != "Which tenant?" {
		t.Fatalf("unexpected envelope: ok=%v %+v", ok, envelope)
	}
}

func TestAssistantTextReturnsLastAssistantText(t *testing.T) {
	output := `{"message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}
{"message":{"role":"user","content":[{"type":"text","text":"ignore me"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"second"},{"type":"text","text":"part"}]}}
`

	if got := AssistantText(output); got != "second\npart" {
		t.Fatalf("unexpected assistant text: %q", got)
	}
}

func TestAssistantTextHandlesLargeJSONLToolResultBeforeFinalAnswer(t *testing.T) {
	largeLine := `{"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 128*1024) + `"}]}}`
	output := largeLine + "\n" + `{"message":{"role":"assistant","content":[{"type":"text","text":"REVIEW_PASS\n\nFindings:\n- ok"}]}}`

	if got := AssistantText(output); got != "REVIEW_PASS\n\nFindings:\n- ok" {
		t.Fatalf("unexpected assistant text: %q", got)
	}
}

func TestFirstPRURLForRepositorySupportsGitLabMergeRequests(t *testing.T) {
	output := "created https://gitlab.com/weskor/agent-machine/-/merge_requests/123"
	if got := FirstPRURLForRepository(output, "weskor", "agent-machine", true); got != "https://gitlab.com/weskor/agent-machine/-/merge_requests/123" {
		t.Fatalf("unexpected GitLab MR URL: %q", got)
	}
}

func TestFirstPRURLForRepositoryRejectsDifferentRepositoryWhenConfigured(t *testing.T) {
	output := "opened https://github.com/acme/other-repo/pull/123"
	if got := FirstPRURLForRepository(output, "weskor", "agent-machine", true); got != "" {
		t.Fatalf("expected no PR URL, got %q", got)
	}
}
