package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

const reviewPassMarker = "REVIEW_PASS"
const reviewFailMarker = "REVIEW_FAIL"

const (
	reviewClassificationBehaviorSpecBlocker = "behavior_spec_blocker"
	reviewClassificationMissingEvidenceOnly = "missing_evidence_only"
	reviewClassificationUnknown             = "unknown"
)

func reviewPrompt(candidate *issue, prURL, workspace string) string {
	return fmt.Sprintf(`Review the final Symphony/Pi runner output for %s.

PR: %s
Workspace: %s

Review only. Do not edit files, commit, push, merge, or comment on GitHub/Linear.

Check for:
- scope drift from the Linear issue description
- runtime app code, secrets, environment variable, or unrelated-file changes
- missing requested validation evidence
- obvious tenant/security risk
- for refactors/replacements/rewrites: missing behavior-contract evidence, dropped observable behavior, lost side effects or cleanup, weakened error handling, changed security/ownership assumptions, or unverified state transitions

Behavior-contract review requirements for refactors, replacements, and rewrites:
- REVIEW_FAIL if the PR replaces code, dependencies, commands, integrations, workflows, or state-machine logic without an existing-behavior inventory covering inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Use docs/specs/ and docs/adr/ as the behavior-contract source when present, including docs/specs/harness-behavior.md and docs/agents/review-policy.md; REVIEW_FAIL if observable behavior contradicts a spec or ADR without an explicit issue-backed update.
- REVIEW_FAIL if the Behavior Contract Evidence section is missing for broad refactors, replacements, or rewrites: relevant specs/ADRs cited, behavior preserved, behavior intentionally changed with justification, and unknown behavior needing clarification.
- REVIEW_FAIL if a broad mechanical move does not cite relevant specs/ADRs or explicitly state that no spec changes were needed.
- REVIEW_FAIL if observable behavior lacks TDD or characterization-test evidence, or if tests cover only the new abstraction while prior observable behavior is unprotected.
- REVIEW_FAIL if broad replacement work lacks a complexity/LOC budget: expected files touched, expected LOC direction, why net growth is acceptable, bespoke code removed, and when to split.
- REVIEW_FAIL if prior side effects, cleanup, or state transitions are dropped without explicit issue-backed justification.

%s

Domain-semantic review requirements:
- Route changed files that create or mutate app data directly into a domain review, especially tools/compound-client-cli, database seeds, auth/invitations/memberships, onboarding, KYC, documents, and payments.
- For direct database writes, identify the nearest production flow and compare required fields, enum values, roles, statuses, tenant/org scope, and side effects.
- Flag hardcoded domain strings such as invitation roles, membership roles, portfolio statuses, KYC statuses, and payment/document states unless they are constants or explicitly verified against schema/domain source.
- REVIEW_PASS requires evidence that relevant domain source files were checked when domain data is mutated. For customer/client invitation or membership changes, compare against packages/auth/src/permissions.ts and apps/dashboard/src/trpc/routers/organization/organization.clients.ts.

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
`, candidate.Identifier, prURL, workspace, ticketContractReviewPrompt(), reviewPassMarker, reviewFailMarker, reviewFailMarker)
}

func reviewCommandWithHighReasoning(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "--thinking ") {
		fields := strings.Fields(trimmed)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "--thinking" {
				fields[i+1] = "xhigh"
				return strings.Join(fields, " ")
			}
		}
	}
	return trimmed + " --thinking xhigh"
}

// runReview performs a separate read-only Pi pass over the final diff before
// the runner hands the Linear issue to Human Review.
func runReview(reviewCommand, workspace string, candidate *issue, prURL string, env map[string]string, timeout time.Duration) (*reviewResult, error) {
	if strings.TrimSpace(reviewCommand) == "" {
		return nil, nil
	}
	prompt := reviewPrompt(candidate, prURL, workspace)
	promptPath := filepath.Join(workspace, ".pi-symphony-review-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return nil, err
	}

	started := time.Now()
	output, err := sh.CaptureEnvWithOutputTimeout(fmt.Sprintf("%s @%s", reviewCommandWithHighReasoning(reviewCommand), sh.Quote(promptPath)), workspace, env, true, timeout)
	log("review duration: %s", time.Since(started).Round(time.Second))
	findings := assistantText(output)
	if findings == "" {
		findings = output
	}
	status := reviewStatus(findings)
	result := &reviewResult{Status: status, Classification: reviewClassification(status, findings), Findings: strings.TrimSpace(findings), Usage: parseUsage(output)}
	if result.Usage != nil {
		log("review usage: input=%.0f output=%.0f cacheRead=%.0f total=%.0f cost=$%.4f", result.Usage.Input, result.Usage.Output, result.Usage.CacheRead, result.Usage.TotalTokens, result.Usage.totalCost())
	}
	if err != nil {
		result.Status = "error"
		return result, err
	}
	return result, nil
}

func reviewClassification(status, output string) string {
	if status == "passed" {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "REVIEW_CLASSIFICATION:") {
			value := strings.TrimSpace(trimmed[len("REVIEW_CLASSIFICATION:"):])
			switch value {
			case reviewClassificationBehaviorSpecBlocker, reviewClassificationMissingEvidenceOnly, reviewClassificationUnknown:
				return value
			default:
				return reviewClassificationUnknown
			}
		}
	}
	return reviewClassificationUnknown
}

func reviewFailureRoutesToHumanHandoff(review *reviewResult, prURL string) bool {
	return review != nil && review.Status == "failed" && review.Classification == reviewClassificationMissingEvidenceOnly && strings.TrimSpace(prURL) != ""
}

func reviewStatus(output string) string {
	for _, line := range strings.Split(output, "\n") {
		switch strings.TrimSpace(line) {
		case reviewPassMarker:
			return "passed"
		case reviewFailMarker:
			return "failed"
		}
	}
	return "unknown"
}
