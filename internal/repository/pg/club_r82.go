package pg

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R82: «Я как админ не вижу "Бывшие резиденты" и соответственно данные
// оттуда. Возможно, если кто-то стал бывшим, он может вернуться».
//
// FormerResidents: everyone marked «Бывший» (former) or moved to the sheet's
// «Бывшие резиденты» (archived), with what the club knows about them: the
// package, the debt, the fines, the payments, the meetings. Nothing is
// removed when someone leaves, so all of it is there.
//
// ReturnResident («Вернуть в резиденты»): the person is a resident again.
// The history stays (payments, fines, meetings, boards, the entry date, the
// debt); a new package starts today the way «Продлить пакет» starts one
// (granted = months of the tariff × 3, «проведено» 0 from today), the note
// gets «вернулся в клуб DD.MM.YYYY».

// FormerPay is one income row of a former resident.
type FormerPay struct {
	Date   string `json:"date"`
	Amount int64  `json:"amount"`
	Cat    string `json:"cat"`
}

// FormerFine is one fine of a former resident.
type FormerFine struct {
	Date   string `json:"date"`
	Type   string `json:"type"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

// FormerRow is one former resident.
type FormerRow struct {
	ID        int64        `json:"id"`
	Name      string       `json:"name"`
	Format    string       `json:"format"`
	Tariff    int64        `json:"tariff"`
	Joined    string       `json:"joined,omitempty"` // DD.MM.YYYY
	Left      string       `json:"left,omitempty"`
	Months    int64        `json:"months"`
	Granted   int64        `json:"granted"`
	Done      int64        `json:"done"`
	Debt      int64        `json:"debt"`
	FinesOpen int64        `json:"finesOpen"`
	PaidTotal int64        `json:"paidTotal"`
	Meetings  int          `json:"meetings"`           // days in the attendance log
	LastMeet  string       `json:"lastMeet,omitempty"` // DD.MM.YYYY
	Note      string       `json:"note,omitempty"`
	Archived  bool         `json:"archived,omitempty"` // from the sheet's «Бывшие резиденты»
	Partner   string       `json:"partner,omitempty"`
	Pays      []FormerPay  `json:"pays"`
	Fines     []FormerFine `json:"fines"`
}

func ddmmyyyy(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format("02.01.2006")
}

// FormerResidents lists the former residents (newest leavers first).
func (r *ClubRepo) FormerResidents(ctx context.Context) ([]FormerRow, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, name, format, tariff, joined_at, left_at, months, meetings_granted, meetings_done,
			GREATEST(rest_entry,0) + GREATEST(renew_debt,0), note, archived, partner, aliases
		FROM club_residents WHERE (former OR archived) AND NOT admin ORDER BY left_at DESC NULLS LAST, name`)
	if err != nil {
		return nil, err
	}
	out := []FormerRow{}
	idx := map[string]int{} // normalized name or alias → position
	for rows.Next() {
		var x FormerRow
		var joined, left *time.Time
		var aliases string
		if err := rows.Scan(&x.ID, &x.Name, &x.Format, &x.Tariff, &joined, &left, &x.Months, &x.Granted, &x.Done, &x.Debt, &x.Note, &x.Archived, &x.Partner, &aliases); err != nil {
			rows.Close()
			return nil, err
		}
		x.Joined, x.Left = ddmmyyyy(joined), ddmmyyyy(left)
		x.Pays, x.Fines = []FormerPay{}, []FormerFine{}
		out = append(out, x)
		i := len(out) - 1
		for _, n := range append([]string{x.Name}, strings.Split(aliases, ",")...) {
			if k := club.NormName(n); k != "" {
				if _, taken := idx[k]; !taken {
					idx[k] = i
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	byID := map[int64]int{}
	for i, x := range out {
		byID[x.ID] = i
	}
	// payments (by the row's resident id, else the name)
	prow, err := r.db.Pool.Query(ctx, `SELECT date, income, income_cat, resident, resident_id FROM club_payments WHERE income > 0 ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		var d time.Time
		var p FormerPay
		var name string
		var rid *int64
		if err := prow.Scan(&d, &p.Amount, &p.Cat, &name, &rid); err != nil {
			prow.Close()
			return nil, err
		}
		i, ok := -1, false
		if rid != nil {
			i, ok = byID[*rid]
		}
		if !ok && (rid == nil || *rid <= 0) {
			i, ok = idx[club.NormName(name)]
		}
		if !ok || PayKind(p.Cat) == "" {
			continue
		}
		p.Date = d.Format("02.01.2006")
		out[i].PaidTotal += p.Amount
		if len(out[i].Pays) < 30 {
			out[i].Pays = append(out[i].Pays, p)
		}
	}
	prow.Close()
	// fines
	frow, err := r.db.Pool.Query(ctx, `SELECT f.resident, COALESCE(f.date, f.created_at::date), f.type, f.amount, f.status, f.paid,
			COALESCE((SELECT sum(amount) FROM club_pay_alloc WHERE fine_id = f.id), 0)
		FROM club_fines f ORDER BY COALESCE(f.date, f.created_at::date) DESC, f.id DESC`)
	if err != nil {
		return nil, err
	}
	for frow.Next() {
		var name, st string
		var d time.Time
		var f FormerFine
		var paid bool
		var part int64
		if err := frow.Scan(&name, &d, &f.Type, &f.Amount, &st, &paid, &part); err != nil {
			frow.Close()
			return nil, err
		}
		i, ok := idx[club.NormName(name)]
		if !ok {
			continue
		}
		f.Date = d.Format("02.01.2006")
		switch {
		case strings.TrimSpace(st) == "Списан":
			f.Status = "Списан"
		case st == "Оплатил" || paid:
			f.Status = "Оплатил"
		default:
			f.Status = "Не оплатил"
			if left := f.Amount - part; left > 0 {
				out[i].FinesOpen += left
			}
		}
		out[i].Fines = append(out[i].Fines, f)
	}
	frow.Close()
	// meetings: the attendance log
	mrow, err := r.db.Pool.Query(ctx, `SELECT resident, count(DISTINCT date), max(date) FROM club_meeting_log GROUP BY resident`)
	if err != nil {
		return nil, err
	}
	for mrow.Next() {
		var name string
		var n int
		var last time.Time
		if err := mrow.Scan(&name, &n, &last); err != nil {
			mrow.Close()
			return nil, err
		}
		if i, ok := idx[club.NormName(name)]; ok {
			out[i].Meetings += n
			if l := last.Format("2006-01-02"); out[i].LastMeet == "" || l > isoDay(out[i].LastMeet) {
				out[i].LastMeet = last.Format("02.01.2006")
			}
		}
	}
	mrow.Close()
	return out, mrow.Err()
}

// isoDay: DD.MM.YYYY → YYYY-MM-DD (for comparing).
func isoDay(s string) string {
	p := strings.Split(s, ".")
	if len(p) != 3 {
		return s
	}
	return p[2] + "-" + p[1] + "-" + p[0]
}

// ReturnResult is what «Вернуть в резиденты» did.
type ReturnResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Format  string `json:"format"`
	Tariff  int64  `json:"tariff"`
	Granted int64  `json:"granted"`
	From    string `json:"from"` // the new package starts (DD.MM.YYYY)
	Debt    int64  `json:"debt"` // kept as it was
}

// ReturnResident makes a former resident a resident again. format and
// tariff: optional (empty / 0 keeps the old one).
func (r *ClubRepo) ReturnResident(ctx context.Context, id int64, format string, tariff int64, by string) (*ReturnResult, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := &applier{ctx: ctx, tx: tx, at: time.Now()}
	res, err := a.residentByID(id)
	if err != nil {
		return nil, &ErrLinkInput{err.Error()}
	}
	var admin bool
	var debt int64
	if err := tx.QueryRow(ctx, `SELECT admin, GREATEST(rest_entry,0) + GREATEST(renew_debt,0) FROM club_residents WHERE id = $1`, id).Scan(&admin, &debt); err != nil {
		return nil, err
	}
	if admin {
		return nil, &ErrLinkInput{"это сотрудник клуба, не резидент"}
	}
	if !res.former && !res.archive {
		return nil, &ErrLinkInput{"«" + res.name + "» уже резидент"}
	}
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	for _, o := range list {
		if o.id != id && !o.former && !o.archive && club.NormName(o.name) == club.NormName(res.name) {
			return nil, &ErrLinkInput{"среди резидентов уже есть «" + o.name + "»"}
		}
	}
	switch f := strings.ToLower(strings.TrimSpace(format)); {
	case f == "":
		format = res.format
	case strings.HasPrefix(f, "онлайн"):
		format = "Онлайн"
	case strings.HasPrefix(f, "офлайн"):
		format = "Офлайн"
	default:
		return nil, &ErrLinkInput{"формат: Онлайн или Офлайн"}
	}
	if tariff < 0 {
		return nil, &ErrLinkInput{"тариф не может быть меньше нуля"}
	}
	if tariff == 0 {
		tariff = res.tariff
	}
	granted := tariffMonths(tariff) * 3
	day := a.day()
	note := strings.TrimSpace(resNote(a, id))
	mark := "вернулся в клуб " + day.Format("02.01.2006")
	if note != "" {
		note += "; " + mark
	} else {
		note = mark
	}
	if err := a.update("club_residents", id, `former = false, archived = false, left_at = NULL, format = $2, tariff = $3,
		meetings_granted = $4, note = $5, updated_at = now(), updated_by = 'return'`, format, tariff, granted, note); err != nil {
		return nil, err
	}
	res.former, res.archive, res.format, res.tariff, res.granted = false, false, format, tariff, granted
	if err := a.resetToday(res); err != nil { // the package starts today, «проведено» 0
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := &ReturnResult{ID: id, Name: res.name, Format: format, Tariff: tariff, Granted: granted, From: day.Format("02.01.2006"), Debt: debt}
	log.Printf("r82 return: %s снова резидент (%s, тариф %s ₸, пакет %d встреч с %s, долг %s ₸ сохранён) by %s",
		res.name, format, club.FmtMoney(tariff), granted, out.From, club.FmtMoney(debt), by)
	return out, nil
}

// resNote: the resident's note now.
func resNote(a *applier, id int64) string {
	var n string
	_ = a.tx.QueryRow(a.ctx, `SELECT note FROM club_residents WHERE id = $1`, id).Scan(&n)
	return n
}
