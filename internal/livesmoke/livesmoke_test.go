package livesmoke

import (
	"strings"
	"testing"
)

func TestValidateEnvironmentRequiresExplicitGates(t *testing.T) {
	if err := ValidateEnvironment(map[string]string{"LINEAR_API_KEY": "x"}, false); err == nil || !strings.Contains(err.Error(), "LIVE_LINEAR=1") {
		t.Fatalf("expected LIVE_LINEAR gate, got %v", err)
	}
	if err := ValidateEnvironment(map[string]string{"LIVE_LINEAR": "1"}, false); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Fatalf("expected LINEAR_API_KEY gate, got %v", err)
	}
	if err := ValidateEnvironment(map[string]string{"LIVE_LINEAR": "1", "LINEAR_API_KEY": "x"}, true); err == nil || !strings.Contains(err.Error(), "LIVE_SMOKE_APPLY=1") {
		t.Fatalf("expected apply gate, got %v", err)
	}
	if err := ValidateEnvironment(map[string]string{"LIVE_LINEAR": "1", "LINEAR_API_KEY": "x", "LIVE_SMOKE_APPLY": "1"}, true); err != nil {
		t.Fatalf("expected gates to pass, got %v", err)
	}
}

func TestAllowedPathFromPromptAcceptsScopedSmokePath(t *testing.T) {
	prompt := "Identifier: CAG-123\n\nAllowed paths:\n- docs/smoke/live-smoke-CAG-123.md\n\nOut of scope:\n- other files"
	path, err := AllowedPathFromPrompt(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if path != "docs/smoke/live-smoke-CAG-123.md" {
		t.Fatalf("path = %q", path)
	}
	if got := IssueIdentifierFromPrompt(prompt); got != "CAG-123" {
		t.Fatalf("identifier = %q", got)
	}
}

func TestAllowedPathFromPromptRejectsUnsafePath(t *testing.T) {
	prompt := "Allowed paths:\n- ../WORKFLOW.md"
	if _, err := AllowedPathFromPrompt(prompt); err == nil {
		t.Fatal("expected unsafe path to be rejected")
	}
}
