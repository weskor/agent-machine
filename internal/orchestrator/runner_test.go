package orchestrator

import (
	"errors"
	"reflect"
	"testing"

	"github.com/weskor/pi-symphony/internal/cli"
	cfg "github.com/weskor/pi-symphony/internal/config"
)

type testClient struct{ id string }
type testConfig struct{ project string }

func TestCLIDependenciesAdaptsModeOperationsThroughRunnerFacade(t *testing.T) {
	cliConfig := cli.Config{ProjectSlug: "CAG"}
	calls := []string{}

	assertConfig := func(client testClient, config testConfig) {
		t.Helper()
		if client.id != "linear" {
			t.Fatalf("client id = %q, want linear", client.id)
		}
		if config.project != "CAG" {
			t.Fatalf("project = %q, want CAG", config.project)
		}
	}

	runner := NewRunner(SetupDependencies[testClient]{}, ModeOperationFuncs[testClient, testConfig]{
		BackfillFunc: func(root string) (cli.BackfillSummary, error) {
			if root != "workspace" {
				t.Fatalf("backfill root = %q, want workspace", root)
			}
			calls = append(calls, "backfill")
			return cli.BackfillSummary{}, nil
		},
		RepairFunc: func(root string) error {
			if root != "workspace" {
				t.Fatalf("repair root = %q, want workspace", root)
			}
			calls = append(calls, "repair")
			return nil
		},
		CleanupFunc: func(root string, options cli.CleanupOptions) error {
			if root != "workspace" {
				t.Fatalf("cleanup root = %q, want workspace", root)
			}
			if !options.Apply || !options.DoneIssues["CAG-1"] {
				t.Fatalf("cleanup options = %#v, want apply with done issue", options)
			}
			calls = append(calls, "cleanup")
			return nil
		},
		StatusFunc: func(client testClient, config testConfig) error {
			assertConfig(client, config)
			calls = append(calls, "status")
			return nil
		},
		RunStatusFunc: func(root, issueIdentifier string) error {
			if root != "workspace" || issueIdentifier != "CAG-1" {
				t.Fatalf("run status args = %q, %q; want workspace, CAG-1", root, issueIdentifier)
			}
			calls = append(calls, "run-status")
			return nil
		},
		ExplainFunc: func(client testClient, config testConfig) error {
			assertConfig(client, config)
			calls = append(calls, "explain")
			return nil
		},
		MergeFunc: func(client testClient, config testConfig) error {
			assertConfig(client, config)
			calls = append(calls, "merge")
			return nil
		},
		ContinuousFunc: func(client testClient, _ cfg.Workflow, config testConfig, maxCycles int) error {
			assertConfig(client, config)
			if maxCycles != 3 {
				t.Fatalf("maxCycles = %d, want 3", maxCycles)
			}
			calls = append(calls, "continuous")
			return nil
		},
		WorkerFunc: func(client testClient, _ cfg.Workflow, config testConfig, role string) error {
			assertConfig(client, config)
			if role != "status" {
				t.Fatalf("worker role = %q, want status", role)
			}
			calls = append(calls, "worker")
			return nil
		},
		RunOneFunc: func(client testClient, _ cfg.Workflow, config testConfig) (bool, error) {
			assertConfig(client, config)
			calls = append(calls, "run-one")
			return false, nil
		},
	}, func(config cli.Config) testConfig {
		return testConfig{project: config.ProjectSlug}
	})

	deps := runner.CLIDependencies()
	if _, err := deps.BackfillStateFromArtifacts("workspace"); err != nil {
		t.Fatalf("BackfillStateFromArtifacts returned error: %v", err)
	}
	if err := deps.RepairArtifacts("workspace"); err != nil {
		t.Fatalf("RepairArtifacts returned error: %v", err)
	}
	if err := deps.CleanupWorkspaces("workspace", cli.CleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-1": true}}); err != nil {
		t.Fatalf("CleanupWorkspaces returned error: %v", err)
	}
	if err := deps.PrintStatus(testClient{id: "linear"}, cliConfig); err != nil {
		t.Fatalf("PrintStatus returned error: %v", err)
	}
	if err := deps.PrintRunProgress("workspace", "CAG-1"); err != nil {
		t.Fatalf("PrintRunProgress returned error: %v", err)
	}
	if err := deps.Explain(testClient{id: "linear"}, cliConfig); err != nil {
		t.Fatalf("Explain returned error: %v", err)
	}
	if err := deps.MergeApprovedPRs(testClient{id: "linear"}, cliConfig); err != nil {
		t.Fatalf("MergeApprovedPRs returned error: %v", err)
	}
	if err := deps.RunContinuous(testClient{id: "linear"}, cfg.Workflow{}, cliConfig, 3); err != nil {
		t.Fatalf("RunContinuous returned error: %v", err)
	}
	if err := deps.RunWorker(testClient{id: "linear"}, cfg.Workflow{}, cliConfig, "status"); err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if err := deps.RunOne(testClient{id: "linear"}, cfg.Workflow{}, cliConfig); err != nil {
		t.Fatalf("RunOne returned error: %v", err)
	}

	wantCalls := []string{"backfill", "repair", "cleanup", "status", "run-status", "explain", "merge", "continuous", "worker", "run-one"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestCLIDependenciesPreservesRunOneErrorContract(t *testing.T) {
	runErr := errors.New("run failed")
	runner := NewRunner(SetupDependencies[testClient]{}, ModeOperationFuncs[testClient, testConfig]{
		RunOneFunc: func(testClient, cfg.Workflow, testConfig) (bool, error) {
			return true, runErr
		},
	}, func(cli.Config) testConfig { return testConfig{} })

	deps := runner.CLIDependencies()
	if err := deps.RunOne(testClient{}, cfg.Workflow{}, cli.Config{}); !errors.Is(err, runErr) {
		t.Fatalf("RunOne error = %v, want %v", err, runErr)
	}
}

func TestCLIDependenciesPassesContinuousMaxCycles(t *testing.T) {
	const wantCycles = 3
	runner := NewRunner(SetupDependencies[testClient]{}, ModeOperationFuncs[testClient, testConfig]{
		ContinuousFunc: func(_ testClient, _ cfg.Workflow, _ testConfig, maxCycles int) error {
			if maxCycles != wantCycles {
				t.Fatalf("maxCycles = %d, want %d", maxCycles, wantCycles)
			}
			return nil
		},
	}, func(cli.Config) testConfig { return testConfig{} })

	deps := runner.CLIDependencies()
	if err := deps.RunContinuous(testClient{}, cfg.Workflow{}, cli.Config{}, wantCycles); err != nil {
		t.Fatalf("RunContinuous returned error: %v", err)
	}
}
