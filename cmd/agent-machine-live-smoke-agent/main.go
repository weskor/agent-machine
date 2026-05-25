package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/weskor/agent-machine/internal/livesmoke"
)

func main() {
	role := flag.String("role", "implementation", "implementation or review")
	_ = flag.String("thinking", "", "ignored compatibility flag appended by review command handling")
	flag.Parse()
	if *role == "review" {
		fmt.Println("REVIEW_PASS")
		fmt.Println("Findings: fake live smoke review passed for the scoped disposable diff.")
		return
	}
	promptPath, err := livesmoke.PromptPath(flag.Args())
	if err != nil {
		fatal(err)
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		fatal(err)
	}
	path, err := livesmoke.AllowedPathFromPrompt(string(prompt))
	if err != nil {
		fatal(err)
	}
	identifier := livesmoke.IssueIdentifierFromPrompt(string(prompt))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, []byte(livesmoke.SmokeMarkerContent(identifier, path)), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("LIVE_SMOKE_FAKE_AGENT wrote %s for %s\n", path, identifier)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "live smoke fake agent: %v\n", err)
	os.Exit(1)
}
