package handoffcomment

import (
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/commenttext"
	"github.com/weskor/agent-machine/internal/runclassification"
)

type Summary struct {
	IssueIdentifier      string
	IssueTitle           string
	IssueURL             string
	IssueDescription     string
	PRURL                string
	RuntimeUsage         string
	Review               string
	ReviewStatus         string
	ReviewClassification string
	Duration             time.Duration
	Validation           []string
	ScopeGuardSummary    string
	ScopeGuardChecked    bool
	FollowUps            []string
	Classification       *runclassification.Classification
	PRDetails            *PRDetails
	Progress             *Progress
}

type PRDetails struct {
	ChangedFiles int
	Additions    int
	Deletions    int
}

type Progress struct {
	Phase        string
	Status       string
	NextAction   string
	PRURL        string
	ProgressPath string
}

func RenderPRBody(summary Summary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "## Agent Machine handoff\n\n")
	fmt.Fprintf(&builder, "- Issue: %s — %s\n", commenttext.MarkdownLink(summary.IssueIdentifier, summary.IssueURL), commenttext.SanitizeLine(summary.IssueTitle))
	fmt.Fprintf(&builder, "- PR: %s\n", commenttext.MarkdownLink(summary.PRURL, summary.PRURL))
	fmt.Fprintf(&builder, "- Review: %s\n", commenttext.SanitizeLine(summary.Review))
	fmt.Fprintf(&builder, "- Usage: %s\n", commenttext.SanitizeLine(summary.RuntimeUsage))
	fmt.Fprintf(&builder, "- Duration: %s\n\n", summary.Duration.Round(time.Second))

	builder.WriteString("### Issue scope\n")
	commenttext.WriteBoundedBullets(&builder, issueScopeNotes(summary), "Issue scope summary not recorded.", 6)

	builder.WriteString("### Validation\n")
	commenttext.WriteBoundedBullets(&builder, summary.Validation, "No validation commands detected in runner output.", 5)

	builder.WriteString("\n### Tests and characterization\n")
	commenttext.WriteBoundedBullets(&builder, testEvidenceNotes(summary), "No test or characterization evidence detected in runner output.", 5)

	builder.WriteString("\n### Behavior Contract Evidence\n")
	commenttext.WriteBoundedBullets(&builder, behaviorContractEvidenceNotes(summary), "No behavior-contract evidence recorded.", 8)

	builder.WriteString("\n### Changed files\n")
	commenttext.WriteBoundedBullets(&builder, changedFilesNotes(summary), "Changed file summary not recorded.", 3)

	builder.WriteString("\n### Risks and out of scope\n")
	commenttext.WriteBoundedBullets(&builder, riskAndScopeNotes(summary), "No known risks or out-of-scope follow-up recorded.", 5)

	builder.WriteString("\n### Progress status\n")
	commenttext.WriteBoundedBullets(&builder, progressStatusNotes(summary), "Progress snapshot not recorded.", 4)

	builder.WriteString("\n### Remaining follow-up\n")
	commenttext.WriteBoundedBullets(&builder, summary.FollowUps, "No follow-up recorded.", 4)

	return commenttext.Truncate(builder.String(), 12000)
}

func RenderLinearComment(summary Summary) string {
	return commenttext.Truncate(fmt.Sprintf("Runtime run completed.\n\nPR: %s\nUsage: %s\nReview: %s\nDuration: %s", summary.PRURL, summary.RuntimeUsage, summary.Review, summary.Duration.Round(time.Second)), 1000)
}

func behaviorContractEvidenceNotes(summary Summary) []string {
	reviewClassification := summary.ReviewClassification
	if strings.TrimSpace(reviewClassification) == "" {
		reviewClassification = "not recorded"
	}
	notes := []string{
		"References: docs/specs/end-to-end-orchestration.md, docs/specs/harness-behavior.md, and docs/agents/review-policy.md.",
		"Behavior inventory: runner-owned PR identity, branch/base validation, review classification, Linear handoff comments/state movement, and run/evaluation artifacts.",
		"Preserved behavior: implementation agents still do not create, update, push, or comment on code-host PRs directly.",
		"Handoff evidence source: runner-owned PR body; separate code-host PR summary comments are not used.",
		"Spec compatibility: observable behavior is preserved unless the issue and docs/specs explicitly describe a change.",
		"Complexity/LOC budget: changed-file counts and additions/deletions are recorded below when reported by the code host.",
		"Review classification: " + reviewClassification,
	}
	if summary.Classification != nil {
		notes = append(notes, "Run classification: outcome="+summary.Classification.Outcome+", root="+emptyAsNA(summary.Classification.RootCause)+", next="+emptyAsNA(summary.Classification.NextAction)+".")
	}
	return notes
}

func issueScopeNotes(summary Summary) []string {
	var notes []string
	for _, line := range commenttext.SectionLines(summary.IssueDescription, "scope") {
		notes = append(notes, "Scope: "+line)
	}
	scopeSummary := strings.TrimSpace(summary.ScopeGuardSummary)
	if scopeSummary != "" {
		notes = append(notes, "Scope guard: "+scopeSummary)
	} else if summary.ScopeGuardChecked {
		notes = append(notes, "Scope guard: changed files matched the Linear ticket path contract.")
	}
	if len(notes) == 0 && strings.TrimSpace(summary.IssueIdentifier) != "" {
		notes = append(notes, "Issue identifier: "+summary.IssueIdentifier+".")
	}
	return commenttext.Unique(notes)
}

func testEvidenceNotes(summary Summary) []string {
	var notes []string
	for _, line := range summary.Validation {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "test") || strings.Contains(lower, "characterization") || strings.Contains(lower, "make ci") || strings.Contains(lower, "go test") {
			notes = append(notes, line)
		}
	}
	return commenttext.Unique(notes)
}

func changedFilesNotes(summary Summary) []string {
	if summary.PRDetails == nil {
		return nil
	}
	details := summary.PRDetails
	return []string{
		fmt.Sprintf("Files changed: %d; additions: %d; deletions: %d.", details.ChangedFiles, details.Additions, details.Deletions),
	}
}

func riskAndScopeNotes(summary Summary) []string {
	var notes []string
	for _, line := range commenttext.SectionLines(summary.IssueDescription, "out of scope", "out-of-scope", "out of scope paths", "out-of-scope paths") {
		notes = append(notes, "Out of scope: "+line)
	}
	if summary.PRDetails != nil && summary.PRDetails.ChangedFiles > 80 {
		notes = append(notes, fmt.Sprintf("Risk: PR changes %d files, above the scoped-run warning threshold.", summary.PRDetails.ChangedFiles))
	}
	if strings.TrimSpace(summary.ReviewStatus) != "" && summary.ReviewStatus != "passed" {
		notes = append(notes, "Risk: review status is "+summary.ReviewStatus+"; see follow-up and review artifacts.")
	}
	return commenttext.Unique(notes)
}

func progressStatusNotes(summary Summary) []string {
	if summary.Progress == nil {
		return nil
	}
	progress := summary.Progress
	notes := []string{
		"Phase: " + emptyAsNA(progress.Phase) + "; status: " + emptyAsNA(progress.Status) + "; next: " + emptyAsNA(progress.NextAction) + ".",
	}
	if progress.PRURL != "" {
		notes = append(notes, "Progress PR: "+progress.PRURL+".")
	}
	if progress.ProgressPath != "" {
		notes = append(notes, "Progress artifact: "+progress.ProgressPath+".")
	}
	return notes
}

func emptyAsNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "n/a"
	}
	return value
}
