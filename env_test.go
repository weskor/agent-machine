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

	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
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
