package config

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

type Workflow struct {
	YAML string
	Body string
}

func ReadWorkflow(path string) (Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Workflow{}, err
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return Workflow{}, errors.New("WORKFLOW.md must start with YAML front matter")
	}
	end := strings.Index(text[4:], "\n---")
	if end == -1 {
		return Workflow{}, errors.New("WORKFLOW.md front matter is not closed")
	}
	end += 4
	return Workflow{YAML: strings.TrimSpace(text[4:end]), Body: strings.TrimSpace(text[end+4:])}, nil
}

func Scalar(yaml, key, fallback string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.+)$`)
	match := re.FindStringSubmatch(yaml)
	if len(match) < 2 {
		return fallback
	}
	value := strings.Trim(strings.TrimSpace(match[1]), `"'`)
	if value == "" || value == "null" {
		return fallback
	}
	if strings.HasPrefix(value, "$.") {
		return fallback
	}
	if strings.HasPrefix(value, "$") {
		if env := os.Getenv(strings.TrimPrefix(value, "$")); env != "" {
			return env
		}
		return fallback
	}
	return value
}

func BaseBranchFromWorkflow(yaml string) string {
	return Scalar(Section(yaml, "workspace"), "  base_branch", "develop")
}

func Section(yaml, name string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == name+":" {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	var out []string
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func ListUnder(yaml, key string) []string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == key+":" {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return nil
	}
	var values []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(line, "    ") || !strings.HasPrefix(trimmed, "- ") {
			break
		}
		values = append(values, strings.Trim(strings.TrimPrefix(trimmed, "- "), `"'`))
	}
	return values
}

func CommandUnder(yaml, key, fallback string) string {
	inline := Scalar(yaml, "  "+key, "")
	if inline != "" && inline != ">-" && inline != "|" {
		return inline
	}
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == key+": >-" || strings.TrimSpace(line) == key+": |" {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return fallback
	}
	var parts []string
	for _, line := range lines[start:] {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func BlockUnder(yaml, key string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == key+": |" {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	var parts []string
	for _, line := range lines[start:] {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		parts = append(parts, strings.TrimPrefix(line, "    "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
