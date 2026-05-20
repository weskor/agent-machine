package workspace

import (
	"os"
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
