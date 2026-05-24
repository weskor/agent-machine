package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvLocalResolvesGitHubAppKeyRelativeToEnvFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(envPath, []byte("GITHUB_APP_PRIVATE_KEY_PATH=keys/app.pem\nLINEAR_API_KEY=linear_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
	t.Setenv("LINEAR_API_KEY", "")

	loadDotEnvLocal(envPath)

	want := filepath.Join(root, "keys", "app.pem")
	if got := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"); got != want {
		t.Fatalf("GITHUB_APP_PRIVATE_KEY_PATH = %q, want %q", got, want)
	}
	if got := os.Getenv("LINEAR_API_KEY"); got != "linear_test" {
		t.Fatalf("LINEAR_API_KEY = %q, want linear_test", got)
	}
}

func TestLoadDotEnvLocalDoesNotOverrideProcessEnv(t *testing.T) {
	root := t.TempDir()
	envPath := filepath.Join(root, ".env.local")
	if err := os.WriteFile(envPath, []byte("GITHUB_APP_ID=file_app\nLINEAR_API_KEY=file_linear\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_APP_ID", "process_app")
	t.Setenv("LINEAR_API_KEY", "process_linear")

	loadDotEnvLocal(envPath)

	if got := os.Getenv("GITHUB_APP_ID"); got != "process_app" {
		t.Fatalf("GITHUB_APP_ID = %q, want process_app", got)
	}
	if got := os.Getenv("LINEAR_API_KEY"); got != "process_linear" {
		t.Fatalf("LINEAR_API_KEY = %q, want process_linear", got)
	}
}
