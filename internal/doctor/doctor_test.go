package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/agentruntime"
)

type fakeRuntime struct {
	result agentruntime.PreflightResult
	err    error
}

func (f fakeRuntime) Preflight(context.Context, agentruntime.PreflightInput) (agentruntime.PreflightResult, error) {
	return f.result, f.err
}

func TestEvaluatePassesReadOnlyFirstRunChecks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "am.yaml")
	promptPath := filepath.Join(dir, "am.agent.md")
	writeFile(t, configPath, "tracker:\n  project_slug: CAG\nworkspace:\n  root: .am/workspaces\n")
	writeFile(t, promptPath, "# Agent\n")

	report := Evaluate(context.Background(), Config{
		ConfigPath:         configPath,
		ProjectSlug:        "CAG",
		LinearAPIKey:       "lin_test",
		RepositoryProvider: "github",
		WorkspaceRoot:      filepath.Join(dir, ".am", "workspaces"),
		PromptPath:         promptPath,
		RuntimeProvider:    "codex_cli",
		RuntimeCommand:     "codex exec",
	}, func(provider string) (Runtime, error) {
		if provider != "codex_cli" {
			t.Fatalf("provider = %q, want codex_cli", provider)
		}
		return fakeRuntime{result: agentruntime.PreflightResult{
			Provider: "codex_cli",
			Checks: []agentruntime.PreflightCheck{{
				Name:    "implementation_command",
				Command: "codex exec",
				OK:      true,
				Message: "provider codex_cli resolved executable \"codex\" for implementation_command",
			}},
		}}, nil
	}, mapLookup(map[string]string{"GITHUB_TOKEN": "gh_test"}))

	if failed := report.Failed(); failed != 0 {
		t.Fatalf("failed checks = %d; report = %#v", failed, report.Checks)
	}
}

func TestEvaluateReportsActionableFailures(t *testing.T) {
	dir := t.TempDir()
	fileAncestor := filepath.Join(dir, "not-a-directory")
	writeFile(t, fileAncestor, "not a directory\n")
	report := Evaluate(context.Background(), Config{
		ConfigPath:         "/missing/am.yaml",
		RepositoryProvider: "gitlab",
		WorkspaceRoot:      filepath.Join(fileAncestor, "workspaces"),
		PromptPath:         "/missing/am.agent.md",
		RuntimeProvider:    "codex_cli",
		RuntimeCommand:     "codex exec",
	}, func(string) (Runtime, error) {
		return fakeRuntime{result: agentruntime.PreflightResult{
			Provider: "codex_cli",
			Checks: []agentruntime.PreflightCheck{{
				Name:    "implementation_command",
				Command: "codex exec",
				OK:      false,
				Message: "provider codex_cli could not resolve executable \"codex\" for implementation_command on PATH or as an executable path",
			}},
		}, err: agentruntime.RuntimeError{Kind: agentruntime.RuntimeErrorKindConfiguration, Message: "missing codex"}}, nil
	}, mapLookup(map[string]string{}))

	if failed := report.Failed(); failed < 5 {
		t.Fatalf("failed checks = %d, want at least 5; report = %#v", failed, report.Checks)
	}
	if err := report.Err(); err == nil || !strings.Contains(err.Error(), "doctor found") {
		t.Fatalf("Err() = %v, want doctor failure", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mapLookup(values map[string]string) EnvLookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
