package orchestrator

import (
	"errors"
	"testing"

	"github.com/weskor/pi-symphony/internal/cli"
	cfg "github.com/weskor/pi-symphony/internal/config"
)

type testClient struct{ id string }
type testConfig struct{ project string }

func TestCLIDependenciesAdaptsModeOperationsThroughRunnerFacade(t *testing.T) {
	cliConfig := cli.Config{ProjectSlug: "CAG"}
	runner := Runner[testClient, testConfig]{
		PrintStatus: func(client testClient, config testConfig) error {
			if client.id != "linear" {
				t.Fatalf("client id = %q, want linear", client.id)
			}
			if config.project != "CAG" {
				t.Fatalf("project = %q, want CAG", config.project)
			}
			return nil
		},
		FromCLIConfig: func(config cli.Config) testConfig {
			return testConfig{project: config.ProjectSlug}
		},
	}

	deps := runner.CLIDependencies()
	if err := deps.PrintStatus(testClient{id: "linear"}, cliConfig); err != nil {
		t.Fatalf("PrintStatus returned error: %v", err)
	}
}

func TestCLIDependenciesPreservesRunOneErrorContract(t *testing.T) {
	runErr := errors.New("run failed")
	runner := Runner[testClient, testConfig]{
		RunOne: func(testClient, cfg.Workflow, testConfig) (bool, error) {
			return true, runErr
		},
		FromCLIConfig: func(cli.Config) testConfig { return testConfig{} },
	}

	deps := runner.CLIDependencies()
	if err := deps.RunOne(testClient{}, cfg.Workflow{}, cli.Config{}); !errors.Is(err, runErr) {
		t.Fatalf("RunOne error = %v, want %v", err, runErr)
	}
}

func TestCLIDependenciesPassesContinuousMaxCycles(t *testing.T) {
	const wantCycles = 3
	runner := Runner[testClient, testConfig]{
		RunContinuous: func(_ testClient, _ cfg.Workflow, _ testConfig, maxCycles int) error {
			if maxCycles != wantCycles {
				t.Fatalf("maxCycles = %d, want %d", maxCycles, wantCycles)
			}
			return nil
		},
		FromCLIConfig: func(cli.Config) testConfig { return testConfig{} },
	}

	deps := runner.CLIDependencies()
	if err := deps.RunContinuous(testClient{}, cfg.Workflow{}, cli.Config{}, wantCycles); err != nil {
		t.Fatalf("RunContinuous returned error: %v", err)
	}
}
