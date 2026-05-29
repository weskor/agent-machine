package gate

import "strings"

const (
	StatusPassed               = "passed"
	StatusBlocked              = "blocked"
	StatusReconciliationNeeded = "reconciliation_needed"
)

type Blocker struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type Result struct {
	Domain     string            `json:"domain"`
	Subject    string            `json:"subject,omitempty"`
	Status     string            `json:"status"`
	ReasonText string            `json:"reason,omitempty"`
	Blockers   []Blocker         `json:"blockers,omitempty"`
	NextAction string            `json:"next_action,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

func NewResult(domain, subject string) Result {
	return Result{Domain: domain, Subject: subject, Status: StatusPassed}
}

func (r Result) Passed() bool {
	return r.Status == StatusPassed && len(r.Blockers) == 0
}

func (r Result) Reason() string {
	reasons := make([]string, 0, len(r.Blockers))
	for _, blocker := range r.Blockers {
		if strings.TrimSpace(blocker.Reason) != "" {
			reasons = append(reasons, blocker.Reason)
		}
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "; ")
	}
	return strings.TrimSpace(r.ReasonText)
}

func (r Result) Codes() []string {
	codes := make([]string, 0, len(r.Blockers))
	seen := map[string]bool{}
	for _, blocker := range r.Blockers {
		code := strings.TrimSpace(blocker.Code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes
}

func (r *Result) Block(code, reason string) {
	r.Status = StatusBlocked
	r.Blockers = append(r.Blockers, Blocker{Code: code, Reason: reason})
	if strings.TrimSpace(r.ReasonText) == "" {
		r.ReasonText = reason
	}
}

func (r *Result) ReconciliationNeeded(code, reason, nextAction string) {
	r.Status = StatusReconciliationNeeded
	r.Blockers = append(r.Blockers, Blocker{Code: code, Reason: reason})
	r.ReasonText = reason
	r.NextAction = nextAction
}

func (r Result) Payload() map[string]any {
	payload := map[string]any{
		"domain": r.Domain,
		"status": r.Status,
	}
	if r.Subject != "" {
		payload["subject"] = r.Subject
	}
	if reason := r.Reason(); reason != "" {
		payload["reason"] = reason
	}
	if len(r.Blockers) > 0 {
		blockers := make([]map[string]string, 0, len(r.Blockers))
		for _, blocker := range r.Blockers {
			blockers = append(blockers, map[string]string{"code": blocker.Code, "reason": blocker.Reason})
		}
		payload["blockers"] = blockers
		payload["codes"] = r.Codes()
	}
	if r.NextAction != "" {
		payload["next_action"] = r.NextAction
	}
	if len(r.Metadata) > 0 {
		payload["metadata"] = r.Metadata
	}
	return payload
}
