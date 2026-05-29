package commenttext

import (
	"strings"
	"testing"
)

func TestSectionLinesExtractsNamedSection(t *testing.T) {
	description := "## Goal\n\nShip it.\n\n## Scope\n\n* Runner handoff code and tests.\n- Runner docs.\n\n## Out of Scope\n\n* Merge policy changes.\n"

	got := SectionLines(description, "scope")

	if len(got) != 2 || got[0] != "Runner handoff code and tests." || got[1] != "Runner docs." {
		t.Fatalf("SectionLines() = %#v", got)
	}
}

func TestSectionLinesDeduplicatesAndSanitizes(t *testing.T) {
	description := "## Scope\n\n* Duplicate `line`\n* Duplicate `line`\n* second\nline ignored\n\n## Risks\n\n* later\n"

	got := SectionLines(description, "scope")

	if len(got) != 3 || got[0] != "Duplicate 'line'" || got[1] != "second" || got[2] != "line ignored" {
		t.Fatalf("SectionLines() = %#v", got)
	}
}

func TestValidationLinesExtractsSafeCommandSummaries(t *testing.T) {
	text := "Validation:\n- bun run am:pi:test\n- git diff --check\n- unrelated prose"

	got := ValidationLines(text)

	if len(got) != 3 || got[0] != "Validation:" || got[1] != "bun run am:pi:test" || got[2] != "git diff --check" {
		t.Fatalf("ValidationLines() = %#v", got)
	}
}

func TestValidationLinesFiltersLongAndDuplicateLines(t *testing.T) {
	long := strings.Repeat("x", 181)
	text := strings.Join([]string{"go test ./...", "- go test ./...", long, "unrelated prose"}, "\n")

	got := ValidationLines(text)

	if len(got) != 1 || got[0] != "go test ./..." {
		t.Fatalf("ValidationLines() = %#v", got)
	}
}

func TestWriteBoundedBulletsLimitsAndSanitizes(t *testing.T) {
	var builder strings.Builder

	WriteBoundedBullets(&builder, []string{"one", "two\nlines", "three"}, "empty", 2)

	got := builder.String()
	for _, expected := range []string{"- one", "- two lines", "- …and 1 more."} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in %q", expected, got)
		}
	}
}

func TestWriteBoundedBulletsWritesEmptyFallback(t *testing.T) {
	var builder strings.Builder

	WriteBoundedBullets(&builder, nil, "nothing recorded", 2)

	if got := builder.String(); got != "- nothing recorded\n" {
		t.Fatalf("WriteBoundedBullets() = %q", got)
	}
}

func TestMarkdownLinkSanitizesLabelAndKeepsPlainEmptyTarget(t *testing.T) {
	if got := MarkdownLink("Title with\n`tick`", ""); got != "Title with 'tick'" {
		t.Fatalf("MarkdownLink(empty target) = %q", got)
	}
	if got := MarkdownLink("Title", "https://example.test/pr/1"); got != "[Title](https://example.test/pr/1)" {
		t.Fatalf("MarkdownLink(target) = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Fatalf("Truncate() = %q", got)
	}
	if got := Truncate("abcdef", 1); got != "…" {
		t.Fatalf("Truncate(limit 1) = %q", got)
	}
}
