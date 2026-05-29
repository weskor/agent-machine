package main

import (
	"fmt"
	"os"

	"github.com/weskor/agent-machine/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], cliDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "[am] %v\n", err)
		os.Exit(1)
	}
}

func log(format string, args ...any) {
	fmt.Printf("[am] "+format+"\n", args...)
}
