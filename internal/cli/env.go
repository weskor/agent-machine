package cli

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
