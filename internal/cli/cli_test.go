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
	configPath := writeConfig(t, "")
	tests := []struct {
		name       string
		args       []string
		wantMode   string
		wantApply  bool
		wantCycles int
	}{
		{name: "status", args: []string{"--status", configPath}, wantMode: modeStatus},
		{name: "run status", args: []string{"--run-status=CAG-119", configPath}, wantMode: modeRunStatus},
		{name: "run status command", args: []string{"run-status", "CAG-119", "--config", configPath}, wantMode: modeRunStatus},
		{name: "explain", args: []string{"--explain", configPath}, wantMode: modeExplain},
		{name: "surface snapshot", args: []string{"surface", "snapshot", "--config", configPath}, wantMode: modeSurface},
		{name: "snapshot alias", args: []string{"snapshot", "--config", configPath}, wantMode: modeSurface},
		{name: "surface snapshot flag", args: []string{"--surface-snapshot", configPath}, wantMode: modeSurface},
		{name: "dry run alias", args: []string{"--dry-run", configPath}, wantMode: modeExplain},
		{name: "continuous cycles", args: []string{"--continuous", "--cycles=3", configPath}, wantMode: modeContinuous, wantCycles: 3},
		{name: "daemon alias", args: []string{"--daemon", configPath}, wantMode: modeContinuous},
		{name: "merge approved", args: []string{"--merge-approved", configPath}, wantMode: modeMerge},
		{name: "repair artifacts", args: []string{"--repair-artifacts", configPath}, wantMode: modeRepair},
		{name: "repair worker task", args: []string{"--repair-worker-task=merge:CAG-1:7", configPath}, wantMode: modeRepairTask},
		{name: "cleanup apply", args: []string{"--cleanup-workspaces", "--apply", configPath}, wantMode: modeCleanup, wantApply: true},
		{name: "backfill", args: []string{"--backfill-state", configPath}, wantMode: modeBackfill},
		{name: "worker", args: []string{"--worker=status", configPath}, wantMode: modeWorker},
		{name: "status wins over later merge approved", args: []string{"--status", "--merge-approved", configPath}, wantMode: modeStatus},
		{name: "repair wins over later status", args: []string{"--status", "--repair-artifacts", configPath}, wantMode: modeRepair},
		{name: "cleanup wins over later daemon", args: []string{"--daemon", "--cleanup-workspaces", configPath}, wantMode: modeCleanup},
		{name: "backfill wins over repair", args: []string{"--repair-artifacts", "--backfill-state", configPath}, wantMode: modeBackfill},
		{name: "explain wins over backfill", args: []string{"--backfill-state", "--explain", configPath}, wantMode: modeExplain},
		{name: "backfill wins over run status", args: []string{"--run-status=CAG-119", "--backfill-state", configPath}, wantMode: modeBackfill},
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

func TestRunRejectsRemovedOnceMode(t *testing.T) {
	configPath := writeConfig(t, "")
	err := Run([]string{"--once", configPath}, testDeps(t, nil, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "--once has been removed") {
		t.Fatalf("Run() error = %v, want removed once mode error", err)
	}
}

func TestRunRejectsMissingModeWithoutLinearClient(t *testing.T) {
	configPath := writeConfig(t, "tracker:\n  api_key: \"\"\n")
	calledClient := false
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}

	err := Run([]string{configPath}, deps)
	if err == nil || !strings.Contains(err.Error(), "no CLI mode selected") {
		t.Fatalf("Run() error = %v, want missing mode error", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called for missing mode")
	}
}

func TestRunStatusDoesNotRequireLinearClient(t *testing.T) {
	configPath := writeConfig(t, "tracker:\n  api_key: \"\"\n")
	calledClient := false
	var gotRoot string
	var gotIssue string
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}
	deps.PrintRunProgress = func(workspaceRoot, issueIdentifier string) error {
		gotRoot = workspaceRoot
		gotIssue = issueIdentifier
		return nil
	}

	if err := Run([]string{"--run-status=CAG-119", configPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called for run progress status")
	}
	if gotRoot != "/tmp/pi-symphony-test-workspaces" || gotIssue != "CAG-119" {
		t.Fatalf("PrintRunProgress(%q, %q)", gotRoot, gotIssue)
	}
}

func TestSurfaceSnapshotDoesNotRequireLinearClient(t *testing.T) {
	configPath := writeConfig(t, "tracker:\n  api_key: \"\"\n")
	calledClient := false
	var gotConfig Config
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}
	deps.SurfaceSnapshot = func(config Config) error {
		gotConfig = config
		return nil
	}

	if err := Run([]string{"surface", "snapshot", "--config", configPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called for surface snapshot")
	}
	if gotConfig.WorkspaceRoot != "/tmp/pi-symphony-test-workspaces" || gotConfig.ProjectSlug != "CAG" {
		t.Fatalf("SurfaceSnapshot config = %+v", gotConfig)
	}
}

func TestRunValidatesConfigBeforeLinearClient(t *testing.T) {
	configPath := writeConfig(t, "workspace:\n  root: \"\"\n")
	calledClient := false
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}

	err := Run([]string{"--status", configPath}, deps)
	if err == nil || !strings.Contains(err.Error(), "symphony.yaml must configure tracker.project_slug and workspace.root") {
		t.Fatalf("Run() error = %v, want config validation error", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called before config validation")
	}
}

func TestRunValidatesLinearAPIKeyBeforeNetworkModes(t *testing.T) {
	configPath := writeConfig(t, "tracker:\n  api_key: \"\"\n")
	calledClient := false
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		calledClient = true
		return fakeClient{}
	}

	err := Run([]string{"--status", configPath}, deps)
	if err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY is required") {
		t.Fatalf("Run() error = %v, want api key validation error", err)
	}
	if calledClient {
		t.Fatal("NewLinearClient called without API key")
	}
}

