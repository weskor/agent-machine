package ghapi

import "strings"

func EmptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UNKNOWN"
	}
	return value
}

func IssueIdentifierFromBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "symphony/")
	branch = strings.TrimSuffix(branch, "-workspace")
	return strings.TrimSpace(branch)
}
