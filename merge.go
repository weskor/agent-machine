package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type pullRequestSummary struct {
	Number            int           `json:"number"`
	URL               string        `json:"url"`
	BaseRefName       string        `json:"baseRefName"`
	HeadRefName       string        `json:"headRefName"`
	Author            prAuthor      `json:"author"`
	Commits           []prCommit    `json:"commits,omitempty"`
	Mergeable         string        `json:"mergeable"`
	MergeStateStatus  string        `json:"mergeStateStatus"`
	ReviewDecision    string        `json:"reviewDecision"`
	StatusCheckRollup []statusCheck `json:"statusCheckRollup"`
}

type prAuthor struct {
	Login string `json:"login"`
}

type prCommit struct {
	OID    string         `json:"oid,omitempty"`
	Author prCommitAuthor `json:"author"`
}

type prCommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Login string `json:"login,omitempty"`
}

func (pr pullRequestSummary) AuthorLogin() string {
	return strings.TrimSpace(pr.Author.Login)
}

type statusCheck struct {
	Typename   string `json:"__typename"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
	Name       string `json:"name"`
	Context    string `json:"context"`
}

func (pr pullRequestSummary) mergeConflictReason() string {
	if strings.EqualFold(pr.Mergeable, "CONFLICTING") || strings.EqualFold(pr.MergeStateStatus, "DIRTY") {
		return fmt.Sprintf("GitHub reports mergeable=%s mergeStateStatus=%s; branch %s has conflicts with the base branch.", emptyAsUnknown(pr.Mergeable), emptyAsUnknown(pr.MergeStateStatus), pr.HeadRefName)
	}
	return ""
}

func (pr pullRequestSummary) mergeGateBlockReason() string {
	if reason := pr.mergeConflictReason(); reason != "" {
		return reason
	}
	if !strings.EqualFold(pr.Mergeable, "MERGEABLE") {
		return fmt.Sprintf("GitHub reports mergeable=%s; waiting for a fresh mergeable result before merging %s.", emptyAsUnknown(pr.Mergeable), pr.HeadRefName)
	}
	return checksBlockReason(pr.StatusCheckRollup)
}

// mergeApprovedPRs is intentionally conservative: it only merges Human Review
// issues when GitHub reports an approval and every reported check is green.
func mergeApprovedPRs(client linearClient, config runnerConfig) error {
	log("mode=merge-approved; project=%s", config.ProjectSlug)
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.MergeTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		return fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	prs = symphonyPRs(prs)
	log("found %d Symphony-owned open PR(s)", len(prs))
	for _, pr := range prs {
		identifier := issueIdentifierFromBranch(pr.HeadRefName)
		candidate, err := client.issueByIdentifier(identifier)
		if err != nil {
			return err
		}
		if candidate == nil || candidate.State.Name != config.HandoffState {
			continue
		}

		states, err := client.workflowStates(candidate.Team.ID)
		if err != nil {
			return err
		}
		if reason := pr.mergeConflictReason(); reason != "" {
			workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				return err
			}
			if err := writePRFeedback(workspace, pr.Number, renderPRConflictFeedback(pr, reason)); err != nil {
				return err
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					return err
				}
			}
			_ = client.createComment(candidate.ID, fmt.Sprintf("PR merge blocked by conflicts; captured repair instructions and moved back to %s for pickup: %s", config.ReadyState, pr.URL))
			log("blocked merge for %s: %s", candidate.Identifier, reason)
			continue
		}
		decision := reconcileIssue(config, *candidate, &pr)
		if decision.ShouldQuarantine && len(decision.Blockers) > 0 {
			_ = client.createComment(candidate.ID, fmt.Sprintf("Symphony PR blocked by reconciliation invariant; next=%s; reason: %s", decision.NextAction, strings.Join(decision.Blockers, "; ")))
			log("%s quarantined: %s", pr.URL, strings.Join(decision.Blockers, "; "))
			continue
		}
		switch pr.ReviewDecision {
		case "APPROVED":
			if !decision.CanMerge {
				log("%s approved but merge is blocked: lifecycle=%s blockers=%s next=%s", pr.URL, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
				continue
			}
			if err := github.SquashMergePullRequest(ctx, pr.Number); err != nil {
				return fmt.Errorf("GitHub API squash merge failed for PR #%d: %w", pr.Number, err)
			}
			if err := github.DeleteBranch(ctx, pr.HeadRefName); err != nil {
				return fmt.Errorf("GitHub API branch deletion failed for %s after merged PR #%d: %w", pr.HeadRefName, pr.Number, err)
			}
			if id := stateID(states, config.DoneState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					return err
				}
			}
			if err := removeDoneWorkspace(config.WorkspaceRoot, candidate.Identifier); err != nil {
				return err
			}
			_ = client.createComment(candidate.ID, fmt.Sprintf("Merged approved PR: %s", pr.URL))
			log("merged %s and moved %s to %s", pr.URL, candidate.Identifier, config.DoneState)
		case "CHANGES_REQUESTED":
			feedback, err := collectPRFeedback(pr.Number)
			if err != nil {
				return err
			}
			workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				return err
			}
			if feedbackAlreadyAddressed(workspace, pr.URL, feedback) {
				log("%s has CHANGES_REQUESTED but feedback was already addressed; waiting for human approval", pr.URL)
				continue
			}
			if err := writePRFeedback(workspace, pr.Number, feedback); err != nil {
				return err
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					return err
				}
			}
			_ = client.createComment(candidate.ID, fmt.Sprintf("PR changes requested; captured GitHub review feedback and moved back to %s for pickup: %s", config.ReadyState, pr.URL))
			log("moved %s back to %s after requested changes; feedback captured", candidate.Identifier, config.ReadyState)
		default:
			log("%s waiting for approval; reviewDecision=%s", pr.URL, pr.ReviewDecision)
		}
	}
	return nil
}

func feedbackAlreadyAddressed(workspace, prURL, feedback string) bool {
	record, ok := reusableRunRecord(workspace)
	if !ok || record.Status != "success" || record.ReviewStatus != "passed" || record.PRURL != prURL {
		return false
	}
	currentHash := feedbackHash(feedback)
	if currentHash == "" {
		return false
	}
	if record.FeedbackHash != "" {
		return record.FeedbackHash == currentHash
	}
	previousFeedback, err := readPRFeedback(workspace)
	return err == nil && feedbackHash(previousFeedback) == currentHash
}

func renderPRConflictFeedback(pr pullRequestSummary, reason string) string {
	return fmt.Sprintf(`# PR #%d merge conflict feedback

## Blocked merge reason

%s

## Repair instructions

- Update this PR branch from the configured base branch.
- Resolve merge conflicts without starting unrelated work.
- Rerun the validation expected by the Linear issue.
- Push the same PR branch and stop for Human Review again.

PR: %s
Branch: %s
`, pr.Number, reason, pr.URL, pr.HeadRefName)
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UNKNOWN"
	}
	return value
}

func symphonyPRs(prs []pullRequestSummary) []pullRequestSummary {
	filtered := make([]pullRequestSummary, 0, len(prs))
	for _, pr := range prs {
		if issueIdentifierFromBranch(pr.HeadRefName) == "" {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}

func issueIdentifierFromBranch(branch string) string {
	match := regexp.MustCompile(`(?i)(CAG[-_][0-9]+)`).FindString(branch)
	return strings.ToUpper(strings.ReplaceAll(match, "_", "-"))
}

func checksPassed(checks []statusCheck) bool {
	return checksBlockReason(checks) == ""
}

func checksBlockReason(checks []statusCheck) string {
	if len(checks) == 0 {
		return "no status checks were reported by GitHub"
	}
	for _, check := range checks {
		switch check.Typename {
		case "CheckRun":
			if !strings.EqualFold(check.Status, "COMPLETED") || !strings.EqualFold(check.Conclusion, "SUCCESS") {
				return fmt.Sprintf("check run %q is status=%s conclusion=%s", checkLabel(check), emptyAsUnknown(check.Status), emptyAsUnknown(check.Conclusion))
			}
		case "StatusContext":
			if !strings.EqualFold(check.State, "SUCCESS") {
				return fmt.Sprintf("status context %q is state=%s", checkLabel(check), emptyAsUnknown(check.State))
			}
		default:
			return fmt.Sprintf("unknown status check shape %q for %q", emptyAsUnknown(check.Typename), checkLabel(check))
		}
	}
	return ""
}

func checkLabel(check statusCheck) string {
	if strings.TrimSpace(check.Name) != "" {
		return check.Name
	}
	if strings.TrimSpace(check.Context) != "" {
		return check.Context
	}
	return "unnamed"
}

func workspaceLockedOrModified(workspaceRoot, identifier, _ string) (bool, string) {
	workspace := filepath.Join(workspaceRoot, identifier)
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		return false, ""
	}
	for _, lockPath := range []string{
		filepath.Join(workspace, ".git", "index.lock"),
		filepath.Join(workspace, ".pi-symphony.lock"),
	} {
		if _, err := os.Stat(lockPath); err == nil {
			return true, fmt.Sprintf("workspace %s is locked", workspace)
		}
	}
	changed, err := workspaceHasChanges(workspace)
	if err != nil {
		return true, fmt.Sprintf("workspace %s status could not be checked", workspace)
	}
	if changed {
		return true, fmt.Sprintf("workspace %s has uncommitted changes", workspace)
	}
	return false, ""
}

func runArtifactMergeBlockReason(workspaceRoot, identifier, prURL string) string {
	record, ok := readRunArtifact(filepath.Join(workspaceRoot, identifier))
	if !ok {
		return "missing run artifact for approved Symphony PR"
	}
	if strings.TrimSpace(record.PRURL) != "" && strings.TrimSpace(prURL) != "" && strings.TrimSpace(record.PRURL) != strings.TrimSpace(prURL) {
		return fmt.Sprintf("run artifact PR URL %s does not match candidate PR %s", record.PRURL, prURL)
	}
	if record.Status != "success" {
		return fmt.Sprintf("run status is %s", emptyAsUnknown(record.Status))
	}
	if record.ReviewStatus != "passed" {
		return fmt.Sprintf("review status is %s", emptyAsUnknown(record.ReviewStatus))
	}
	return ""
}

func readRunArtifact(workspace string) (runRecord, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		return runRecord{}, false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, false
	}
	return record, true
}
