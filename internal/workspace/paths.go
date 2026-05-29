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
	if info, err := os.Lstat(clean); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace root is a symlink: %s", clean)
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

// EnsureRoot creates a missing workspace root when it is safely creatable under
// an existing non-symlink ancestor, then returns the safe absolute root path.
func EnsureRoot(root string) (string, error) {
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
	if existing, err := SafeRoot(clean); err == nil {
		return existing, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	ancestor, err := nearestExistingSafeDir(clean)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", fmt.Errorf("create workspace root %s under existing ancestor %s: %w", clean, ancestor, err)
	}
	return SafeRoot(clean)
}

func nearestExistingSafeDir(path string) (string, error) {
	for current := filepath.Clean(path); current != string(filepath.Separator) && current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("%s does not exist and ancestor %s is a symlink", path, current)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%s does not exist and ancestor %s is not a directory", path, current)
			}
			return current, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("%s cannot be inspected through ancestor %s: %w", path, current, err)
		}
	}
	return string(filepath.Separator), nil
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

func EnsureIsolated(root, workspace, expectedBranch string) error {
	return EnsureIsolatedContext(context.Background(), root, workspace, expectedBranch)
}

func EnsureIsolatedContext(ctx context.Context, root, workspace, expectedBranch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := AssertSafeDeletePath(root, workspace); err != nil {
		return err
	}
	topLevel, err := sh.CaptureQuietContext(ctx, "git rev-parse --show-toplevel", workspace)
	if err != nil {
		return fmt.Errorf("workspace %s is not a git checkout: %w", workspace, err)
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if filepath.Clean(topAbs) != filepath.Clean(workspaceAbs) {
		return fmt.Errorf("refusing shared git checkout: top-level %s does not match workspace %s", strings.TrimSpace(topLevel), workspace)
	}
	current, err := CurrentGitBranchContext(ctx, workspace)
	if err != nil {
		return err
	}
	if current == expectedBranch {
		return nil
	}
	if current != "" && strings.HasPrefix(current, "am/") {
		return fmt.Errorf("workspace %s is on unexpected Agent Machine branch %q; expected %q", workspace, current, expectedBranch)
	}
	if err := sh.RunWithContext(ctx, "git switch -C "+sh.Quote(expectedBranch), workspace); err != nil {
		return err
	}
	return nil
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
	if strings.HasPrefix(path, ".am-debug/") || path == ".am-debug" {
		return true
	}
	switch path {
	case ".am-run.json", ".am-evaluation.json", ".am-prompt.md", ".am-review-prompt.md":
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
