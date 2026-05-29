package commenttext

import (
	"fmt"
	"strings"
)

func SectionLines(description string, names ...string) []string {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[normalizeSectionName(name)] = true
	}
	inSection := false
	var lines []string
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inSection && len(lines) > 0 {
				break
			}
			continue
		}
		if name, ok := sectionHeading(trimmed); ok {
			if inSection && len(lines) > 0 {
				break
			}
			inSection = wanted[normalizeSectionName(name)]
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") {
			lines = append(lines, SanitizeLine(strings.TrimSpace(strings.TrimLeft(trimmed, "-* "))))
			continue
		}
		lines = append(lines, SanitizeLine(trimmed))
	}
	return Unique(lines)
}

func ValidationLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		clean := SanitizeLine(strings.Trim(line, " -•\t`"))
		lower := strings.ToLower(clean)
		if clean == "" || len(clean) > 180 {
			continue
		}
		if strings.Contains(lower, "bun run ") || strings.Contains(lower, "git diff --check") || strings.Contains(lower, "go test") || strings.Contains(lower, "validation") {
			lines = append(lines, clean)
		}
	}
	return Unique(lines)
}

func WriteBoundedBullets(builder *strings.Builder, values []string, empty string, limit int) {
	if len(values) == 0 {
		fmt.Fprintf(builder, "- %s\n", empty)
		return
	}
	for i, value := range values {
		if i >= limit {
			fmt.Fprintf(builder, "- …and %d more.\n", len(values)-limit)
			return
		}
		fmt.Fprintf(builder, "- %s\n", SanitizeLine(value))
	}
}

func MarkdownLink(label, target string) string {
	label = SanitizeLine(label)
	target = strings.TrimSpace(target)
	if target == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, target)
}

func SanitizeLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "`", "'")
	value = strings.Join(strings.Fields(value), " ")
	return Truncate(value, 240)
}

func Truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	return strings.TrimSpace(value[:limit-1]) + "…"
}

func Unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sectionHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimLeft(trimmed, "#")
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
	if trimmed == "" || len(trimmed) > 80 {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "goal", "scope", "requirements", "acceptance criteria", "validation", "out of scope", "out-of-scope", "out of scope paths", "out-of-scope paths", "risks":
		return lower, true
	default:
		return "", false
	}
}

func normalizeSectionName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(name, "#: ")))
}
