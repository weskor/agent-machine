package ghapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

func TestConfigureGitHubAppCommitIdentity(t *testing.T) {
	workspace := t.TempDir()
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatalf("init git repo: %v", err)
	}

	identity := DefaultAppIdentity
	if err := ConfigureAppCommitIdentity(identity, workspace, time.Minute); err != nil {
		t.Fatalf("configure identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := sh.Run("git add file.txt && git commit -q -m test", workspace); err != nil {
		t.Fatalf("commit with configured identity: %v", err)
	}

	got, err := sh.CaptureQuiet("git log -1 --format='%an <%ae>|%cn <%ce>'", workspace)
	if err != nil {
		t.Fatalf("read commit identity: %v", err)
	}
	want := identity.BotName() + " <" + identity.BotEmail() + ">|" + identity.BotName() + " <" + identity.BotEmail() + ">"
	if strings.TrimSpace(got) != want {
		t.Fatalf("commit identity = %q, want %q", strings.TrimSpace(got), want)
	}
}

func TestConfigureGitHubAppCommitIdentityUsesConfiguredSlug(t *testing.T) {
	workspace := t.TempDir()
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatalf("init git repo: %v", err)
	}
	identity, ok := NewAppIdentity("custom-bot", "config github.app_slug")
	if !ok {
		t.Fatal("expected custom app identity")
	}

	if err := ConfigureAppCommitIdentity(identity, workspace, time.Minute); err != nil {
		t.Fatalf("configure identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := sh.Run("git add file.txt && git commit -q -m test", workspace); err != nil {
		t.Fatalf("commit with configured identity: %v", err)
	}

	got, err := sh.CaptureQuiet("git log -1 --format='%an <%ae>|%cn <%ce>'", workspace)
	if err != nil {
		t.Fatalf("read commit identity: %v", err)
	}
	want := "custom-bot[bot] <custom-bot[bot]@users.noreply.github.com>|custom-bot[bot] <custom-bot[bot]@users.noreply.github.com>"
	if strings.TrimSpace(got) != want {
		t.Fatalf("commit identity = %q, want %q", strings.TrimSpace(got), want)
	}
}
