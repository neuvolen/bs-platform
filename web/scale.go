package web

import (
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// R60/R61: the login page shows the library's real scale. {{scale:diag}},
// {{scale:tools}}, {{scale:ideas}} are the counts («1 059»), {{scale:diag.w}}
// and the like the word in the right form («диагноза»), {{scale:text}} the
// whole phrase (content.ScaleText). Filled when the page is built, so a
// bigger library needs no edit here. The voiced demo lines say rounded
// numbers («больше ста семидесяти диагнозов»): r60_scale_test keeps them true.
func injectScale(page string) string {
	if !strings.Contains(page, "{{scale:") {
		return page
	}
	d, t, i := content.Scale()
	return strings.NewReplacer(
		"{{scale:diag}}", content.Num(d), "{{scale:diag.w}}", content.Plural(d, "диагноз", "диагноза", "диагнозов"),
		"{{scale:tools}}", content.Num(t), "{{scale:tools.w}}", content.Plural(t, "инструмент", "инструмента", "инструментов"),
		"{{scale:ideas}}", content.Num(i), "{{scale:ideas.w}}", content.Plural(i, "бизнес-идея", "бизнес-идеи", "бизнес-идей"),
		"{{scale:text}}", content.ScaleText(),
	).Replace(page)
}
