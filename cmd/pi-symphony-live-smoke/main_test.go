package main

import (
	"os"
	"strings"
	"testing"

	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/livesmoke"
)

func TestParseOptionsUsesProvidedIssuesAsCount(t *testing.T) {
	opts := parseOptions([]string{"--issue", "CAG-1", "--issue", "CAG-2", "--count", "9", "--concurrency", "2"})
	if opts.count != 2 {
		t.Fatalf("count = %d, want 2", opts.count)
	}
	if opts.concurrency != 2 || len(opts.issues) != 2 {
		t.Fatalf("unexpected opts: %#v", opts)
	}
}

func TestWriteSmokeConfigUsesSafeGeneratedConfig(t *testing.T) {
	root := t.TempDir()
	config := cfg.Config{
		Tracker:   cfg.TrackerConfig{Endpoint: "https://api.linear.app/graphql", ProjectSlug: "project-slug", NeedsInfoState: "Needs Info"},
		Workspace: cfg.WorkspaceConfig{BaseBranch: "main"},
		Hooks:     cfg.HooksConfig{TimeoutText: "120000"},
		Agent:     cfg.AgentConfig{MaxRetryBackoffText: "300000"},
		Pi:        cfg.PiConfig{AfterCreate: "git clone --branch main git@github.com:weskor/pi-symphony.git ."},
		GitHub:    cfg.GitHubConfig{AppSlug: "pi-symphony-bot"},
		Workflow:  cfg.WorkflowConfig{HandoffState: "Human Review", RunningState: "In Progress", NeedsInfoState: "Needs Info", DoneState: "Done"},
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
		"repository:\n  remote: \"git@github.com:weskor/pi-symphony.git\"",
		"root: \"" + root + "\"",
		"prompt_path: \"symphony.live-smoke.agent.md\"",
		"max_concurrent_agents: 2",
		"go run ./cmd/pi-symphony-live-smoke-agent --role implementation",
		"go run ./cmd/pi-symphony-live-smoke-agent --role review",
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
	if _, err := cfg.ParseConfig(proj.YAML); err != nil {
		t.Fatalf("generated config was not parseable: %v", err)
	}
	if !strings.Contains(proj.Prompt, "Pi Symphony Live Smoke Prompt") {
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
	opts := applyReportOptions(options{project: "symphony.yaml"}, livesmoke.Report{
		ConfigPath:    "symphony.yaml",
		WorkspaceRoot: "/tmp/smoke/.symphony/workspaces",
		Issues:        []livesmoke.IssueRef{{Identifier: "CAG-1"}, {Identifier: "CAG-2"}},
	})
	if opts.workspaceRoot != "/tmp/smoke/.symphony/workspaces" {
		t.Fatalf("workspaceRoot = %q", opts.workspaceRoot)
	}
	if opts.count != 2 || strings.Join(opts.issues, ",") != "CAG-1,CAG-2" {
		t.Fatalf("unexpected issues/count: %#v", opts)
	}
}
