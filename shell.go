package main

import (
	"fmt"
	"time"
)

var defaultGitHubCommandTimeout = 2 * time.Minute

func log(format string, args ...any) {
	fmt.Printf("[pi-symphony] "+format+"\n", args...)
}
