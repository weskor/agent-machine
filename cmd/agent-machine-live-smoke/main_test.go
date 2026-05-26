package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfg "github.com/weskor/agent-machine/internal/config"
	"github.com/weskor/agent-machine/internal/livesmoke"
)

func TestParseOptionsUsesProvidedIssuesAsCount(t *testing.T) {
	opts := parseOptions([]string{"--issue", "CAG-1", "--issue", "CAG-2", "--count", "9", "--concurrency", "2", "--public-report", "auto"})
	if opts.count != 2 {
		t.Fatalf("count = %d, want 2", opts.count)
	}
	if opts.concurrency != 2 || len(opts.issues) != 2 || opts.publicReport != "auto" {
		t.Fatalf("unexpected opts: %#v", opts)
	}
}

func TestWriteSmokeConfigUsesSafeGeneratedConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), `workspace\root`)
	config := cfg.Config{
		Repository: cfg.RepositoryConfig{Remote: `git@github.com:weskor/agent-machine.with.dot.git`},
		Tracker:    cfg.TrackerConfig{Endpoint: "https://api.linear.app/graphql\nnext", ProjectSlug: "project-slug", NeedsInfoState: "Needs Info"},
		Workspace:  cfg.WorkspaceConfig{BaseBranch: "main"},
		Hooks:      cfg.HooksConfig{TimeoutText: "120000"},
		Agent:      cfg.AgentConfig{MaxRetryBackoffText: "300000"},
		Pi:         cfg.PiConfig{AfterCreate: "git clone --branch main git@github.com:weskor/agent-machine.git ."},
		GitHub:     cfg.GitHubConfig{AppSlug: "agent-machine-bot"},
		Workflow:   cfg.WorkflowConfig{HandoffState: "Human Review", RunningState: "In Progress", NeedsInfoState: "Needs Info", DoneState: "Done"},
	}

	path, err := writeSmokeConfig(options{workspaceRoot: root, concurrency: 2}, config)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		"repository:\n  remote: \"git@github.com:weskor/agent-machine.with.dot.git\"",
		"endpoint: \"https://api.linear.app/graphql\\nnext\"",
		"root: " + yamlScalar(root),
		"prompt_path: \"am.live-smoke.agent.md\"",
		"max_concurrent_agents: 2",
		"go run ./cmd/agent-machine-live-smoke-agent --role implementation",
		"go run ./cmd/agent-machine-live-smoke-agent --role review",
		"active_states:\n    - Ready for Agent",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in project:\n%s", expected, text)
		}
	}
	proj, err := cfg.ReadProject(path)
	if err != nil {
		t.Fatalf("generated config was not readable: %v", err)
	}
	parsed, err := cfg.ParseConfig(proj.YAML)
	if err != nil {
		t.Fatalf("generated config was not parseable: %v", err)
	}
	if parsed.Tracker.Endpoint != "https://api.linear.app/graphql\nnext" || parsed.Workspace.Root != root {
		t.Fatalf("generated config did not preserve escaped scalars: endpoint=%q root=%q", parsed.Tracker.Endpoint, parsed.Workspace.Root)
	}
	if !strings.Contains(proj.Prompt, "Agent Machine Live Smoke Prompt") {
		t.Fatalf("generated prompt was not loaded: %q", proj.Prompt)
	}
}

func TestEnvMapParsesKeyValueEntries(t *testing.T) {
	env := envMap([]string{"LIVE_LINEAR=1", "LINEAR_API_KEY=secret", "MALFORMED"})
	if env["LIVE_LINEAR"] != "1" || env["LINEAR_API_KEY"] != "secret" {
		t.Fatalf("unexpected env map: %#v", env)
	}
	if _, ok := env["MALFORMED"]; ok {
		t.Fatalf("malformed env entry should be ignored: %#v", env)
	}
}

