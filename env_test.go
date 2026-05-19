package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNearestDotEnvLocalResolvesGitHubAppKeyRelativeToEnvFile(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".symphony", "workspaces", "CAG-1")
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(workflowDir, "WORKFLOW.md")
	if err := os.WriteFile(workflowPath, []byte("---\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("GITHUB_APP_PRIVATE_KEY_PATH=keys/app.pem\nLINEAR_API_KEY=linear_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "../../stale-relative-key.pem")
	t.Setenv("LINEAR_API_KEY", "")

	loadNearestDotEnvLocal(workflowPath)

	want := filepath.Join(root, "keys", "app.pem")
	if got := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"); got != want {
		t.Fatalf("GITHUB_APP_PRIVATE_KEY_PATH = %q, want %q", got, want)
	}
	if got := os.Getenv("LINEAR_API_KEY"); got != "linear_test" {
		t.Fatalf("LINEAR_API_KEY = %q, want linear_test", got)
	}
}

func TestLoadNearestDotEnvLocalPrefersWorkflowGitHubAppEnv(t *testing.T) {
	root := t.TempDir()
	workflowPath := filepath.Join(root, "WORKFLOW.md")
	if err := os.WriteFile(workflowPath, []byte("---\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("GITHUB_APP_ID=workflow_app\nGITHUB_APP_INSTALLATION_ID=workflow_installation\nGITHUB_APP_PRIVATE_KEY_PATH=keys/workflow.pem\nLINEAR_API_KEY=workflow_linear\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_APP_ID", "stale_app")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "stale_installation")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "/tmp/stale.pem")
	t.Setenv("LINEAR_API_KEY", "operator_linear")

	loadNearestDotEnvLocal(workflowPath)

	if got := os.Getenv("GITHUB_APP_ID"); got != "workflow_app" {
		t.Fatalf("GITHUB_APP_ID = %q, want workflow_app", got)
	}
	if got := os.Getenv("GITHUB_APP_INSTALLATION_ID"); got != "workflow_installation" {
		t.Fatalf("GITHUB_APP_INSTALLATION_ID = %q, want workflow_installation", got)
	}
	if want, got := filepath.Join(root, "keys", "workflow.pem"), os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"); got != want {
		t.Fatalf("GITHUB_APP_PRIVATE_KEY_PATH = %q, want %q", got, want)
	}
	if got := os.Getenv("LINEAR_API_KEY"); got != "operator_linear" {
		t.Fatalf("LINEAR_API_KEY = %q, want existing non-GitHub-App env to remain", got)
	}
}
