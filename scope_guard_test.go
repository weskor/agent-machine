package main

import (
	"strings"
	"testing"
)

func TestEvaluateScopeGuardAllowsExactFilesAndGlobs(t *testing.T) {
	description := `## Scope

Allowed paths:

* ` + "`run_one.go`" + `
* ` + "`internal/state/*`" + `

Out of scope:

* ` + "`docs/product.md`" + `
`

	result := evaluateScopeGuard(description, []string{"run_one.go", "internal/state/store.go"})
	if result.Blocks() || !result.Checked {
		t.Fatalf("unexpected scope result: %+v", result)
	}
}

func TestEvaluateScopeGuardBlocksCAG76StyleScopeDrift(t *testing.T) {
	description := `## Scope

Allowed paths:

* ` + "`locks.go`" + `
* ` + "`cleanup.go`" + `
* ` + "`internal/workspace/*`" + `

Out of scope:

* ` + "`state_projection.go`" + `
`

	result := evaluateScopeGuard(description, []string{"state_projection.go", "internal/workspace/locks.go"})
	if !result.Blocks() {
		t.Fatalf("expected scope guard to block state_projection.go drift: %+v", result)
	}
	summary := result.Summary()
	for _, want := range []string{"state_projection.go", "Out of scope"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("scope summary missing %q: %s", want, summary)
		}
	}
}

func TestEvaluateScopeGuardWarnsWhenContractMissing(t *testing.T) {
	result := evaluateScopeGuard("## Scope\n\nRefactor carefully.", []string{"run_one.go"})
	if result.Blocks() || result.Checked || len(result.Warnings) == 0 {
		t.Fatalf("expected non-blocking missing-contract warning, got %+v", result)
	}
}

func TestEvaluateScopeGuardBlocksMixedAllowedAndBlockedDiff(t *testing.T) {
	description := `Allowed paths:
* docs/specs/*.md
* internal/state/*

Out-of-scope paths:
* secrets/*
`

	result := evaluateScopeGuard(description, []string{"docs/specs/harness-behavior.md", "secrets/local.env", "runner.go"})
	if !result.Blocks() {
		t.Fatalf("expected mixed diff to block: %+v", result)
	}
	summary := result.Summary()
	for _, want := range []string{"secrets/local.env", "runner.go"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("scope summary missing %q: %s", want, summary)
		}
	}
}