func TestLoadDotEnvLocalSetsMissingValues(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	path := t.TempDir() + "/.env.local"
	if err := os.WriteFile(path, []byte("LINEAR_API_KEY=from-file\nexport GH_TOKEN='gh-file'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadDotEnvLocal(path)

	if os.Getenv("LINEAR_API_KEY") != "from-file" || os.Getenv("GH_TOKEN") != "gh-file" {
		t.Fatalf("dotenv values were not loaded")
	}
}

func TestApplyReportOptionsReusesWorkspaceAndIssues(t *testing.T) {
	opts := applyReportOptions(options{project: "am.yaml"}, livesmoke.Report{
		ConfigPath:    "am.yaml",
		WorkspaceRoot: "/tmp/smoke/.am/workspaces",
		Issues:        []livesmoke.IssueRef{{Identifier: "CAG-1"}, {Identifier: "CAG-2"}},
	})
	if opts.workspaceRoot != "/tmp/smoke/.am/workspaces" {
		t.Fatalf("workspaceRoot = %q", opts.workspaceRoot)
	}
	if opts.count != 2 || strings.Join(opts.issues, ",") != "CAG-1,CAG-2" {
		t.Fatalf("unexpected issues/count: %#v", opts)
	}
}

func TestWritePublicReportIsOptional(t *testing.T) {
	if err := writePublicReport(livesmoke.Report{}); err != nil {
		t.Fatalf("writePublicReport without path returned error: %v", err)
	}
}

func TestWritePublicReportWritesMarkdownEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docs", "smoke", "evidence.md")
	report := livesmoke.Report{
		ConfigPath:       "am.yaml",
		SmokeConfig:      "/tmp/smoke/am.live-smoke.yaml",
		WorkspaceRoot:    "/tmp/smoke/workspaces",
		FakeAgent:        true,
		PublicReportPath: path,
		Issues:           []livesmoke.IssueRef{{Identifier: "CAG-1", URL: "https://linear.app/example/CAG-1"}},
		CommandResults:   []livesmoke.CommandResult{{Command: "go run . status", Success: true}},
	}

	if err := writePublicReport(report); err != nil {
		t.Fatalf("writePublicReport returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Agent Machine Live Smoke Evidence") || !strings.Contains(string(data), "go run . status") {
		t.Fatalf("unexpected markdown report:\n%s", data)
	}
}

func TestRenderReportDoesNotRequireLiveEnvironment(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "live-smoke.json")
	publicPath := filepath.Join(dir, "evidence.md")
	report := livesmoke.Report{
		ConfigPath:     "am.yaml",
		SmokeConfig:    filepath.Join(dir, "am.live-smoke.yaml"),
		WorkspaceRoot:  filepath.Join(dir, "workspaces"),
		FakeAgent:      true,
		Issues:         []livesmoke.IssueRef{{Identifier: "CAG-1", URL: "https://linear.app/example/CAG-1"}},
		CommandResults: []livesmoke.CommandResult{{Command: "go run . status", Success: true}},
		FinalStatusRan: true,
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run(context.Background(), []string{"--render-report", "--from-report", reportPath, "--public-report", publicPath}, nil)
	if err != nil {
		t.Fatalf("run render report returned error: %v", err)
	}
	rendered, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "go run . status") || !strings.Contains(string(rendered), "CAG-1") {
		t.Fatalf("unexpected rendered evidence:\n%s", rendered)
	}
}

func TestRenderReportCanAnnotateManualIntervention(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "live-smoke.json")
	publicPath := filepath.Join(dir, "evidence.md")
	report := livesmoke.Report{
		ConfigPath:     "am.yaml",
		SmokeConfig:    filepath.Join(dir, "am.live-smoke.yaml"),
		WorkspaceRoot:  filepath.Join(dir, "workspaces"),
		FakeAgent:      true,
		Issues:         []livesmoke.IssueRef{{Identifier: "CAG-1"}},
		CommandResults: []livesmoke.CommandResult{{Command: "go run . status", Success: true}},
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = run(context.Background(), []string{"--render-report", "--from-report", reportPath, "--public-report", publicPath, "--manual-intervention", "PR approved by operator before merge"}, nil)
	if err != nil {
		t.Fatalf("run render report returned error: %v", err)
	}
	rendered, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "Manual intervention: PR approved by operator before merge") {
		t.Fatalf("manual intervention note missing:\n%s", rendered)
	}
}
