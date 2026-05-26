package livesmoke

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	prompt := "Allowed paths:\n- ../am.yaml"
	if _, err := AllowedPathFromPrompt(prompt); err == nil {
		t.Fatal("expected unsafe path to be rejected")
	}
}

func TestSmokeMarkerContentTitleCasesUnicodeFilename(t *testing.T) {
	content := SmokeMarkerContent("CAG-1", "docs/smoke/éclair-test.md")
	if !strings.HasPrefix(content, "# Éclair Test\n") {
		t.Fatalf("unexpected marker heading: %q", content)
	}
}

func TestMarkdownReportRecordsIssuesCommandsAndBoundary(t *testing.T) {
	report := Report{
		StartedAt:        time.Date(2026, 5, 25, 16, 52, 52, 0, time.UTC),
		ConfigPath:       "am.yaml",
		SmokeConfig:      "/tmp/smoke/.am/am.live-smoke.yaml",
		WorkspaceRoot:    "/tmp/smoke/.am/workspaces",
		FakeAgent:        true,
		ApplyMerge:       false,
		ReportPath:       ".am/live-smoke/live-smoke-20260525T165252Z.json",
		PublicReportPath: "docs/smoke/live-smoke-20260525T165252Z-evidence.md",
		Issues: []IssueRef{{
			Identifier: "CAG-186",
			URL:        "https://linear.app/wessismore/issue/CAG-186/example",
			Title:      "Agent Machine: disposable live smoke",
			Path:       "docs/smoke/live-smoke-20260525T165252Z-01.md",
		}},
		CommandResults: []CommandResult{
			{Command: "go run . worker implementation --config /tmp/smoke/.am/am.live-smoke.yaml", Success: true, ExitCode: 0},
			{Command: "go run . status --config /tmp/smoke/.am/am.live-smoke.yaml", Success: false, ExitCode: 1, Error: "status failed"},
		},
	}

	markdown := MarkdownReport(report)

	for _, expected := range []string{
		"# Agent Machine Live Smoke Evidence 20260525T165252Z",
		"[CAG-186](https://linear.app/wessismore/issue/CAG-186/example)",
		"`go run . worker implementation --config /tmp/smoke/.am/am.live-smoke.yaml` -> passed",
		"`go run . status --config /tmp/smoke/.am/am.live-smoke.yaml` -> failed (exit 1): status failed",
		"does not replace PR review, CI checks, Linear state inspection, or code-host merge evidence",
	} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in markdown:\n%s", expected, markdown)
		}
	}
}

func TestMarkdownReportDoesNotClaimLegacyCommandSuccess(t *testing.T) {
	markdown := MarkdownReport(Report{Commands: []string{"go run . status"}})
	if !strings.Contains(markdown, "`go run . status` -> recorded (legacy report; exit status unavailable)") {
		t.Fatalf("legacy command should not be reported as passed:\n%s", markdown)
	}
	if !strings.Contains(markdown, "records harness-controlled commands but not their exit status") {
		t.Fatalf("legacy boundary should not claim exit status evidence:\n%s", markdown)
	}
}

func TestPublicReportPathUsesSmokeDocs(t *testing.T) {
	path := PublicReportPath(filepath.Join(".am", "live-smoke", "live-smoke-20260525T165252Z.json"))
	if path != filepath.Join("docs", "smoke", "live-smoke-20260525T165252Z-evidence.md") {
		t.Fatalf("path = %q", path)
	}
}
