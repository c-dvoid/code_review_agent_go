package service

import (
	"fmt"
	"strings"

	"github.com/c-dvoid/code-review-agent-go/internal/domain"
)

var verdictLabel = map[domain.Verdict]string{
	domain.VerdictApprove:        "✅ Approve",
	domain.VerdictComment:        "💬 Comment",
	domain.VerdictRequestChanges: "⛔ Request changes",
}

// FormatMarkdown renders a review report as a pull request comment.
func FormatMarkdown(r *domain.ReviewReport) string {
	var b strings.Builder
	b.WriteString("## 🤖 Automated Code Review\n\n")
	fmt.Fprintf(&b, "**Verdict:** %s\n\n", verdictLabel[r.Verdict])
	b.WriteString(r.Summary + "\n\n")

	if len(r.Issues) == 0 {
		b.WriteString("No issues found.\n")
	} else {
		fmt.Fprintf(&b, "### Findings (%d)\n\n", len(r.Issues))
		for i, is := range r.Issues {
			loc := ""
			if is.File != "" {
				loc = " — `" + is.File
				if is.Line > 0 {
					loc += fmt.Sprintf(":%d", is.Line)
				}
				loc += "`"
			}
			fmt.Fprintf(&b, "%d. **[%s] %s** _(%s)_%s\n", i+1, strings.ToUpper(string(is.Severity)), is.Title, is.Category, loc)
			fmt.Fprintf(&b, "   %s\n", is.Description)
			if is.Suggestion != "" {
				fmt.Fprintf(&b, "   > 💡 %s\n", is.Suggestion)
			}
			b.WriteString("\n")
		}
	}

	// The comment is edited in place on every push, so say which commit it covers.
	commit := ""
	if r.PullRequest != nil && r.PullRequest.HeadSHA != "" {
		commit = fmt.Sprintf(" · commit `%.7s`", r.PullRequest.HeadSHA)
	}
	fmt.Fprintf(&b, "\n<sub>Reviewed by `%s` (%s)%s · review id `%s` · updated on each push</sub>\n", r.Model, r.Provider, commit, r.ID)
	return b.String()
}
