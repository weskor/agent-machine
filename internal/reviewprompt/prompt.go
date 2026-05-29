package reviewprompt

import (
	"fmt"
	"strings"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/ticketcontract"
)

type Evidence struct {
	IssueIdentifier string
	IssueTitle      string
	PRURL           string
	Workspace       string
	BaseBranch      string
	HeadBranch      string
	HeadSHA         string
	ChangedFiles    int
	Additions       int
	Deletions       int
	ChecksStatus    string
	ChecksSummary   string
	ScopeSummary    string
	ReviewGuidance  string
	Validation      []string
	ProgressPath    string
}

func (e Evidence) PromptBlock() string {
	var b strings.Builder
	b.WriteString("Runner-owned deterministic review evidence:\n")
	writeEvidenceLine(&b, "Issue", strings.TrimSpace(e.IssueIdentifier+" "+e.IssueTitle))
	writeEvidenceLine(&b, "PR", e.PRURL)
	writeEvidenceLine(&b, "Workspace", e.Workspace)
	writeEvidenceLine(&b, "Base branch", e.BaseBranch)
	writeEvidenceLine(&b, "Head branch", e.HeadBranch)
	writeEvidenceLine(&b, "Head SHA", e.HeadSHA)
	if e.ChangedFiles > 0 || e.Additions > 0 || e.Deletions > 0 {
		writeEvidenceLine(&b, "Diff size", fmt.Sprintf("files=%d additions=%d deletions=%d", e.ChangedFiles, e.Additions, e.Deletions))
	}
	writeEvidenceLine(&b, "Code-host checks", emptyAsUnknown(e.ChecksStatus)+" — "+emptyAsUnknown(e.ChecksSummary))
	writeEvidenceLine(&b, "Scope guard", e.ScopeSummary)
	if len(e.Validation) > 0 {
		b.WriteString("- Validation:\n")
		for _, line := range e.Validation {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				b.WriteString("  - " + trimmed + "\n")
			}
		}
	}
	writeEvidenceLine(&b, "Progress snapshot", e.ProgressPath)
	b.WriteString("\nUse this packet as the source of truth for deterministic PR/check/scope facts. Focus review findings on semantic/spec quality that the runner cannot compute deterministically.\n")
	return b.String()
}

func Prompt(candidate *domain.Issue, prURL, workspace, guidance string, evidence *Evidence) string {
	evidenceBlock := "Runner-owned deterministic review evidence: unavailable; use the local git diff and PR details available in the workspace."
	if evidence != nil {
		evidenceBlock = evidence.PromptBlock()
	}
	guidanceBlock := reviewGuidancePromptBlock(guidance)
	return fmt.Sprintf(`Review the final Agent Machine/Pi runner output for %s.

PR: %s
Workspace: %s

%s

Review only. Do not edit files, commit, push, merge, or comment on the code host or Linear.

Check for:
- scope drift from the Linear issue description
- runtime app code, secrets, environment variable, or unrelated-file changes
- missing requested validation evidence
- obvious tenant/security risk
- for refactors/replacements/rewrites: missing behavior-contract evidence, dropped observable behavior, lost side effects or cleanup, weakened error handling, changed security/ownership assumptions, or unverified state transitions

Behavior-contract review requirements for refactors, replacements, and rewrites:
- REVIEW_FAIL if the PR replaces code, dependencies, commands, integrations, workflows, or state-machine logic without an existing-behavior inventory covering inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Use docs/specs/ and docs/adr/ as the behavior-contract source when present, including docs/specs/harness-behavior.md and docs/agents/review-policy.md; REVIEW_FAIL if observable behavior contradicts a spec or ADR without an explicit issue-backed update.
- REVIEW_FAIL if the Behavior Contract Evidence section is missing from the PR body or handoff evidence source for broad refactors, replacements, or rewrites: relevant specs/ADRs cited, behavior preserved, behavior intentionally changed with justification, and unknown behavior needing clarification.
- REVIEW_FAIL if a broad mechanical move does not cite relevant specs/ADRs or explicitly state that no spec changes were needed.
- REVIEW_FAIL if observable behavior lacks TDD or characterization-test evidence, or if tests cover only the new abstraction while prior observable behavior is unprotected.
- REVIEW_FAIL if broad replacement work lacks a complexity/LOC budget: expected files touched, expected LOC direction, why net growth is acceptable, bespoke code removed, and when to split.
- REVIEW_FAIL if prior side effects, cleanup, or state transitions are dropped without explicit issue-backed justification.

%s
%s

Use the local git diff and PR details available in the workspace.

Respond with exactly one marker line:
%s — if the PR is safe to hand off
%s — if the PR should go back to Ready for Agent

If you return %s, also include exactly one classification line:
REVIEW_CLASSIFICATION: behavior_spec_blocker
REVIEW_CLASSIFICATION: missing_evidence_only
REVIEW_CLASSIFICATION: unknown

Classification rules:
- behavior_spec_blocker: behavior/spec mismatch, scope drift, validation blocker, security risk, out-of-scope changes, missing required validation, or any finding that means the implementation should be repaired before Human Review.
- missing_evidence_only: the PR exists and validation appears green, but the only concern is missing or insufficient Behavior Contract Evidence in the PR body/handoff notes. Use only when there is no behavior/spec/scope/validation blocker.
- unknown: malformed, ambiguous, or mixed findings. Use unknown unless missing_evidence_only is clearly the only issue.

Then add concise findings.
`, candidate.Identifier, prURL, workspace, evidenceBlock, ticketcontract.ReviewPrompt(), guidanceBlock, reviewpolicy.PassMarker, reviewpolicy.FailMarker, reviewpolicy.FailMarker)
}

func writeEvidenceLine(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.WriteString(fmt.Sprintf("- %s: %s\n", label, strings.TrimSpace(value)))
}

func reviewGuidancePromptBlock(guidance string) string {
	trimmed := strings.TrimSpace(guidance)
	if trimmed == "" {
		return ""
	}
	return "\nTarget-repository review guidance from project configuration:\n" + trimmed + "\n"
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
