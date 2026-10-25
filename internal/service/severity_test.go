package service

import (
	"testing"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

func TestParseReview_DerivesSeverityAndVerdict(t *testing.T) {
	// The model's own severity and verdict disagree with its classification;
	// the classification wins.
	text := `{"summary":"s","verdict":"approve","issues":[
		{"impact":"maintainability","likelihood":"common","severity":"critical","title":"smell"},
		{"impact":"Security","likelihood":"COMMON","severity":"major","title":"sqli"},
		{"impact":"crash","likelihood":"sometimes","title":"rare panic"},
		{"impact":"vibes","likelihood":"common","severity":"minor","title":"unknown impact"},
		{"title":"no classification"}
	]}`
	r, err := parseReview(text)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]domain.Issue{
		"sqli":              {Severity: domain.SeverityCritical, Impact: "security", Likelihood: "common"},
		"rare panic":        {Severity: domain.SeverityMajor, Impact: "crash"}, // unknown likelihood counts as edge case
		"smell":             {Severity: domain.SeverityMinor, Impact: "maintainability", Likelihood: "common"},
		"unknown impact":    {Severity: domain.SeverityMinor}, // falls back to the model's severity
		"no classification": {Severity: domain.SeverityInfo},
	}
	for _, is := range r.Issues {
		w := want[is.Title]
		if is.Severity != w.Severity || is.Impact != w.Impact || is.Likelihood != w.Likelihood {
			t.Errorf("%s: got severity=%q impact=%q likelihood=%q, want %+v", is.Title, is.Severity, is.Impact, is.Likelihood, w)
		}
	}
	if r.Issues[0].Title != "sqli" {
		t.Errorf("issues not sorted by severity: first is %q", r.Issues[0].Title)
	}
	if r.Verdict != string(domain.VerdictRequestChanges) {
		t.Errorf("verdict = %q, want request_changes despite the model's approve", r.Verdict)
	}
}

func TestSeverityTableCoversSchema(t *testing.T) {
	if len(impactNames) != len(severityByImpact) {
		t.Fatalf("impactNames has %d entries, severityByImpact %d", len(impactNames), len(severityByImpact))
	}
	for _, name := range impactNames {
		if _, ok := deriveSeverity(name, LikelihoodCommon); !ok {
			t.Errorf("impact %q is in the schema but has no severity", name)
		}
	}
}
