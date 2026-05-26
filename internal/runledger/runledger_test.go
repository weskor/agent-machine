package runledger

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPathUsesRunnerStateOutsideIssueWorkspace(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".am", "workspaces")
	path, err := Path(workspaceRoot, "CAG-190")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(repoRoot, ".am", "state", "run-ledger", "CAG-190", FileName)
	if path != want {
		t.Fatalf("Path() = %q, want %q", path, want)
	}
}

func TestAppendReadAndFormatEvents(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	observed := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{IssueIdentifier: "CAG-190", Phase: "selected", ObservedAt: observed, Workspace: filepath.Join(root, "CAG-190")},
		{IssueIdentifier: "CAG-190", Phase: "completed", Status: "success", Outcome: "handoff_ready", PRURL: "https://github.com/acme/repo/pull/190", ObservedAt: observed.Add(time.Minute), NextAction: "await_approval_and_green_checks"},
	}
	for _, event := range events {
		if err := Append(root, event); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	read, path, err := Read(root, "CAG-190")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(read) != 2 || read[0].Phase != "selected" || read[1].Status != "success" {
		t.Fatalf("events = %#v", read)
	}
	formatted := Format("CAG-190", read, path)
	for _, expected := range []string{"issue=CAG-190", "events=2", "phase=selected", "phase=completed", "status=success", "pr=https://github.com/acme/repo/pull/190", "next=await_approval_and_green_checks"} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("formatted ledger missing %q:\n%s", expected, formatted)
		}
	}
}
