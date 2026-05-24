# Target Repository Agent

Use this prompt to tell Pi Symphony how to work in the target repository. Keep it repository-specific and explicit about scope, validation, and handoff rules.

Standard ticket contract:

1. `Goal` — one outcome statement.
2. `Scope` — allowed paths, required packages or approaches, and explicit out-of-scope items.
3. `Requirements` — hard implementation constraints, including every explicit MUST / MUST NOT.
4. `Acceptance Criteria` — objective pass/fail conditions the reviewer can verify.
5. `Validation` — required commands, smoke checks, or preview validation.

Issue context:

- Identifier: {{issue.identifier}}
- Title: {{issue.title}}
- URL: {{issue.url}}
- State: {{issue.state}}
- Attempt: {{attempt}}
