package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// Writes the server makes on its own besides the club tables (migration
// step 3):
//   - the app's section writes (wheel, tasks, problems, SMM, leads, content,
//     checklists, profiles) change the server's copy of those sheets
//     (club_sheets, as displayed), the way the script changes the sheet;
//   - setResidentField corrects one field of a resident (the data audit).
//
// Both go the write-first way: kept, applied here, then sent to the script.

func init() {
	for a := range club.SectionWriteActions {
		ClubWriteActions[a] = true
	}
	ClubWriteActions["setResidentField"] = true
}

// SectionWrite: an app section write (applied here for anyone, as the
// script accepts it from anyone).
func SectionWrite(action string) bool {
	_, ok := club.SectionWriteActions[action]
	return ok
}

var extraApply = map[string]func(a *applier, p map[string]string) error{"setResidentField": (*applier).setResidentField}

var extraSeen = map[string]func(a *applier, p map[string]string) (*bool, error){}

func init() {
	for act := range club.SectionWriteActions {
		act := act
		extraApply[act] = func(a *applier, p map[string]string) error { return a.applySection(act, p) }
		extraSeen[act] = func(a *applier, p map[string]string) (*bool, error) { return a.sectionSeen(act, p) }
	}
}

// appliesHere: the write changes something the server keeps.
func appliesHere(action string) bool { return clubApplyActions[action] || extraApply[action] != nil }

func (a *applier) sheet(name string, lock bool) ([][]string, bool, error) {
	q := `SELECT rows FROM club_sheets WHERE name = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	var raw []byte
	err := a.tx.QueryRow(a.ctx, q, name).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rows [][]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

// hasSections: the last import brought the app's section sheets.
func (a *applier) hasSections() (bool, error) {
	_, ok, err := a.sheet(club.SheetScriptProps, false)
	return ok, err
}

func (a *applier) applySection(action string, p map[string]string) error {
	if ok, err := a.hasSections(); err != nil || !ok {
		if err != nil {
			return err
		}
		return ErrNothingToApply // the script still keeps these sheets alone
	}
	grids := map[string][][]string{}
	prev := map[string]json.RawMessage{}
	for _, n := range club.SectionWriteActions[action] {
		rows, ok, err := a.sheet(n, true)
		if err != nil {
			return err
		}
		prev[n] = json.RawMessage("null")
		if ok {
			grids[n] = rows
			b, _ := json.Marshal(rows)
			prev[n] = b
		}
	}
	changed, err := club.ApplySection(action, p, a.at, grids)
	if err != nil {
		if errors.Is(err, club.ErrNoSection) {
			return ErrNothingToApply
		}
		return err
	}
	for n, rows := range changed {
		b, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		a.undo = append(a.undo, undoStep{T: "club_sheets", N: n, Prev: prev[n]})
		if _, err := a.tx.Exec(a.ctx, `INSERT INTO club_sheets (name, rows) VALUES ($1,$2)
			ON CONFLICT (name) DO UPDATE SET rows = EXCLUDED.rows, at = now()`, n, b); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) sectionSeen(action string, p map[string]string) (*bool, error) {
	if ok, err := a.hasSections(); err != nil || !ok {
		return nil, err
	}
	grids := map[string][][]string{}
	for _, n := range club.SectionWriteActions[action] {
		rows, ok, err := a.sheet(n, false)
		if err != nil {
			return nil, err
		}
		if ok {
			grids[n] = rows
		}
	}
	return club.SectionSeen(action, p, a.at, grids), nil
}

// undoSheet puts a sheet back as it was (or removes it when it was not there).
func undoSheet(ctx context.Context, tx pgx.Tx, s undoStep) error {
	if s.N == "" {
		return errors.New("undo: sheet without a name")
	}
	if len(s.Prev) == 0 || string(s.Prev) == "null" {
		_, err := tx.Exec(ctx, `DELETE FROM club_sheets WHERE name = $1`, s.N)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO club_sheets (name, rows) VALUES ($1,$2)
		ON CONFLICT (name) DO UPDATE SET rows = EXCLUDED.rows, at = now()`, s.N, []byte(s.Prev))
	return err
}

// ResidentFields: what setResidentField may change, and the column it is.
var ResidentFields = map[string]string{
	"tariff": "tariff", "meetingsGranted": "meetings_granted", "meetingsDone": "meetings_done",
	"paidEntry": "paid_entry", "restEntry": "rest_entry", "renewDebt": "renew_debt",
	"months": "months", "chatId": "tg_id", "partner": "partner", "note": "note",
}

// setResidentField: {name, field, value} — one field of a resident, as the
// audit's fix sends it (the script writes the same cell).
func (a *applier) setResidentField(p map[string]string) error {
	name, field := strings.TrimSpace(p["name"]), strings.TrimSpace(p["field"])
	col, ok := ResidentFields[field]
	if !ok {
		return fmt.Errorf("поле «%s» не меняется", field)
	}
	r, err := a.resident(name)
	if err != nil {
		return err
	}
	v := strings.TrimSpace(p["value"])
	switch field {
	case "partner", "note":
		return a.update("club_residents", r.id, col+` = $2`, v)
	case "chatId":
		var id any
		if v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("Chat ID «%s» не число", v)
			}
			id = n
		}
		return a.update("club_residents", r.id, `tg_id = $2`, id)
	}
	n := int64(0)
	if v != "" {
		x, ok := club.Money(v)
		if !ok {
			return fmt.Errorf("«%s» не число", v)
		}
		n = x
	}
	if n < 0 {
		return fmt.Errorf("«%s»: меньше нуля", v)
	}
	return a.update("club_residents", r.id, col+` = $2`, n)
}
