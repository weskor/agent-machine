# Pi Symphony GitHub behavior contract

Phase 1 inventory plus Phase 2/3 parity notes for replacing core `gh` CLI behavior.

## Observable GitHub contract and parity checklist

- [x] Open PR metadata preserves the former `gh pr list --state open --json number,url,baseRefName,headRefName,author,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup` shape through the typed GitHub API client.
- [x] Open PR metadata uses GitHub App-compatible REST endpoints instead of GraphQL `statusCheckRollup` fields so status and merge lanes can run with installation tokens.
- [x] Symphony-owned PRs are selected by CAG issue IDs in `headRefName`.
- [x] Branch/base sanity requires base `develop` (or configured base) and head `symphony/<issue>-workspace`.
- [x] App-authored PR invariant accepts both valid GitHub API shapes for the same installed app: GraphQL/gh-style `app/pi-symphony-bot` and REST/go-github `pi-symphony-bot[bot]`.
- [x] Bot commit identity is separate: commit author name/email must be `pi-symphony-bot[bot] <285402021+pi-symphony-bot[bot]@users.noreply.github.com>` when commit evidence is available.
- [x] Human-authored PRs are quarantined before merge.
- [x] Green checks allow merge only when every `CheckRun` is completed/success and every `StatusContext` is success.
- [x] Pending, failed, cancelled, timed-out, action-required, neutral, missing, and unknown check shapes block merge.
- [x] Merge conflicts block merge when `mergeable=CONFLICTING` or `mergeStateStatus=DIRTY`, write repair feedback, and move the Linear issue back to Ready.
- [x] `CHANGES_REQUESTED` captures review, issue comment, and inline review comment feedback, writes `.pi-symphony-feedback.md`, and moves the Linear issue back to Ready unless already addressed.
- [x] Review feedback preserves the former `gh pr view <number> --json reviews,comments` plus `gh api repos/:owner/:repo/pulls/<number>/comments` shapes through the typed GitHub API client.
- [x] PR handoff comments keep a stable issue-comment identity by finding the `<!-- pi-symphony-summary -->` marker via the typed GitHub API client and updating `issues/comments/<id>`; absent marker creates an issue comment through the typed client.
- [x] Artifact repair preserves the former `gh pr view <url> --json state,mergedAt` semantics through the typed GitHub API client and treats `MERGED` or non-null `mergedAt` as manually merged.
- [x] Approved PR merge uses the typed GitHub API client to squash merge after reconciliation, review, checks, conflict, artifact, and workspace gates pass.
- [x] Post-merge branch deletion uses the typed GitHub API client only after squash merge confirms `merged=true`, and still refuses non-CAG/Symphony branch names.
- [x] GitHub repository owner/name are resolved from `GITHUB_REPOSITORY` or git remote origin rather than hardcoded in client code.
- [x] The typed GitHub API client uses `GH_TOKEN`/`GITHUB_TOKEN` when present and otherwise mints a short-lived GitHub App installation token from `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, and `GITHUB_APP_PRIVATE_KEY_PATH` so daemon merge/status paths do not require manual `gh auth token` exports.

## Phase 2 scope guard

Read-only GitHub gates and core merge/comment/branch-delete mutations use the typed GitHub API client. Any remaining `gh` usage is non-core shell compatibility or follow-up inventory.

Known remaining non-core `gh` usage: `closeInvalidPR` still shells out to `gh pr close` for pre-handoff sanity-check cleanup, which is outside the CAG-41 merge/comment/branch-delete replacement scope.
