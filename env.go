package main

import (
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnvLocal is a local-operator convenience for the pilot. It fills missing
// env vars without printing or persisting secret values.
func loadDotEnvLocal(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	baseDir := filepath.Dir(path)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key == "GITHUB_APP_PRIVATE_KEY_PATH" && value != "" && !filepath.IsAbs(value) {
			value = filepath.Clean(filepath.Join(baseDir, value))
		}
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func loadNearestDotEnvLocal(path string) {
	dir := path
	if filepath.Base(path) != ".env.local" {
		dir = filepath.Dir(path)
	}
	absDir, err := filepath.Abs(dir)
	if err == nil {
		dir = absDir
	}
	for {
		candidate := filepath.Join(dir, ".env.local")
		if _, err := os.Stat(candidate); err == nil {
			loadDotEnvLocal(candidate)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
