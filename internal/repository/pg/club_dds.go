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

// R32a: the cash journal (ДДС) edited on the platform like a spreadsheet.
// Rows are read and written by id; a batch of changes (cells edited, rows
// added, pasted or deleted) goes in one transaction. Only once the server
// keeps the club's data (master = server): before that the hourly import
// from the sheet replaces the table and would drop the edits.

// DDSRow is one row of the journal as the grid shows it.
type DDSRow struct {
	ID         int64  `json:"id"`
	SheetRow   int    `json:"row,omitempty"`
	Date       string `json:"date"` // 2006-01-02
	Income     int64  `json:"income"`
	Expense    int64  `json:"expense"`
	IncomeCat  string `json:"incomeCat"`
	Resident   string `json:"resident"`
	ExpenseCat string `json:"expenseCat"`
	Account    string `json:"account"`
	Comment    string `json:"comment"`
	Applied    bool   `json:"applied,omitempty"`
	Source     string `json:"source,omitempty"`
	UpdatedBy  string `json:"updatedBy,omitempty"`
}

// DDSOp is one change: insert (cid + set), update (id + set) or delete (id).
type DDSOp struct {
	Op  string                     `json:"op"`
	ID  int64                      `json:"id,omitempty"`
	CID string                     `json:"cid,omitempty"`
	Set map[string]json.RawMessage `json:"set,omitempty"`
}

// DDSResult: the new ids by the client's cid and the rows as they are now.
type DDSResult struct {
	IDs     map[string]int64 `json:"ids"`
	Rows    []DDSRow         `json:"rows"`
	Deleted []int64          `json:"deleted"`
}

// ErrDDSInput is a change the journal does not take (bad date, negative sum…).
type ErrDDSInput struct{ Msg string }

func (e *ErrDDSInput) Error() string { return e.Msg }

const ddsCols = `id, COALESCE(sheet_row,0), date, income, expense, income_cat, resident, expense_cat, account, comment, applied, source, updated_by`

func scanDDS(row pgx.Row) (DDSRow, error) {
	var r DDSRow
	var d time.Time
	err := row.Scan(&r.ID, &r.SheetRow, &d, &r.Income, &r.Expense, &r.IncomeCat, &r.Resident, &r.ExpenseCat, &r.Account, &r.Comment, &r.Applied, &r.Source, &r.UpdatedBy)
	r.Date = d.Format("2006-01-02")
	return r, err
}

