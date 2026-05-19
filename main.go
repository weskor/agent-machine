package main

import (
	"fmt"
	"os"

	"github.com/weskor/pi-symphony/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], cliDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "[pi-symphony] %v\n", err)
		os.Exit(1)
	}
}
