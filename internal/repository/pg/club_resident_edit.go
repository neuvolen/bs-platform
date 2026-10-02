package pg

import (
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// setResidentField also carries the resident card's settings edited on the
// platform (script v37): exception («Исключение», L), format («Формат», O)
// and joinedAt («Дата входа»). Like the other fields it sets a value by
// name, so it is safe to put on top of an import again.

func init() {
	ResidentFields["exception"] = "exception"
	ResidentFields["format"] = "format"
	ResidentFields["joinedAt"] = "joined_at"
	reapplyAfterSend["setResidentField"] = true
	base := extraApply["setResidentField"]
	extraApply["setResidentField"] = func(a *applier, p map[string]string) error {
		switch strings.TrimSpace(p["field"]) {
		case "exception", "format", "joinedAt":
			return a.setResidentSetting(p)
		}
		return base(a, p)
	}
}

func (a *applier) setResidentSetting(p map[string]string) error {
	r, err := a.resident(strings.TrimSpace(p["name"]))
	if err != nil {
		return err
	}
	v := p["value"]
	switch strings.TrimSpace(p["field"]) {
	case "exception":
		x, err := club.EditExceptionValue(v)
		if err != nil {
			return err
		}
		return a.update("club_residents", r.id, `exception = $2, updated_at = now()`, x == "Да")
	case "format":
		x, err := club.EditFormatValue(v)
		if err != nil {
			return err
		}
		return a.update("club_residents", r.id, `format = $2, updated_at = now()`, x)
	default: // joinedAt
		d, err := club.EditJoinDate(v, club.Today())
		if err != nil {
			return err
		}
		return a.update("club_residents", r.id, `joined_at = $2, updated_at = now()`, d.Format("2006-01-02"))
	}
}
