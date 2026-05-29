package terminalpr

import (
	"context"
	"errors"
	"testing"
)

type fakeStateClient struct {
	state  string
	merged bool
	err    error
}

func (f fakeStateClient) PullRequestState(context.Context, string) (string, bool, error) {
	return f.state, f.merged, f.err
}

func TestMergedFactsReturnsAlreadyMergedFacts(t *testing.T) {
	facts, merged, err := MergedFacts(context.Background(), fakeStateClient{state: "MERGED", merged: true}, " https://github.com/weskor/agent-machine/pull/1 ")
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("merged=false; want true")
	}
	if facts.PRURL != "https://github.com/weskor/agent-machine/pull/1" || facts.State != "MERGED" || facts.Reason != ReasonAlreadyMerged {
		t.Fatalf("facts = %+v; want already-merged facts", facts)
	}
}

func TestMergedFactsIgnoresEmptyClientAndOpenPR(t *testing.T) {
	if facts, merged, err := MergedFacts(context.Background(), nil, "https://github.com/weskor/agent-machine/pull/1"); err != nil || merged || facts != (Facts{}) {
		t.Fatalf("nil client facts=%+v merged=%v err=%v; want empty false nil", facts, merged, err)
	}
	if facts, merged, err := MergedFacts(context.Background(), fakeStateClient{state: "OPEN"}, "https://github.com/weskor/agent-machine/pull/1"); err != nil || merged || facts != (Facts{}) {
		t.Fatalf("open PR facts=%+v merged=%v err=%v; want empty false nil", facts, merged, err)
	}
}

func TestTerminalErrorMatchesSentinelAndFormatsDefaultReason(t *testing.T) {
	err := TerminalError(Facts{PRURL: "https://github.com/weskor/agent-machine/pull/1"})
	if !errors.Is(err, ErrTerminalPullRequest) {
		t.Fatalf("errors.Is(%v, ErrTerminalPullRequest)=false; want true", err)
	}
	if got := err.Error(); got != "pull_request_already_merged (state=UNKNOWN): https://github.com/weskor/agent-machine/pull/1" {
		t.Fatalf("error = %q", got)
	}
}

func TestFactsFromErrorAppliesFallbacks(t *testing.T) {
	facts, ok := FactsFromError(TerminalError(Facts{}), " https://github.com/weskor/agent-machine/pull/2 ")
	if !ok {
		t.Fatal("ok=false; want true")
	}
	if facts.PRURL != "https://github.com/weskor/agent-machine/pull/2" || facts.Reason != ReasonAlreadyMerged {
		t.Fatalf("facts = %+v; want fallback PR URL and default reason", facts)
	}
	if _, ok := FactsFromError(errors.New("other"), ""); ok {
		t.Fatal("ok=true for non-terminal error; want false")
	}
}
