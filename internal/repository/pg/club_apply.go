package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/jackc/pgx/v5"
)

// What a club write from the app does to the server's tables. The script
// still does the rest (calendar, messages to the resident, the sheet's own
// recalculations); the hourly import then brings the sheet's version.
//
// The server does what the action means, not the script's slips: three
// script functions write into the wrong columns (setMeetings and
// renewMeetings swap «оплачено» and «проведено», a resident's payment adds to
// «Тариф» and clears «проведено», addResident puts the partner into
// «Месяцев»). The sheet keeps those slips until the script is fixed; the
// import overwrites the server's numbers with them meanwhile.

// ClubWriteActions are the app's actions that change club data (the script's
// BS_CLUB_WRITE_ACTIONS).
var ClubWriteActions = map[string]bool{"addFine": true, "addSchedule": true, "addPayment": true, "confirmMeeting": true,
	"setPartner": true, "setMeetings": true, "renewMeetings": true, "addResident": true, "deleteSchedule": true,
	"updateMeeting": true, "addOfflineGroup": true, "updateFine": true, "deleteFine": true, "markAttendance": true,
	"approveResident": true, "convertToResident": true, "saveProfit": true}

// clubApplyActions change the club tables; the other write actions only
// touch sheets the server does not keep (subscribers, residents' profit).
var clubApplyActions = map[string]bool{"addFine": true, "addSchedule": true, "addPayment": true, "confirmMeeting": true,
	"setPartner": true, "setMeetings": true, "renewMeetings": true, "addResident": true, "deleteSchedule": true,
	"updateMeeting": true, "addOfflineGroup": true, "updateFine": true, "deleteFine": true, "markAttendance": true,
	"convertToResident": true}

// reapplyAfterSend: writes that are safe to put on top of an import again
// even if the sheet may already have them (they set a value by name, not add
// one or find a row by its number, which the copy may have moved).
var reapplyAfterSend = map[string]bool{"setPartner": true, "setMeetings": true, "updateMeeting": true}

// ErrNothingToApply: the action does not change the club tables here.
var ErrNothingToApply = errors.New("nothing to apply")

var undoTables = map[string]bool{"club_fines": true, "club_payments": true, "club_meetings": true,
	"club_meeting_log": true, "club_residents": true}

// undoStep brings one row back: Prev is the row before (null: it was inserted).
type undoStep struct {
	T    string          `json:"t"`
	ID   int64           `json:"id"`
	Prev json.RawMessage `json:"prev,omitempty"`
}

type applier struct {
	ctx  context.Context
	tx   pgx.Tx
	at   time.Time
	undo []undoStep
}

func (a *applier) insert(table, sql string, args ...any) (int64, error) {
	var id int64
	if err := a.tx.QueryRow(a.ctx, sql+` RETURNING id`, args...).Scan(&id); err != nil {
		return 0, err
	}
	a.undo = append(a.undo, undoStep{T: table, ID: id})
	return id, nil
}

func (a *applier) keep(table string, id int64) error {
	var prev []byte
	if err := a.tx.QueryRow(a.ctx, `SELECT to_jsonb(t) FROM `+table+` t WHERE id = $1 FOR UPDATE`, id).Scan(&prev); err != nil {
		return err
	}
	a.undo = append(a.undo, undoStep{T: table, ID: id, Prev: prev})
	return nil
}

// update runs "UPDATE table SET <set> WHERE id = $1" keeping the row before.
func (a *applier) update(table string, id int64, set string, args ...any) error {
	if err := a.keep(table, id); err != nil {
		return err
	}
	_, err := a.tx.Exec(a.ctx, `UPDATE `+table+` SET `+set+` WHERE id = $1`, append([]any{id}, args...)...)
	return err
}

func (a *applier) delete(table string, id int64) error {
	if err := a.keep(table, id); err != nil {
		return err
	}
	_, err := a.tx.Exec(a.ctx, `DELETE FROM `+table+` WHERE id = $1`, id)
	return err
}

