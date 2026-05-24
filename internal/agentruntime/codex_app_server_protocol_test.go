package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexThreadStartRequestUsesAppServerThreadMethod(t *testing.T) {
	req := NewCodexThreadStartRequest(7, SessionStartInput{WorkingDir: "/tmp/work", ApprovalPolicy: "never", Sandbox: "workspace-write", Model: "gpt-5"})

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`"jsonrpc":"2.0"`,
		`"method":"thread/start"`,
		`"approvalPolicy":"never"`,
		`"cwd":"/tmp/work"`,
		`"sandbox":"workspace-write"`,
		`"ephemeral":false`,
		`"kind":"pi_symphony"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("thread/start request missing %s: %s", want, body)
		}
	}
}

func TestCodexTurnStartRequestUsesPersistentThread(t *testing.T) {
	req := NewCodexTurnStartRequest(8, SessionTurnInput{SessionID: "thread-1", Prompt: "continue", WorkingDir: "/tmp/work"}, "never", "workspace-write", "gpt-5")

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`"method":"turn/start"`,
		`"threadId":"thread-1"`,
		`"type":"input_text"`,
		`"text":"continue"`,
		`"cwd":"/tmp/work"`,
		`"approvalPolicy":"never"`,
		`"sandboxPolicy":"workspace-write"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("turn/start request missing %s: %s", want, body)
		}
	}
}