func TestRunExplainDispatchesNoMutatingOperations(t *testing.T) {
	configPath := writeConfig(t, "")
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
	deps.RunContinuous = func(fakeClient, cfg.Project, Config, int) error {
		t.Fatal("explain dispatched continuous mutation")
		return nil
	}
	deps.RunWorker = func(fakeClient, cfg.Project, Config, string) error {
		t.Fatal("explain dispatched worker process")
		return nil
	}
	calledExplain := false
	deps.Explain = func(fakeClient, Config) error {
		calledExplain = true
		return nil
	}

	if err := Run([]string{"--dry-run", configPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !calledExplain {
		t.Fatal("explain dependency was not called")
	}
}

func TestRunUsesSchemaTrackerCredentials(t *testing.T) {
	configPath := writeConfig(t, "tracker:\n  endpoint: https://linear.test/graphql\n")
	var gotClient fakeClient
	deps := testDeps(t, nil, nil, nil)
	deps.NewLinearClient = func(apiKey, endpoint string) fakeClient {
		gotClient = fakeClient{apiKey: apiKey, endpoint: endpoint}
		return gotClient
	}

	if err := Run([]string{"--status", configPath}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotClient.apiKey != "test-linear-key" || gotClient.endpoint != "https://linear.test/graphql" {
		t.Fatalf("NewLinearClient credentials = %#v", gotClient)
	}
}

func TestLoadProjectConfigParsesDefaultsAndConfigValues(t *testing.T) {
	configPath := writeConfig(t, "")
	_, config, err := LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig() error = %v", err)
	}
	if config.ConfigPath != configPath {
		t.Fatalf("project path not preserved")
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
	if config.RuntimeProvider != "pi_cli" || config.RuntimeCommand != "pi --print" || config.PiCommand != "pi --print" {
		t.Fatalf("runtime config = provider %q runtime command %q pi command %q, want pi_cli/pi --print", config.RuntimeProvider, config.RuntimeCommand, config.PiCommand)
	}
	if config.ReviewGuidance != "" {
		t.Fatalf("ReviewGuidance = %q, want empty", config.ReviewGuidance)
	}
}

func TestLoadProjectConfigDefaultsToCodexWithoutLegacyPiCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "symphony.yaml")
	content := `
tracker:
  project_slug: CAG
  api_key: test-linear-key
workspace:
  root: /tmp/pi-symphony-test-workspaces
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "symphony.agent.md"), []byte("# Agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, config, err := LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig() error = %v", err)
	}
	if config.RuntimeProvider != "codex_cli" || !strings.Contains(config.RuntimeCommand, "codex") {
		t.Fatalf("runtime config = provider %q command %q, want default codex runtime", config.RuntimeProvider, config.RuntimeCommand)
	}
}

func TestLoadProjectConfigParsesRuntimeProvider(t *testing.T) {
	configPath := writeConfig(t, "runtime:\n  provider: codex_cli\n  command: codex exec\n  review_command: codex exec review\n")
	_, config, err := LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig() error = %v", err)
	}
	if config.RuntimeProvider != "codex_cli" || config.RuntimeCommand != "codex exec" || config.ReviewCommand != "codex exec review" {
		t.Fatalf("runtime config = provider %q command %q review %q, want codex_cli commands", config.RuntimeProvider, config.RuntimeCommand, config.ReviewCommand)
	}
}

func TestLoadProjectConfigParsesReviewGuidance(t *testing.T) {
	configPath := writeConfig(t, "review:\n  guidance: |\n    Check target repository docs.\n    Verify ownership boundaries.\n")
	_, config, err := LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig() error = %v", err)
	}
	want := "Check target repository docs.\nVerify ownership boundaries."
	if config.ReviewGuidance != want {
		t.Fatalf("ReviewGuidance = %q, want %q", config.ReviewGuidance, want)
	}
}

func TestLoadProjectConfigParsesGitHubOwnership(t *testing.T) {
	configPath := writeConfig(t, "github:\n  app_slug: pi-symphony-bot\n  pr_author_override: other-bot[bot]\n")
	_, config, err := LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("LoadProjectConfig() error = %v", err)
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
		ConfigureGitHubRepositoryFromConfig: func(string) {},
		SetGitHubTimeout:                    func(cfg.Budget) {},
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
		RepairWorkerTask: func(_, taskKey string) error {
			if taskKey != "" {
				setMode(modeRepairTask)
			}
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
		PrintRunProgress: func(string, string) error {
			setMode(modeRunStatus)
			return nil
		},
		Explain: func(fakeClient, Config) error {
			setMode(modeExplain)
			return nil
		},
		SurfaceSnapshot: func(Config) error {
			setMode(modeSurface)
			return nil
		},
		MergeApprovedPRs: func(fakeClient, Config) error {
			setMode(modeMerge)
			return nil
		},
		RunContinuous: func(_ fakeClient, _ cfg.Project, _ Config, maxCycles int) error {
			setMode(modeContinuous)
			if gotCycles != nil {
				*gotCycles = maxCycles
			}
			return nil
		},
		RunWorker: func(_ fakeClient, _ cfg.Project, _ Config, _ string) error {
			setMode(modeWorker)
			return nil
		},
	}
}

func writeConfig(t *testing.T, overrides string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "symphony.yaml")
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
		yaml = mergeSimpleConfigOverride(yaml, overrides)
	}
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "symphony.agent.md"), []byte("# Agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func mergeSimpleConfigOverride(base, overrides string) string {
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
