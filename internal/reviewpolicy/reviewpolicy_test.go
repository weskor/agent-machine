package reviewpolicy

import "testing"

func TestStatusUsesFirstExplicitMarker(t *testing.T) {
	output := "review prompt mentions REVIEW_PASS inline\nREVIEW_FAIL\nScope drift found."
	if got := Status(output); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
}

func TestStatusDetectsPassFailAndUnknown(t *testing.T) {
	if got := Status("REVIEW_PASS\nNo blockers. Historical note mentions REVIEW_FAIL."); got != "passed" {
		t.Fatalf("status = %q, want passed", got)
	}
	if got := Status("REVIEW_FAIL\nScope drift found."); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := Status("No explicit marker."); got != "unknown" {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestClassificationPassIsEmpty(t *testing.T) {
	if got := Classification("passed", "REVIEW_PASS"); got != "" {
		t.Fatalf("expected empty pass classification, got %q", got)
	}
}

func TestClassificationBehaviorSpecBlocker(t *testing.T) {
	output := "REVIEW_FAIL\nREVIEW_CLASSIFICATION: behavior_spec_blocker\nScope drift found."
	if got := Classification("failed", output); got != BehaviorSpecBlocker {
		t.Fatalf("classification = %q", got)
	}
}

func TestClassificationMissingEvidenceOnly(t *testing.T) {
	output := "REVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nPR body needs Behavior Contract Evidence."
	if got := Classification("failed", output); got != MissingEvidenceOnly {
		t.Fatalf("classification = %q", got)
	}
}

func TestClassificationUnknownWhenMissingOrMalformed(t *testing.T) {
	for _, output := range []string{"REVIEW_FAIL", "REVIEW_FAIL\nREVIEW_CLASSIFICATION: maybe"} {
		if got := Classification("failed", output); got != Unknown {
			t.Fatalf("classification = %q for %q", got, output)
		}
	}
}
