package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaultsAndCompatibility(t *testing.T) {
	config, err := ParseConfig(`tracker:
  project_slug: CAG
workspace:
  root: /tmp/workspaces
pi:
  command: pi --print
`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Tracker.Kind != "linear" || config.Tracker.Endpoint == "" {
		t.Fatalf("tracker defaults not applied: %#v", config.Tracker)
	}
	if config.Workspace.BaseBranch != "develop" || config.Compound.HandoffState != "Human Review" {
		t.Fatalf("compatibility defaults not applied: %#v", config)
	}
	if config.Budgets.WallClock != 2*time.Hour || config.Pi.Command != "pi --print" {
		t.Fatalf("unexpected normalized config: %#v", config)
	}
}

func TestParseConfigExpandsEnvironment(t *testing.T) {
	t.Setenv("SYMPHONY_WORKSPACE_ROOT", "/tmp/from-env")
	config, err := ParseConfig(`tracker:
  project_slug: CAG
workspace:
  root: $SYMPHONY_WORKSPACE_ROOT
`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Workspace.Root != "/tmp/from-env" {
		t.Fatalf("workspace.root = %q", config.Workspace.Root)
	}
}

func TestParseConfigAgentMaxTurnsDefaultsExplicitOneAndInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{name: "unset", yaml: "", want: 1},
		{name: "one", yaml: "agent:\n  max_turns: 1\n", want: 1},
		{name: "zero", yaml: "agent:\n  max_turns: 0\n", want: 1},
		{name: "negative", yaml: "agent:\n  max_turns: -2\n", want: 1},
		{name: "malformed", yaml: "agent:\n  max_turns: many\n", want: 1},
		{name: "unsupported continuation parsed", yaml: "agent:\n  max_turns: 2\n", want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseConfig("tracker:\n  project_slug: CAG\nworkspace:\n  root: /tmp/workspaces\n" + tt.yaml)
			if err != nil {
				t.Fatal(err)
			}
			if config.Agent.MaxTurns != tt.want || AgentMaxTurnsFromWorkflow(tt.yaml) != tt.want {
				t.Fatalf("max_turns = config %d helper %d, want %d", config.Agent.MaxTurns, AgentMaxTurnsFromWorkflow(tt.yaml), tt.want)
			}
		})
	}
}

func TestParseConfigInvalidDurationsReturnFieldPaths(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "budget", yaml: "budgets:\n  pi_timeout: tomorrow\n", want: "budgets.pi_timeout"},
		{name: "polling", yaml: "polling:\n  interval_ms: soon\n", want: "polling.interval_ms"},
		{name: "hooks", yaml: "hooks:\n  timeout_ms: -1\n", want: "hooks.timeout_ms"},
		{name: "agent", yaml: "agent:\n  max_retry_backoff_ms: later\n", want: "agent.max_retry_backoff_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig("tracker:\n  project_slug: CAG\nworkspace:\n  root: /tmp/workspaces\n" + tt.yaml)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want field path %s", err, tt.want)
			}
		})
	}
}

func TestParseConfigMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "project slug", yaml: "workspace:\n  root: /tmp/workspaces\n", want: "tracker.project_slug"},
		{name: "workspace root", yaml: "tracker:\n  project_slug: CAG\nworkspace:\n  root: ''\n", want: "workspace.root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.yaml)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want field path %s", err, tt.want)
			}
		})
	}
}

func TestParseConfigAcceptsUnknownAndEmptyOptionalValues(t *testing.T) {
	_, err := ParseConfig(`tracker:
  project_slug: CAG
  future_field: accepted
workspace:
  root: /tmp/workspaces
github:
  app_slug: ""
compound:
  required_validation: []
  future_field: accepted
`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigWorkflowExamples(t *testing.T) {
	for _, path := range []string{"../../WORKFLOW.example.md", "../../examples/compound-web.WORKFLOW.md"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			wf, err := ReadWorkflow(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseConfig(wf.YAML); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseConfigCurrentWorkflowWhenAvailable(t *testing.T) {
	path := "../../WORKFLOW.md"
	if _, err := os.Stat(path); err != nil {
		t.Skip(err)
	}
	wf, err := ReadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(wf.YAML); err != nil {
		t.Fatal(err)
	}
}
