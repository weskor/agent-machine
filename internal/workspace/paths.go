package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sh "github.com/weskor/agent-machine/internal/shell"
)

// SafeRoot returns an absolute, existing workspace root that is safe to use for
// workspace lifecycle operations.
func SafeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) || clean == filepath.Dir(clean) {
		return "", fmt.Errorf("unsafe workspace root %q", root)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory: %s", clean)
	}
	return clean, nil
}

// SafePath resolves a single workspace name under root and rejects traversal,
// hidden names, symlinks, and paths outside the workspace root.
func SafePath(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) || strings.Contains(name, string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe workspace name %q", name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("unsafe hidden workspace name %q", name)
	}
	safeRoot, err := SafeRoot(root)
	if err != nil {
		return "", err
	}
	workspace := filepath.Clean(filepath.Join(safeRoot, name))
	if err := AssertSafeDeletePath(safeRoot, workspace); err != nil {
		return "", err
	}
	return workspace, nil
}

// AssertSafeDeletePath verifies workspace is an immediate non-symlink child of root.
func AssertSafeDeletePath(root, workspace string) error {
	safeRoot, err := SafeRoot(root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if abs == safeRoot || filepath.Dir(abs) != safeRoot {
		return fmt.Errorf("refusing unsafe workspace path %q outside root %q", workspace, safeRoot)
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink workspace path %q", workspace)
	}
	return nil
}

func CurrentGitBranch(workspace string) (string, error) {
	return CurrentGitBranchContext(context.Background(), workspace)
}

func CurrentGitBranchContext(ctx context.Context, workspace string) (string, error) {
	output, err := sh.CaptureQuietContext(ctx, "git branch --show-current", workspace)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func HasChanges(workspace string) (bool, error) {
	output, err := sh.CaptureQuiet("git status --porcelain", workspace)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "??"))
		if IsIgnoredEvidencePath(workspace, path) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// IsIgnoredEvidencePath reports whether a git-status path is runner/operator
// evidence that must not make an otherwise clean workspace look dirty.
// It intentionally ignores only exact, bounded artifact paths. The top-level
// "false" marker is a known external subagent output=false scratch artifact;
// only zero-byte files or bounded reviewer-output scratch files match. Other
// non-empty files, nested files, and symlinks remain dirty.
func IsIgnoredEvidencePath(workspace, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, ".pi-symphony-debug/") || path == ".pi-symphony-debug" {
		return true
	}
	switch path {
	case ".pi-symphony-run.json", ".pi-symphony-evaluation.json", ".pi-symphony-prompt.md", ".pi-symphony-review-prompt.md":
		return true
	case "false":
		return isSubagentFalseScratch(filepath.Join(workspace, path))
	default:
		return false
	}
}

func isSubagentFalseScratch(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Size() == 0 {
		return true
	}
	if info.Size() > 16*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := strings.TrimSpace(string(data))
	if !(strings.HasPrefix(content, "PASS") || strings.HasPrefix(content, "BLOCK")) {
		return false
	}
	return strings.Contains(content, "Did not write ") && strings.Contains(content, "/false")
}
