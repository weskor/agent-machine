package main

import (
	"fmt"
	"os"
	"time"
)

var defaultGitHubCommandTimeout = 2 * time.Minute

func isEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func log(format string, args ...any) {
	fmt.Printf("[pi-symphony] "+format+"\n", args...)
}
