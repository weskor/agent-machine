package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cfg "github.com/weskor/pi-symphony/internal/config"
)

type fakeClient struct {
	apiKey   string
	endpoint string
}

func TestRunDispatchesRepresentativeArgCombinations(t *testing.T) {
	workflowPath := writeWorkflow(t, "")
	tests := []struct {
		name       string
		args       []string
		wantMode   string
		wantApply  bool
		wantCycles int
	}{
		{name: "default once", args: []string{workflowPath}, wantMode: modeOnce},
		{name: "explicit once", args: []string{"--once", workflowPath}, wantMode: modeOnce},
		{name: "status", args: []string{"--status", workflowPath}, wantMode: modeStatus},
		{name: "explain", args: []string{"--explain", workflowPath}, wantMode: modeExplain},
		{name: "dry run alias", args: []string{"--dry-run", workflowPath}, wantMode: modeExplain},
		{name: "continuous cycles", args: []string{"--continuous", "--cycles=3", workflowPath}, wantMode: modeContinuous, wantCycles: 3},
		{name: "daemon alias", args: []string{"--daemon", workflowPath}, wantMode: modeContinuous},
		{name: "merge approved", args: []string{"--merge-approved", workflowPath}, wantMode: modeMerge},
		{name: "repair artifacts", args: []string{"--repair-artifacts", workflowPath}, wantMode: modeRepair},
		{name: "cleanup apply", args: []string{"--cleanup-workspaces", "--apply", workflowPath}, wantMode: modeCleanup, wantApply: true},
		{name: "backfill", args: []string{"--backfill-state", workflowPath}, wantMode: modeBackfill},
		{name: "status wins over later merge approved", args: []string{"--status", "--merge-approved", workflowPath}, wantMode: modeStatus},
		{name: "repair wins over later status", args: []string{"--status", "--repair-artifacts", workflowPath}, wantMode: modeRepair},
		{name: "cleanup wins over later daemon", args: []string{"--daemon", "--cleanup-workspaces", workflowPath}, wantMode: modeCleanup},
		{name: "backfill wins over repair", args: []string{"--repair-artifacts", "--backfill-state", workflowPath}, wantMode: modeBackfill},
		{name: "explain wins over backfill", args: []string{"--backfill-state", "--explain", workflowPath}, wantMode: modeExplain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMode string
			var gotApply bool
			var gotCycles int
			err := Run(tt.args, testDeps(t, &gotMode, &gotApply, &gotCycles))
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if gotMode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", gotMode, tt.wantMode)
			}
			if gotApply != tt.wantApply {
				t.Fatalf("apply = %t, want %t", gotApply, tt.wantApply)
			}
			if gotCycles != tt.wantCycles {
				t.Fatalf("cycles = %d, want %d", gotCycles, tt.wantCycles)
			}
		})
	}
}

func TestRunValidatesConfigBeforeLinearClient(t *testing.T) {
	workflowPath := writeWorkflow(t, "workspace:\n  root: \"\"\n")
	calledClient := false
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}

	err := Run([]string{"--status", workflowPath}, deps)
	if err == nil || !strings.Contains(err.Error(), "WORKFLOW.md must configure tracker.project_slug and workspace.root") {
		t.Fatalf("Run() error = %v, want config validation error", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called before config validation")
	}
}

func TestRunValidatesLinearAPIKeyBeforeNetworkModes(t *testing.T) {
	workflowPath := writeWorkflow(t, "tracker:\n  api_key: \"\"\n")
	calledClient := false
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}

	err := Run([]string{"--status", workflowPath}, deps)
	if err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY is required") {
		t.Fatalf("Run() error = %v, want api key validation error", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called without API key")
	}
}

