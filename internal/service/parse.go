package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

type llmReview struct {
	Summary string         `json:"summary"`
	Verdict string         `json:"verdict"`
	Issues  []domain.Issue `json:"issues"`
}

// parseReview extracts the review JSON from model output. Providers with
// structured output return bare JSON; others may wrap it in prose or fences.
func parseReview(text string) (*llmReview, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("%w: no JSON object found in model output", domain.ErrLLMBadResponse)
	}

	var r llmReview
	if err := json.Unmarshal([]byte(text[start:end+1]), &r); err != nil {
		return nil, fmt.Errorf("%w: decode review JSON: %v", domain.ErrLLMBadResponse, err)
	}
	normalize(&r)
	return &r, nil
}

func normalize(r *llmReview) {
	if r.Issues == nil {
		r.Issues = []domain.Issue{}
	}
	worst := 0
	for i := range r.Issues {
		is := &r.Issues[i]
		is.Severity = domain.Severity(strings.ToLower(strings.TrimSpace(string(is.Severity))))
		switch is.Severity {
		case domain.SeverityCritical, domain.SeverityMajor, domain.SeverityMinor, domain.SeverityInfo:
		default:
			is.Severity = domain.SeverityInfo
		}
		if is.Line < 0 {
			is.Line = 0
		}
		worst = max(worst, is.Severity.Rank())
	}
	sort.SliceStable(r.Issues, func(i, j int) bool {
		return r.Issues[i].Severity.Rank() > r.Issues[j].Severity.Rank()
	})

	switch domain.Verdict(r.Verdict) {
	case domain.VerdictApprove, domain.VerdictComment, domain.VerdictRequestChanges:
	default:
		r.Verdict = string(deriveVerdict(len(r.Issues), worst))
	}
}

func deriveVerdict(count, worstRank int) domain.Verdict {
	switch {
	case worstRank >= domain.SeverityMajor.Rank():
		return domain.VerdictRequestChanges
	case count > 0:
		return domain.VerdictComment
	default:
		return domain.VerdictApprove
	}
}