// DDSRows: the whole journal in the sheet's order (date, then as entered).
func (r *ClubRepo) DDSRows(ctx context.Context) ([]DDSRow, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+ddsCols+` FROM club_payments ORDER BY date, COALESCE(sheet_row, 2147483647), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DDSRow{}
	for rows.Next() {
		x, err := scanDDS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ParseDDSDate reads what a person types or pastes into the date column:
// 30.09.2026, 30.09.26, 30.09 (this year), 2026-09-30, 30/09/2026.
func ParseDDSDate(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation("2006-01-02", s, club.Almaty); err == nil {
		return t, true
	}
	if len(s) > 10 && s[4] == '-' { // 2026-09-30T00:00:00+05:00
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			t = t.In(club.Almaty)
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, club.Almaty), true
		}
	}
	s = strings.NewReplacer("/", ".", "-", ".").Replace(s)
	p := strings.Split(s, ".")
	if len(p) < 2 || len(p) > 3 {
		return time.Time{}, false
	}
	d, e1 := strconv.Atoi(strings.TrimSpace(p[0]))
	m, e2 := strconv.Atoi(strings.TrimSpace(p[1]))
	y := now.In(club.Almaty).Year()
	var e3 error
	if len(p) == 3 && strings.TrimSpace(p[2]) != "" {
		y, e3 = strconv.Atoi(strings.TrimSpace(p[2]))
		if y < 100 {
			y += 2000
		}
	}
	if e1 != nil || e2 != nil || e3 != nil || m < 1 || m > 12 || d < 1 || d > 31 || y < 2000 || y > 2100 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, club.Almaty)
	if t.Day() != d { // 31.02
		return time.Time{}, false
	}
	return t, true
}

// ParseDDSMoney: 100 000, 100000, "100 000 ₸", 1 500,50 → whole tenge.
func ParseDDSMoney(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c == '-':
			b.WriteRune(c)
		case c == ',' || c == '.':
			b.WriteRune('.')
		}
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	if err != nil || f < 0 || f > 1e13 {
		return 0, false
	}
	return int64(f + 0.5), true
}

func ddsText(raw json.RawMessage, max int) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return "", errors.New("text expected")
		}
		s = n.String()
	}
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max])
	}
	return s, nil
}

func ddsMoney(raw json.RawMessage) (int64, bool) {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n < 0 || n > 1e13 {
			return 0, false
		}
		return int64(n + 0.5), true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0, false
	}
	return ParseDDSMoney(s)
}

var ddsField = map[string]string{"date": "date", "income": "income", "expense": "expense", "incomeCat": "income_cat",
	"resident": "resident", "expenseCat": "expense_cat", "account": "account", "comment": "comment"}

// ddsSet turns the client's fields into column = value, checked.
func ddsSet(set map[string]json.RawMessage, now time.Time) (map[string]any, error) {
	out := map[string]any{}
	for k, raw := range set {
		col, ok := ddsField[k]
		if !ok {
			return nil, &ErrDDSInput{"неизвестная колонка " + k}
		}
		switch k {
		case "date":
			s, _ := ddsText(raw, 40)
			t, ok := ParseDDSDate(s, now)
			if !ok {
				return nil, &ErrDDSInput{"дата «" + s + "»: нужна в виде 30.09.2026"}
			}
			out[col] = t.Format("2006-01-02")
		case "income", "expense":
			v, ok := ddsMoney(raw)
			if !ok {
				return nil, &ErrDDSInput{"сумма: целое число тенге от 0"}
			}
			out[col] = v
		case "comment":
			s, err := ddsText(raw, 1000)
			if err != nil {
				return nil, &ErrDDSInput{"комментарий: текст"}
			}
			out[col] = s
		default:
			s, err := ddsText(raw, 200)
			if err != nil {
				return nil, &ErrDDSInput{k + ": текст"}
			}
			out[col] = s
		}
	}
	return out, nil
}

// DDSApply makes the changes in one transaction: all or nothing.
func (r *ClubRepo) DDSApply(ctx context.Context, ops []DDSOp, who string, now time.Time) (*DDSResult, error) {
	if len(ops) == 0 {
		return &DDSResult{IDs: map[string]int64{}, Rows: []DDSRow{}, Deleted: []int64{}}, nil
	}
	if len(ops) > 5000 {
		return nil, &ErrDDSInput{"за раз не больше 5 000 изменений"}
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res := &DDSResult{IDs: map[string]int64{}, Rows: []DDSRow{}, Deleted: []int64{}}
	a := &applier{ctx: ctx, tx: tx, at: now}
	money := map[int64]bool{} // R69: rows whose sum, category, resident or date changed
	touched := map[int64]bool{}
	var order []int64
	touch := func(id int64) {
		if !touched[id] {
			touched[id] = true
			order = append(order, id)
		}
	}
	for i, op := range ops {
		set, err := ddsSet(op.Set, now)
		if err != nil {
			var in *ErrDDSInput
			if errors.As(err, &in) {
				return nil, &ErrDDSInput{fmt.Sprintf("строка %d: %s", i+1, in.Msg)}
			}
			return nil, err
		}
		switch op.Op {
		case "insert":
			if _, ok := set["date"]; !ok {
				set["date"] = now.In(club.Almaty).Format("2006-01-02")
			}
			cols := []string{"source", "created_by", "updated_at", "updated_by"}
			vals := []any{"platform", who, now, who}
			for c, v := range set {
				cols = append(cols, c)
				vals = append(vals, v)
			}
			ph := make([]string, len(vals))
			for k := range vals {
				ph[k] = "$" + strconv.Itoa(k+1)
			}
			var id int64
			if err := tx.QueryRow(ctx, `INSERT INTO club_payments (`+strings.Join(cols, ", ")+`) VALUES (`+strings.Join(ph, ", ")+`) RETURNING id`, vals...).Scan(&id); err != nil {
				return nil, err
			}
			if op.CID != "" {
				res.IDs[op.CID] = id
			}
			touch(id)
		case "update":
			if op.ID <= 0 {
				return nil, &ErrDDSInput{fmt.Sprintf("строка %d: нет id", i+1)}
			}
			if len(set) == 0 {
				continue
			}
			for _, k := range []string{"income", "income_cat", "resident", "date"} {
				if _, ok := set[k]; ok {
					money[op.ID] = true
				}
			}
			if _, ok := set["resident"]; ok {
				set["resident_id"] = nil // another name: matched again
			}
			parts := []string{"updated_at = $2", "updated_by = $3"}
			args := []any{op.ID, now, who}
			for c, v := range set {
				args = append(args, v)
				parts = append(parts, c+" = $"+strconv.Itoa(len(args)))
			}
			ct, err := tx.Exec(ctx, `UPDATE club_payments SET `+strings.Join(parts, ", ")+` WHERE id = $1`, args...)
			if err != nil {
				return nil, err
			}
			if ct.RowsAffected() == 0 {
				return nil, &ErrDDSInput{fmt.Sprintf("строка %d: её уже удалили, обновите страницу", i+1)}
			}
			touch(op.ID)
		case "delete":
			if op.ID <= 0 {
				return nil, &ErrDDSInput{fmt.Sprintf("строка %d: нет id", i+1)}
			}
			// R69: what the row paid owes again (its ledger lines go back)
			if _, err := a.unallocate(op.ID); err != nil {
				return nil, err
			}
			// Deleting a row that is already gone is not an error (two people, a retry)
			if _, err := tx.Exec(ctx, `DELETE FROM club_payments WHERE id = $1`, op.ID); err != nil {
				return nil, err
			}
			res.Deleted = append(res.Deleted, op.ID)
			delete(touched, op.ID)
		default:
			return nil, &ErrDDSInput{fmt.Sprintf("строка %d: действие %q", i+1, op.Op)}
		}
	}
	// R69: a new row, or an edit of its sum, category, resident or date, is
	// applied by the one model (club_alloc.go): no time window, the ledger
	// says what the row already paid, an edit takes it back first.
	inserted := map[int64]bool{}
	for _, id := range res.IDs {
		inserted[id] = true
	}
	for _, id := range order {
		if !touched[id] || (!inserted[id] && !money[id]) {
			continue
		}
		var cnt int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM club_payments WHERE id = $1`, id).Scan(&cnt); err != nil || cnt == 0 {
			continue
		}
		if _, err := a.autoAllocate(id, !inserted[id], who); err != nil {
			return nil, err
		}
	}
	for _, id := range order {
		if !touched[id] {
			continue
		}
		row, err := scanDDS(tx.QueryRow(ctx, `SELECT `+ddsCols+` FROM club_payments WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if row.Income < 0 || row.Expense < 0 {
			return nil, &ErrDDSInput{"сумма не может быть отрицательной"}
		}
		res.Rows = append(res.Rows, row)
	}
	return res, tx.Commit(ctx)
}