// undoAll puts the rows back as they were, newest change first.
func undoAll(ctx context.Context, tx pgx.Tx, steps []undoStep) error {
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if !undoTables[s.T] {
			return fmt.Errorf("undo: table %q", s.T)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+s.T+` WHERE id = $1`, s.ID); err != nil {
			return err
		}
		if len(s.Prev) > 0 && string(s.Prev) != "null" {
			if _, err := tx.Exec(ctx, `INSERT INTO `+s.T+` SELECT * FROM jsonb_populate_record(NULL::`+s.T+`, $1)`, s.Prev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *applier) day() time.Time {
	t := a.at.In(club.Almaty)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, club.Almaty)
}

// appDate reads the app's "dd.MM.yyyy" or "dd.MM" (this year).
func (a *applier) appDate(s string) (time.Time, bool) {
	p := strings.Split(strings.TrimSpace(s), ".")
	if len(p) < 2 {
		return time.Time{}, false
	}
	d, _ := strconv.Atoi(p[0])
	m, _ := strconv.Atoi(p[1])
	y := a.at.In(club.Almaty).Year()
	if len(p) > 2 && p[2] != "" {
		y, _ = strconv.Atoi(p[2])
	}
	if d < 1 || d > 31 || m < 1 || m > 12 || y < 2000 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, club.Almaty), true
}

