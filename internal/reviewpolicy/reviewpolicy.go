package reviewpolicy

import "strings"

const (
	PassMarker = "REVIEW_PASS"
	FailMarker = "REVIEW_FAIL"

	BehaviorSpecBlocker = "behavior_spec_blocker"
	MissingEvidenceOnly = "missing_evidence_only"
	Unknown             = "unknown"
)

func Classification(status, output string) string {
	if status == "passed" {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "REVIEW_CLASSIFICATION:") {
			value := strings.TrimSpace(trimmed[len("REVIEW_CLASSIFICATION:"):])
			switch value {
			case BehaviorSpecBlocker, MissingEvidenceOnly, Unknown:
				return value
			default:
				return Unknown
			}
		}
	}
	return Unknown
}

func Status(output string) string {
	for _, line := range strings.Split(output, "\n") {
		switch strings.TrimSpace(line) {
		case PassMarker:
			return "passed"
		case FailMarker:
			return "failed"
		}
	}
	return Unknown
}
