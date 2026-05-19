package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadWorkflowSplitsFrontMatterAndBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := "---\ntitle: Test workflow\nstate: ready\n---\n\n# Body\n\nRun the worker.\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	wf, err := readWorkflow(path)
	if err != nil {
		t.Fatalf("readWorkflow returned error: %v", err)
	}
	if wf.YAML != "title: Test workflow\nstate: ready" {
		t.Fatalf("unexpected YAML: %q", wf.YAML)
	}
	if wf.Body != "# Body\n\nRun the worker." {
		t.Fatalf("unexpected body: %q", wf.Body)
	}
}

func TestReadWorkflowRequiresFrontMatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(path, []byte("# Missing front matter\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readWorkflow(path); err == nil {
		t.Fatal("expected missing front matter to return an error")
	}
}

func TestScalarHandlesFallbacksQuotesAndEnvironment(t *testing.T) {
	t.Setenv("SYMPHONY_LABEL", "from-env")
	yaml := "" +
		"title: 'Quoted value'\n" +
		"empty: \"\"\n" +
		"nullish: null\n" +
		"json_path: $.issue.identifier\n" +
		"env_value: $SYMPHONY_LABEL\n" +
		"missing_env: $DOES_NOT_EXIST\n"

	tests := []struct {
		name     string
		key      string
		fallback string
		want     string
	}{
		{name: "quoted", key: "title", fallback: "fallback", want: "Quoted value"},
		{name: "missing", key: "missing", fallback: "fallback", want: "fallback"},
		{name: "empty", key: "empty", fallback: "fallback", want: "fallback"},
		{name: "null", key: "nullish", fallback: "fallback", want: "fallback"},
		{name: "json path", key: "json_path", fallback: "fallback", want: "fallback"},
		{name: "environment", key: "env_value", fallback: "fallback", want: "from-env"},
		{name: "missing environment", key: "missing_env", fallback: "fallback", want: "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scalar(yaml, tt.key, tt.fallback); got != tt.want {
				t.Fatalf("scalar(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestSectionReturnsIndentedYamlBlock(t *testing.T) {
	yaml := "" +
		"review:\n" +
		"  prompt: |\n" +
		"    Check the diff.\n" +
		"  command: bun run check\n" +
		"next: value\n"

	want := "  prompt: |\n    Check the diff.\n  command: bun run check"
	if got := section(yaml, "review"); got != want {
		t.Fatalf("section returned %q, want %q", got, want)
	}
	if got := section(yaml, "missing"); got != "" {
		t.Fatalf("missing section returned %q, want empty string", got)
	}
}

func TestListUnderReturnsQuotedItemsUntilSectionEnds(t *testing.T) {
	yaml := "" +
		"tasks:\n" +
		"    - 'first item'\n" +
		"    - \"second item\"\n" +
		"other:\n" +
		"    - ignored\n"

	want := []string{"first item", "second item"}
	if got := listUnder(yaml, "tasks"); !reflect.DeepEqual(got, want) {
		t.Fatalf("listUnder returned %#v, want %#v", got, want)
	}
	if got := listUnder(yaml, "missing"); got != nil {
		t.Fatalf("missing list returned %#v, want nil", got)
	}
}

func TestCommandUnderSupportsInlineAndFoldedCommands(t *testing.T) {
	yaml := "" +
		"steps:\n" +
		"  inline: bun run check\n" +
		"  folded: >-\n" +
		"    bun run symphony:pi:test\n" +
		"    -- --run TestWorkflow\n" +
		"  literal: |\n" +
		"    git diff --check\n"

	if got := commandUnder(yaml, "inline", "fallback"); got != "bun run check" {
		t.Fatalf("inline command = %q, want bun run check", got)
	}
	if got := commandUnder(yaml, "folded", "fallback"); got != "bun run symphony:pi:test -- --run TestWorkflow" {
		t.Fatalf("folded command = %q", got)
	}
	if got := commandUnder(yaml, "literal", "fallback"); got != "git diff --check" {
		t.Fatalf("literal command = %q", got)
	}
	if got := commandUnder(yaml, "missing", "fallback"); got != "fallback" {
		t.Fatalf("missing command = %q, want fallback", got)
	}
}

func TestBlockUnderReturnsDedentedLiteralBlock(t *testing.T) {
	yaml := "" +
		"prompt: |\n" +
		"    First line.\n" +
		"    Second line.\n" +
		"next: value\n"

	want := "First line.\nSecond line."
	if got := blockUnder(yaml, "prompt"); got != want {
		t.Fatalf("blockUnder returned %q, want %q", got, want)
	}
	if got := blockUnder(yaml, "missing"); got != "" {
		t.Fatalf("missing block returned %q, want empty string", got)
	}
}