// appTime: "9:5" → "09:05", empty → def.
func appTime(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	p := strings.Split(s, ":")
	h, err := strconv.Atoi(p[0])
	if err != nil {
		h, _ = strconv.Atoi(strings.Split(def, ":")[0])
	}
	m := 0
	if len(p) > 1 {
		m, _ = strconv.Atoi(p[1])
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

func pint(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(strings.Split(s, ".")[0]), 10, 64)
	return n
}

// sameDay: a sheet date matches the app's "dd.MM.yyyy" or "dd.MM".
func sameDay(d time.Time, app string) bool {
	app = strings.TrimSpace(app)
	t := d.In(club.Almaty)
	return app != "" && (t.Format("02.01.2006") == app || t.Format("02.01") == app)
}

// tariffMonths is the script's _tariffToMonths.
func tariffMonths(sum int64) int64 {
	switch {
	case sum >= 1000000:
		return 12
	case sum >= 400000:
		return 3
	case sum > 0:
		return 1
	}
	return 3
}

type resRow struct {
	id                         int64
	name, partner, format      string
	tariff, granted, done      int64
	former, exception, archive bool
}

func (a *applier) residents() ([]resRow, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT id, name, partner, format, tariff, meetings_granted, meetings_done, former, exception, archived
		FROM club_residents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resRow
	for rows.Next() {
		var r resRow
		if err := rows.Scan(&r.id, &r.name, &r.partner, &r.format, &r.tariff, &r.granted, &r.done, &r.former, &r.exception, &r.archive); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resident finds a resident of the debet sheet by exact name, as the script does.
func (a *applier) resident(name string) (*resRow, error) {
	list, err := a.residents()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if !list[i].archive && strings.TrimSpace(list[i].name) == strings.TrimSpace(name) {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("резидент «%s» не найден", name)
}

type fineRow struct {
	id     int64
	row    int
	name   string
	typ    string
	amount int64
	date   *time.Time
	status string
}

func (a *applier) fines() ([]fineRow, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT id, COALESCE(sheet_row,0), resident, type, amount, date, status FROM club_fines
		ORDER BY COALESCE(sheet_row, 2147483647), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fineRow
	for rows.Next() {
		var f fineRow
		if err := rows.Scan(&f.id, &f.row, &f.name, &f.typ, &f.amount, &f.date, &f.status); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// findFine is the script's _bsFindFineRow: the app's row number if the name
// agrees, else the first fine with the same name, type, amount and date.
func (a *applier) findFine(p map[string]string) (*fineRow, error) {
	list, err := a.fines()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(p["name"])
	typ := strings.TrimSpace(p["kind"])
	if typ == "" {
		typ = strings.TrimSpace(p["type"])
	}
	if typ == "undefined" {
		typ = ""
	}
	amount := pint(p["amount"])
	date := strings.TrimSpace(p["date"])
	if row := int(pint(p["row"])); row >= 3 {
		for i := range list {
			if list[i].row == row && strings.TrimSpace(list[i].name) == name {
				return &list[i], nil
			}
		}
	}
	for i := range list {
		f := list[i]
		if strings.TrimSpace(f.name) != name || (typ != "" && strings.TrimSpace(f.typ) != typ) || (amount != 0 && f.amount != amount) {
			continue
		}
		if date != "" && f.date != nil && f.date.Format("02.01.2006") != date {
			continue
		}
		return &list[i], nil
	}
	return nil, errors.New("штраф не найден")
}

// deleteRows removes fines and moves the rows below up, as deleting sheet rows does.
func (a *applier) deleteFines(gone []fineRow) error {
	if len(gone) == 0 {
		return nil
	}
	var rows []int
	for _, f := range gone {
		if err := a.delete("club_fines", f.id); err != nil {
			return err
		}
		if f.row > 0 {
			rows = append(rows, f.row)
		}
	}
	rest, err := a.fines()
	if err != nil {
		return err
	}
	for _, f := range rest {
		shift := 0
		for _, r := range rows {
			if r < f.row {
				shift++
			}
		}
		if shift > 0 && f.row > 0 {
			if err := a.update("club_fines", f.id, `sheet_row = $2`, f.row-shift); err != nil {
				return err
			}
		}
	}
	return nil
}

type meetRow struct {
	id   int64
	row  int
	res  string
	date time.Time
	time string
}

func (a *applier) meetings() ([]meetRow, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT id, COALESCE(sheet_row,0), resident, date, time FROM club_meetings
		ORDER BY COALESCE(sheet_row, 2147483647), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []meetRow
	for rows.Next() {
		var m meetRow
		if err := rows.Scan(&m.id, &m.row, &m.res, &m.date, &m.time); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// findMeeting: resident, the app's date and, if given, the time (HH:MM).
func (a *applier) findMeeting(res, date, tm string) (*meetRow, error) {
	list, err := a.meetings()
	if err != nil {
		return nil, err
	}
	tm = strings.TrimSpace(tm)
	for i := range list {
		m := list[i]
		if strings.TrimSpace(m.res) != strings.TrimSpace(res) || !sameDay(m.date, date) {
			continue
		}
		if tm != "" && appTime(m.time, "00:00") != appTime(tm, "00:00") {
			continue
		}
		return &list[i], nil
	}
	return nil, errors.New("встреча не найдена")
}

func (a *applier) deleteMeeting(m *meetRow) error {
	if err := a.delete("club_meetings", m.id); err != nil {
		return err
	}
	if m.row == 0 {
		return nil
	}
	rest, err := a.meetings()
	if err != nil {
		return err
	}
	for _, x := range rest {
		if x.row > m.row {
			if err := a.update("club_meetings", x.id, `sheet_row = $2`, x.row-1); err != nil {
				return err
			}
		}
	}
	return nil
}

// meetingHappened: one more meeting done, a line in «Лог встреч» (once per
// day and person) and the schedule's row marked «Проведена».
func (a *applier) meetingHappened(name, date, tm string, matchTime bool) error {
	r, err := a.resident(name)
	if err != nil {
		return err
	}
	// Пакет закончился: тариф уходит в долг продления (как _addCheckForResident)
	renew := int64(0)
	if r.granted > 0 && r.done+1 >= r.granted && r.tariff > 0 {
		renew = r.tariff
	}
	if err := a.update("club_residents", r.id, `meetings_done = meetings_done + 1, renew_debt = renew_debt + $2,
		updated_at = now(), updated_by = 'server'`, renew); err != nil {
		return err
	}
	d := a.day()
	if strings.TrimSpace(date) != "" {
		if x, ok := a.appDate(date); ok {
			d = x
		}
	}
	var n int
	if err := a.tx.QueryRow(a.ctx, `SELECT count(*) FROM club_meeting_log WHERE date = $1 AND btrim(resident) = btrim($2)`,
		d.Format("2006-01-02"), name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := a.insert("club_meeting_log", `INSERT INTO club_meeting_log (date, resident, time_cell) VALUES ($1,$2,$3)`,
			d.Format("2006-01-02"), name, a.at.In(club.Almaty).Format("02.01.2006 15:04")); err != nil {
			return err
		}
	}
	list, err := a.meetings()
	if err != nil {
		return err
	}
	for _, m := range list {
		if strings.TrimSpace(m.res) != strings.TrimSpace(name) || !sameDay(m.date, date) {
			continue
		}
		if matchTime && strings.TrimSpace(tm) != "" && m.time != "" && appTime(m.time, "00:00") != appTime(tm, "00:00") {
			continue
		}
		if err := a.update("club_meetings", m.id, `done = true, sent_3d = true`); err != nil {
			return err
		}
		if matchTime {
			break // confirmMeeting marks one row, markAttendance every row of the day
		}
	}
	return nil
}

// offlineAddress: «Настройки» offline_address, as the script's getText.
func (a *applier) offlineAddress() string {
	var v string
	if err := a.tx.QueryRow(a.ctx, `SELECT value FROM club_settings WHERE key = 'offline_address'`).Scan(&v); err == nil && strings.TrimSpace(v) != "" {
		return v
	}
	return "г.Алматы, Достык 44"
}

// ApplyClubAction changes the club tables for one write; the returned undo
// steps bring them back. ErrNothingToApply: the action changes nothing here.
func applyClubAction(ctx context.Context, tx pgx.Tx, action string, p map[string]string, at time.Time) ([]undoStep, error) {
	if !clubApplyActions[action] {
		return nil, ErrNothingToApply
	}
	a := &applier{ctx: ctx, tx: tx, at: at}
	err := a.apply(action, p)
	return a.undo, err
}

func (a *applier) apply(action string, p map[string]string) error {
	day := a.day().Format("2006-01-02")
	switch action {
	case "addFine":
		name := strings.TrimSpace(p["name"])
		if name == "" {
			return errors.New("нет имени")
		}
		typ := p["type"]
		if typ == "" {
			typ = "Штраф"
		}
		amount := pint(p["amount"])
		if amount == 0 {
			amount = 10000
		}
		_, err := a.insert("club_fines", `INSERT INTO club_fines (resident, type, amount, date, paid, status, created_by)
			VALUES ($1,$2,$3,$4,false,'Не оплатил','server')`, name, typ, amount, day)
		return err

	case "updateFine":
		f, err := a.findFine(p)
		if err != nil {
			return err
		}
		st := p["status"]
		if st == "" {
			st = "Оплатил"
		}
		return a.update("club_fines", f.id, `status = $2, paid = ($2 = 'Оплатил'), paid_at = CASE WHEN $2 = 'Оплатил' THEN now() END`, st)

	case "deleteFine":
		f, err := a.findFine(p)
		if err != nil {
			return err
		}
		return a.deleteFines([]fineRow{*f})

	case "addPayment":
		amount := pint(p["amount"])
		src, res := p["src"], p["resident"]
		if amount <= 0 {
			return errors.New("нет суммы")
		}
		if p["type"] != "income" {
			_, err := a.insert("club_payments", `INSERT INTO club_payments (date, expense, expense_cat, source, created_by)
				VALUES ($1,$2,$3,'server','server')`, day, amount, src)
			return err
		}
		var fee int64
		feeCat := ""
		if p["isCash"] != "true" {
			fee, feeCat = (amount*4+50)/100, "Комиссия+налог" // банк: 4% комиссия и налог
		}
		if _, err := a.insert("club_payments", `INSERT INTO club_payments (date, income, expense, income_cat, resident, expense_cat, source, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,'server','server')`, day, amount, fee, src, res, feeCat); err != nil {
			return err
		}
		// Оплата штрафа закрывает неоплаченные штрафы резидента: строки удаляются
		if res != "" && strings.Contains(src, "Штраф") {
			list, err := a.fines()
			if err != nil {
				return err
			}
			remain := amount
			var gone []fineRow
			for _, f := range list {
				if remain <= 0 {
					break
				}
				if f.name == res && f.status != "Оплатил" && f.amount > 0 && remain >= f.amount {
					gone = append(gone, f)
					remain -= f.amount
				}
			}
			return a.deleteFines(gone)
		}
		// Оплата резидента продлевает пакет встреч (addMeetingsOnPayment): за каждый
		// оплаченный период пакет по тарифу, остаток (или перебор) переносится
		if res != "" {
			r, err := a.resident(res)
			if err != nil || r.tariff <= 0 || amount/r.tariff < 1 {
				return nil
			}
			total := tariffMonths(r.tariff)*3*(amount/r.tariff) + r.granted - r.done
			if total < 0 {
				total = 0
			}
			return a.update("club_residents", r.id, `meetings_done = 0, meetings_granted = $2`, total)
		}
		return nil

	case "addSchedule":
		res := strings.TrimSpace(p["res"])
		d, ok := a.appDate(p["date"])
		if res == "" || !ok {
			return errors.New("нужны резидент и дата")
		}
		// У резидента не бывает двух встреч в один день
		if _, err := a.findMeeting(res, d.Format("02.01.2006"), ""); err == nil {
			return nil
		}
		// Офлайн-резиденту скрипт пишет адрес вместо ссылки Meet (как _autoCreateMeet)
		addr := ""
		if r, err := a.resident(res); err != nil || r.format == "Офлайн" {
			addr = a.offlineAddress()
		}
		_, err := a.insert("club_meetings", `INSERT INTO club_meetings (resident, date, time, place, link_cell) VALUES ($1,$2,$3,$4,$4)`,
			res, d.Format("2006-01-02"), appTime(p["time"], "12:00"), addr)
		return err

	case "deleteSchedule":
		m, err := a.findMeeting(p["res"], p["date"], p["time"])
		if err != nil {
			return err
		}
		return a.deleteMeeting(m)

	case "updateMeeting":
		m, err := a.findMeeting(p["oldRes"], p["oldDate"], p["oldTime"])
		if err != nil {
			return err
		}
		set, args := []string{}, []any{}
		if nd := strings.TrimSpace(p["newDate"]); nd != "" {
			d, ok := a.appDate(nd)
			if !ok {
				return errors.New("новая дата не читается")
			}
			args = append(args, d.Format("2006-01-02"))
			set = append(set, fmt.Sprintf("date = $%d", len(args)+1))
		}
		if nt := strings.TrimSpace(p["newTime"]); nt != "" {
			args = append(args, appTime(nt, "00:00"))
			set = append(set, fmt.Sprintf("time = $%d", len(args)+1))
		}
		if len(set) == 0 {
			return nil
		}
		return a.update("club_meetings", m.id, strings.Join(set, ", "), args...)

	case "confirmMeeting":
		if strings.TrimSpace(p["res"]) == "" {
			return errors.New("нет резидента")
		}
		return a.meetingHappened(p["res"], p["date"], p["time"], true)

	case "markAttendance":
		var names []string
		for _, n := range strings.Split(p["names"], "|") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return errors.New("нет имён")
		}
		// Партнёр отмечается вместе с резидентом
		all, err := a.residents()
		if err != nil {
			return err
		}
		in := map[string]bool{}
		for _, n := range names {
			in[n] = true
		}
		for _, n := range append([]string(nil), names...) {
			for _, r := range all {
				if !r.archive && strings.TrimSpace(r.name) == n && r.partner != "" && !in[strings.TrimSpace(r.partner)] {
					in[strings.TrimSpace(r.partner)] = true
					names = append(names, strings.TrimSpace(r.partner))
				}
			}
		}
		for _, n := range names {
			if err := a.meetingHappened(n, p["date"], "", false); err != nil {
				return err
			}
		}
		return nil

	case "setPartner":
		name, partner := strings.TrimSpace(p["name"]), strings.TrimSpace(p["partner"])
		r, err := a.resident(name)
		if err != nil {
			return err
		}
		if partner == "" {
			if err := a.update("club_residents", r.id, `partner = ''`); err != nil {
				return err
			}
			if old := strings.TrimSpace(r.partner); old != "" {
				if o, err := a.resident(old); err == nil && strings.TrimSpace(o.partner) == name {
					return a.update("club_residents", o.id, `partner = ''`)
				}
			}
			return nil
		}
		o, err := a.resident(partner)
		if err != nil {
			return err
		}
		if err := a.update("club_residents", r.id, `partner = $2`, partner); err != nil {
			return err
		}
		return a.update("club_residents", o.id, `partner = $2`, name)

	case "setMeetings":
		r, err := a.resident(p["name"])
		if err != nil {
			return err
		}
		done, granted := pint(p["done"]), pint(p["granted"])
		return a.update("club_residents", r.id, `meetings_done = GREATEST($2::bigint, 0), meetings_granted = GREATEST($3::bigint, 0)`, done, granted)

	case "renewMeetings":
		// Новый пакет, неиспользованные встречи переносятся
		r, err := a.resident(p["name"])
		if err != nil {
			return err
		}
		pack := tariffMonths(r.tariff) * 3
		left := r.granted - r.done
		total := pack + left
		if total < 0 {
			total = 0
		}
		return a.update("club_residents", r.id, `meetings_done = 0, meetings_granted = $2`, total)

	case "addResident", "convertToResident":
		name := strings.TrimSpace(p["name"])
		if name == "" {
			return errors.New("нет имени")
		}
		if _, err := a.resident(name); err == nil {
			return nil // уже в списке
		}
		debt := pint(p["debt"])
		visits := pint(p["visits"])
		if visits == 0 {
			visits = 3
		}
		format := strings.TrimSpace(p["format"])
		if format == "" {
			format = "Офлайн"
		}
		source, tg := p["source"], pint(p["residentChatId"])
		if action == "convertToResident" {
			source, tg = "Telegram канал", pint(p["chatId"])
		}
		var tgID any
		if tg > 0 {
			tgID = tg
		}
		partner := strings.TrimSpace(p["partner"])
		if _, err := a.insert("club_residents", `INSERT INTO club_residents (name, tg_id, format, tariff, meetings_granted, meetings_done,
			paid_entry, rest_entry, renew_debt, source, joined_at, partner, updated_by)
			VALUES ($1,$2,$3,$4,$5,0,0,$6,0,$7,$8,$9,'server')`,
			name, tgID, format, debt, tariffMonths(debt)*visits, debt, source, day, partner); err != nil {
			return err
		}
		if partner != "" {
			if o, err := a.resident(partner); err == nil && strings.TrimSpace(o.partner) == "" {
				return a.update("club_residents", o.id, `partner = $2`, name)
			}
		}
		return nil

	case "addOfflineGroup":
		d, ok := a.appDate(p["date"])
		if !ok || strings.TrimSpace(p["time"]) == "" {
			return errors.New("нужны дата и время")
		}
		addr := a.offlineAddress()
		list, err := a.residents()
		if err != nil {
			return err
		}
		n := 0
		for _, r := range list {
			f := strings.TrimSpace(r.format)
			if r.archive || r.former || r.exception || r.name == "" || (f != "" && f != "Офлайн") {
				continue
			}
			if _, err := a.insert("club_meetings", `INSERT INTO club_meetings (resident, date, time, place, link_cell) VALUES ($1,$2,$3,$4,$4)`,
				r.name, d.Format("2006-01-02"), appTime(p["time"], "12:00"), addr); err != nil {
				return err
			}
			n++
		}
		if n == 0 {
			return errors.New("нет офлайн-резидентов")
		}
		return nil
	}
	return ErrNothingToApply
}

// seenInSheet: the import already has what this write adds. Defined for the
// writes that add rows; nil for the others.
func seenInSheet(ctx context.Context, q pgx.Tx, action string, p map[string]string, at time.Time) (*bool, error) {
	a := &applier{ctx: ctx, tx: q, at: at}
	day := a.day().Format("2006-01-02")
	var n int
	var err error
	switch action {
	case "addFine":
		amount := pint(p["amount"])
		if amount == 0 {
			amount = 10000
		}
		err = q.QueryRow(ctx, `SELECT count(*) FROM club_fines WHERE btrim(resident) = btrim($1) AND amount = $2 AND date = $3`,
			p["name"], amount, day).Scan(&n)
	case "addPayment":
		col, cat := "expense", "expense_cat"
		if p["type"] == "income" {
			col, cat = "income", "income_cat"
		}
		err = q.QueryRow(ctx, `SELECT count(*) FROM club_payments WHERE date = $1 AND `+col+` = $2 AND btrim(`+cat+`) = btrim($3)`,
			day, pint(p["amount"]), p["src"]).Scan(&n)
	case "addSchedule":
		d, ok := a.appDate(p["date"])
		if !ok {
			return nil, nil
		}
		err = q.QueryRow(ctx, `SELECT count(*) FROM club_meetings WHERE btrim(resident) = btrim($1) AND date = $2`,
			p["res"], d.Format("2006-01-02")).Scan(&n)
	case "addResident", "convertToResident":
		err = q.QueryRow(ctx, `SELECT count(*) FROM club_residents WHERE NOT archived AND btrim(name) = btrim($1)`, p["name"]).Scan(&n)
	case "confirmMeeting", "markAttendance":
		d := a.day()
		if x, ok := a.appDate(p["date"]); ok {
			d = x
		}
		names := strings.Split(p["names"], "|")
		if action == "confirmMeeting" {
			names = []string{p["res"]}
		}
		want := 0
		for _, nm := range names {
			if strings.TrimSpace(nm) == "" {
				continue
			}
			want++
			var c int
			if err = q.QueryRow(ctx, `SELECT count(*) FROM club_meeting_log WHERE date = $1 AND btrim(resident) = btrim($2)`,
				d.Format("2006-01-02"), nm).Scan(&c); err != nil {
				return nil, err
			}
			if c > 0 {
				n++
			}
		}
		yes := want > 0 && n == want
		return &yes, nil
	case "addOfflineGroup":
		d, ok := a.appDate(p["date"])
		if !ok {
			return nil, nil
		}
		err = q.QueryRow(ctx, `SELECT count(*) FROM club_meetings WHERE date = $1 AND time = $2`,
			d.Format("2006-01-02"), appTime(p["time"], "12:00")).Scan(&n)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	yes := n > 0
	return &yes, nil
}
