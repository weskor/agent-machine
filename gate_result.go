package main

import "strings"

const (
	deterministicGateStatusPassed               = "passed"
	deterministicGateStatusBlocked              = "blocked"
	deterministicGateStatusReconciliationNeeded = "reconciliation_needed"
)

type deterministicGateBlocker struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type deterministicGateResult struct {
	Domain     string                     `json:"domain"`
	Subject    string                     `json:"subject,omitempty"`
	Status     string                     `json:"status"`
	ReasonText string                     `json:"reason,omitempty"`
	Blockers   []deterministicGateBlocker `json:"blockers,omitempty"`
	NextAction string                     `json:"next_action,omitempty"`
	Metadata   map[string]string          `json:"metadata,omitempty"`
}

func newDeterministicGateResult(domain, subject string) deterministicGateResult {
	return deterministicGateResult{Domain: domain, Subject: subject, Status: deterministicGateStatusPassed}
}

func (r deterministicGateResult) Passed() bool {
	return r.Status == deterministicGateStatusPassed && len(r.Blockers) == 0
}

func (r deterministicGateResult) Reason() string {
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

func (r deterministicGateResult) Codes() []string {
	codes := make([]string, 0, len(r.Blockers))
	for _, blocker := range r.Blockers {
		if strings.TrimSpace(blocker.Code) != "" {
			codes = append(codes, blocker.Code)
		}
	}
	return uniqueStrings(codes)
}

func (r *deterministicGateResult) Block(code, reason string) {
	r.Status = deterministicGateStatusBlocked
	r.Blockers = append(r.Blockers, deterministicGateBlocker{Code: code, Reason: reason})
	if strings.TrimSpace(r.ReasonText) == "" {
		r.ReasonText = reason
	}
}

func (r *deterministicGateResult) ReconciliationNeeded(code, reason, nextAction string) {
	r.Status = deterministicGateStatusReconciliationNeeded
	r.Blockers = append(r.Blockers, deterministicGateBlocker{Code: code, Reason: reason})
	r.ReasonText = reason
	r.NextAction = nextAction
}

func (r deterministicGateResult) Payload() map[string]any {
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
