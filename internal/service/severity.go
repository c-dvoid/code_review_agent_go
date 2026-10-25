package service

import "github.com/c-dvoid/code-review-agent-go/internal/domain"

// Likelihood values: how readily an issue is triggered.
const (
	LikelihoodCommon   = "common"
	LikelihoodEdgeCase = "edge_case"
)

// severityByImpact maps an issue's impact to its severity when it is triggered
// on common paths and when it only shows up in edge cases. Deriving severity
// from this table, instead of letting the model pick a level, keeps the same
// problem at the same severity across runs and models.
var severityByImpact = map[string]struct{ common, edgeCase domain.Severity }{
	"security":           {domain.SeverityCritical, domain.SeverityMajor},
	"data_loss":          {domain.SeverityCritical, domain.SeverityMajor},
	"crash":              {domain.SeverityCritical, domain.SeverityMajor},
	"incorrect_behavior": {domain.SeverityMajor, domain.SeverityMinor},
	"resource":           {domain.SeverityMajor, domain.SeverityMinor},
	"performance":        {domain.SeverityMinor, domain.SeverityInfo},
	"maintainability":    {domain.SeverityMinor, domain.SeverityInfo},
}

// impactNames lists the valid impacts in prompt and schema order.
var impactNames = []string{"security", "data_loss", "crash", "incorrect_behavior", "resource", "performance", "maintainability"}

// deriveSeverity returns the severity for an impact and likelihood, and false
// when the impact is unknown. An unknown likelihood counts as an edge case.
func deriveSeverity(impact, likelihood string) (domain.Severity, bool) {
	s, ok := severityByImpact[impact]
	if !ok {
		return "", false
	}
	if likelihood == LikelihoodCommon {
		return s.common, true
	}
	return s.edgeCase, true
}
