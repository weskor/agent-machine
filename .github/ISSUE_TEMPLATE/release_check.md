---
name: Release check
about: Track a release readiness task
title: "Release: "
labels: ""
assignees: ""
---

## Goal


## Scope


## Acceptance Criteria

- [ ] Documentation is updated
- [ ] Release packaging is validated
- [ ] Stability checks pass

## Validation

```bash
mise exec go -- make ci
git diff --check
goreleaser check
goreleaser release --snapshot --clean
```
