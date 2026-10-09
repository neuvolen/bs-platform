package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R69: «если резидент не пришёл на встречу, не предупредив, ему штраф 50к».
// missMeeting marks the meeting «Не пришёл без предупреждения» (next to
// «Встреча прошла» and «Перенести»): the meeting is not counted, a fine of
// NoShowFine is written once (the meeting's day and type) and the resident
// gets one bot message (bot: maybeNoShowNotes, by notified_at).

// NoShowType is the fine's type; NoShowFine its default sum (the setting
// noshow_fine overrides it).
const (
	NoShowType = "Неявка без предупреждения"
	NoShowFine = int64(50000)
)

func init() {
	ClubWriteActions["missMeeting"] = true
	extraApply["missMeeting"] = (*applier).missMeeting
}

func (a *applier) noShowSum() int64 {
	var v string
	if err := a.tx.QueryRow(a.ctx, `SELECT value FROM club_settings WHERE key = 'noshow_fine'`).Scan(&v); err == nil {
		if n, ok := ParseDDSMoney(v); ok && n > 0 {
			return n
		}
	}
	return NoShowFine
}

func (a *applier) missMeeting(p map[string]string) error {
	name := strings.TrimSpace(p["res"])
	if name == "" {
		return errors.New("нет резидента")
	}
	r, err := a.resident(name)
	if err != nil {
		return err
	}
	d, ok := a.appDate(p["date"])
	if !ok {
		return errors.New("нет даты встречи")
	}
	tm := strings.TrimSpace(p["time"])
	var meetID int64
	list, err := a.meetings()
	if err != nil {
		return err
	}
	for _, m := range list {
		if (club.NormName(m.res) != club.NormName(name) && club.NormName(m.res) != club.NormName(r.name)) || !sameDay(m.date, d.Format("02.01.2006")) {
			continue
		}
		if tm != "" && m.time != "" && appTime(m.time, "00:00") != appTime(tm, "00:00") {
			continue
		}
		meetID = m.id
		if tm == "" {
			tm = m.time
		}
		if err := a.update("club_meetings", m.id, `status = 'noshow', done = false`); err != nil {
			return err
		}
		break
	}
	// once: the same resident, day and type
	rows, err := a.tx.Query(a.ctx, `SELECT resident FROM club_fines WHERE type = $1 AND date = $2`, NoShowType, d.Format("2006-01-02"))
	if err != nil {
		return err
	}
	var have []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have = append(have, n)
	}
	rows.Close()
	for _, n := range have {
		if a.fineOwner(n, r) {
			return nil
		}
	}
	note := "Встреча " + d.Format("02.01.2006")
	if tm != "" {
		note += " " + appTime(tm, "00:00")
	}
	var mid any
	if meetID > 0 {
		mid = meetID
	}
	if _, err := a.insert("club_fines", `INSERT INTO club_fines (resident, type, amount, date, paid, status, created_by, note, meeting_id)
		VALUES ($1,$2,$3,$4,false,'Не оплатил','server',$5,$6)`, r.name, NoShowType, a.noShowSum(), d.Format("2006-01-02"), note, mid); err != nil {
		return err
	}
	return a.finePrepaid(r.name)
}

// NoShowNote: a no-show fine the resident was not told about yet.
type NoShowNote struct {
	ID       int64
	Resident string
	TgID     int64
	Amount   int64
	Note     string
}

// NoShowPending: the no-show fines of the last 3 days nobody told the resident about.
func (r *ClubRepo) NoShowPending(ctx context.Context) ([]NoShowNote, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT f.id, f.resident, f.amount, f.note,
			COALESCE((SELECT tg_id FROM club_residents c WHERE lower(btrim(c.name)) = lower(btrim(f.resident)) AND NOT c.archived ORDER BY c.id LIMIT 1), 0)
		FROM club_fines f WHERE f.type = $1 AND f.notified_at IS NULL AND f.created_at > now() - interval '3 days' AND f.status <> 'Списан'
		ORDER BY f.id`, NoShowType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoShowNote
	for rows.Next() {
		var n NoShowNote
		if err := rows.Scan(&n.ID, &n.Resident, &n.Amount, &n.Note, &n.TgID); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimNoShowNote marks the fine as told; false: someone already did.
func (r *ClubRepo) ClaimNoShowNote(ctx context.Context, id int64) (bool, error) {
	ct, err := r.db.Pool.Exec(ctx, `UPDATE club_fines SET notified_at = $2 WHERE id = $1 AND notified_at IS NULL`, id, time.Now())
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// NoShowText: the resident's message.
func NoShowText(name string, amount int64, note, kaspi string) string {
	first := strings.Fields(strings.TrimSpace(name))
	who := name
	if len(first) > 0 {
		who = first[0]
	}
	return fmt.Sprintf("⚠️ %s, вы не пришли на встречу и не предупредили.\n\n%s\nШтраф: %s тг (правило клуба: неявка без предупреждения)\n\nОплатить: %s",
		who, note, club.FmtMoney(amount), kaspi)
}
