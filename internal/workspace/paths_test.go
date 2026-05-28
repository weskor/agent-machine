package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafePathRejectsTraversalTable(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, workspaceName, want string }{
		{name: "parent traversal", workspaceName: "..", want: "unsafe hidden workspace name"},
		{name: "nested traversal", workspaceName: filepath.Join("..", "CAG-1"), want: "unsafe workspace name"},
		{name: "hidden", workspaceName: ".hidden", want: "unsafe hidden workspace name"},
		{name: "empty", workspaceName: "", want: "unsafe workspace name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SafePath(root, tc.workspaceName)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SafePath error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAssertSafeDeletePathRejectsUnsafeTargetsTable(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, tc := range []struct{ name, target, want string }{
		{name: "root", target: root, want: "refusing unsafe workspace path"},
		{name: "outside", target: filepath.Join(outside, "CAG-1"), want: "refusing unsafe workspace path"},
		{name: "symlink", target: symlink, want: "refusing symlink workspace path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertSafeDeletePath(root, tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AssertSafeDeletePath error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestEnsureRootCreatesMissingRootUnderExistingAncestor(t *testing.T) {
	parent := filepath.Join(t.TempDir(), ".am")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspaces")

	got, err := EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("EnsureRoot() = %q, want %q", got, root)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("created workspace root is not a directory: %s", root)
	}
}

func TestEnsureRootRejectsUnsafeRoots(t *testing.T) {
	dir := t.TempDir()
	fileRoot := filepath.Join(dir, "workspaces")
	if err := os.WriteFile(fileRoot, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRoot(fileRoot); err == nil || !strings.Contains(err.Error(), "workspace root is not a directory") {
		t.Fatalf("EnsureRoot(file) error = %v, want non-directory rejection", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(dir, "linked-workspaces")
	if err := os.Symlink(target, linkRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := EnsureRoot(linkRoot); err == nil || !strings.Contains(err.Error(), "workspace root is a symlink") {
		t.Fatalf("EnsureRoot(symlink) error = %v, want symlink rejection", err)
	}
}

func TestHasChangesIgnoresFalseScratchMarkers(t *testing.T) {
	workspace := initGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "false"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := HasChanges(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("zero-byte top-level false marker should be ignored")
	}

	reviewerScratch := "PASS\n\n## Review\n- Looks good.\n\nDid not write /tmp/workspace/false because the task also says “Do not modify files.”\n"
	if err := os.WriteFile(filepath.Join(workspace, "false"), []byte(reviewerScratch), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = HasChanges(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("bounded subagent reviewer false scratch output should be ignored")
	}

	if err := os.WriteFile(filepath.Join(workspace, "false"), []byte("real change"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = HasChanges(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("non-empty false file must remain dirty")
	}
}

func TestHasChangesTreatsNestedFalseMarkerAsDirty(t *testing.T) {
	workspace := initGitWorkspace(t)
	nested := filepath.Join(workspace, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "false"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := HasChanges(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("nested false file must remain dirty")
	}
}

func TestHasChangesTreatsFalseSymlinkAsDirty(t *testing.T) {
	workspace := initGitWorkspace(t)
	target := filepath.Join(workspace, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "false")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	changed, err := HasChanges(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("false symlink must remain dirty")
	}
}

func initGitWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = workspace
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return workspace
}