func TestRunExplainDispatchesNoMutatingOperations(t *testing.T) {
	workflowPath := writeWorkflow(t, "")
	deps := testDeps(t, nil, nil, nil)
	deps.BackfillStateFromArtifacts = func(string) (BackfillSummary, error) {
		t.Fatal("explain dispatched backfill mutation")
		return BackfillSummary{}, nil
	}
	deps.RepairArtifacts = func(string) error {
		t.Fatal("explain dispatched artifact repair mutation")
		return nil
	}
	deps.CleanupWorkspaces = func(string, CleanupOptions) error {
		t.Fatal("explain dispatched cleanup mutation")
		return nil
	}
	deps.MergeApprovedPRs = func(fakeClient, Config) error {
		t.Fatal("explain dispatched merge mutation")
		return nil
	}
	deps.RunContinuous = func(fakeClient, cfg.Workflow, Config, int) error {
		t.Fatal("explain dispatched continuous mutation")
		return nil
	}
	deps.RunOne = func(fakeClient, cfg.Workflow, Config) error {
		t.Fatal("explain dispatched run-one mutation")
		return nil
	}
	calledExplain := false
	deps.Explain = func(fakeClient, Config) error {
		calledExplain = true
		return nil
	}

	if err := Run([]string{"--dry-run", workflowPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !calledExplain {
		t.Fatal("explain dependency was not called")
	}
}

func TestRunUsesSchemaTrackerCredentials(t *testing.T) {
	workflowPath := writeWorkflow(t, "tracker:\n  endpoint: https://linear.test/graphql\n")
	var gotClient fakeClient
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		gotClient = fakeClient{apiKey: apiKey, endpoint: endpoint}
		return gotClient
	}

	if err := Run([]string{"--status", workflowPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotClient.apiKey != "test-linear-key" || gotClient.endpoint != "https://linear.test/graphql" {
		t.Fatalf("NewLinearClient credentials = %#v", gotClient)
	}
}

func TestLoadWorkflowConfigParsesDefaultsAndWorkflowValues(t *testing.T) {
	workflowPath := writeWorkflow(t, "")
	_, config, err := LoadWorkflowConfig(workflowPath)
	if err != nil {
		t.Fatalf("LoadWorkflowConfig() error = %v", err)
	}
	if config.WorkflowPath != workflowPath {
		t.Fatalf("workflow path not preserved")
	}
	if config.ProjectSlug != "CAG" || config.WorkspaceRoot != "/tmp/pi-symphony-test-workspaces" {
		t.Fatalf("config = %+v", config)
	}
	if config.APIKey != "test-linear-key" || config.Endpoint != "https://api.linear.app/graphql" {
		t.Fatalf("tracker credentials = %+v", config)
	}
	if config.RunningState != "In Progress" || config.HandoffState != "Human Review" || config.DoneState != "Done" || config.NeedsInfoState != "Needs Info" || config.ReadyState != "Ready for Agent" {
		t.Fatalf("unexpected states in config: %+v", config)
	}
	if !reflect.DeepEqual(config.ActiveStates, []string{"Ready for Agent", "In Progress"}) {
		t.Fatalf("ActiveStates = %#v", config.ActiveStates)
	}
}

func TestLoadWorkflowConfigParsesGitHubOwnership(t *testing.T) {
	workflowPath := writeWorkflow(t, "github:\n  app_slug: pi-symphony-bot\n  pr_author_override: other-bot[bot]\n")
	_, config, err := LoadWorkflowConfig(workflowPath)
	if err != nil {
		t.Fatalf("LoadWorkflowConfig() error = %v", err)
	}
	if config.GitHubAppSlug != "pi-symphony-bot" || config.GitHubPRAuthorOverride != "other-bot[bot]" {
		t.Fatalf("unexpected GitHub ownership config: %+v", config)
	}
}

func testDeps(t *testing.T, gotMode *string, gotApply *bool, gotCycles *int) Dependencies[fakeClient] {
	t.Helper()
	setMode := func(mode string) {
		if gotMode != nil {
			*gotMode = mode
		}
	}
	return Dependencies[fakeClient]{
		ConfigureGitHubRepositoryFromWorkflow: func(string) {},
		SetGitHubTimeout:                      func(cfg.Budget) {},
		NewLinearClient: func(apiKey, endpoint string) fakeClient {
			return fakeClient{apiKey: apiKey, endpoint: endpoint}
		},
		IssueIdentifiersByState: func(fakeClient, string, string) (map[string]bool, error) {
			return map[string]bool{"CAG-1": true}, nil
		},
		BackfillStateFromArtifacts: func(string) (BackfillSummary, error) {
			setMode(modeBackfill)
			return BackfillSummary{}, nil
		},
		RepairArtifacts: func(string) error {
			setMode(modeRepair)
			return nil
		},
		CleanupWorkspaces: func(_ string, options CleanupOptions) error {
			setMode(modeCleanup)
			if gotApply != nil {
				*gotApply = options.Apply
			}
			return nil
		},
		PrintStatus: func(fakeClient, Config) error {
			setMode(modeStatus)
			return nil
		},
		Explain: func(fakeClient, Config) error {
			setMode(modeExplain)
			return nil
		},
		MergeApprovedPRs: func(fakeClient, Config) error {
			setMode(modeMerge)
			return nil
		},
		RunContinuous: func(_ fakeClient, _ cfg.Workflow, _ Config, maxCycles int) error {
			setMode(modeContinuous)
			if gotCycles != nil {
				*gotCycles = maxCycles
			}
			return nil
		},
		RunOne: func(fakeClient, cfg.Workflow, Config) error {
			setMode(modeOnce)
			return nil
		},
	}
}

func writeWorkflow(t *testing.T, overrides string) string {
	t.Helper()
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	yaml := `tracker:
  project_slug: CAG
  api_key: test-linear-key
workspace:
  root: /tmp/pi-symphony-test-workspaces
active_states:
    - Ready for Agent
    - In Progress
pi:
  command: pi --print
`
	if overrides != "" {
		yaml = mergeSimpleWorkflowOverride(yaml, overrides)
	}
	content := "---\n" + yaml + "---\n# Workflow\n"
	if err := os.WriteFile(workflowPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return workflowPath
}

func mergeSimpleWorkflowOverride(base, overrides string) string {
	switch overrides {
	case "workspace:\n  root: \"\"\n":
		return strings.Replace(base, "  root: /tmp/pi-symphony-test-workspaces", "  root: \"\"", 1)
	case "tracker:\n  api_key: \"\"\n":
		return strings.Replace(base, "  api_key: test-linear-key", "  api_key: \"\"", 1)
	case "tracker:\n  endpoint: https://linear.test/graphql\n":
		return strings.Replace(base, "  api_key: test-linear-key", "  api_key: test-linear-key\n  endpoint: https://linear.test/graphql", 1)
	default:
		return base + overrides
	}
}
