// Package club holds the business rules of the club: residents and their
// debts, the cash journal (ДДС), fines, meetings and the P&L. The rules are
// pure functions, so they can be checked against the Google Sheet numbers
// without a database.
package club

import (
	"strings"
	"time"
)

// Resident is one row of «BS - резиденты дебет» (or «Бывшие резиденты»).
type Resident struct {
	Name      string     `json:"name"`
	TgID      int64      `json:"tgId,omitempty"`
	Format    string     `json:"format"` // «Онлайн» / «Офлайн»
	Tariff    int64      `json:"tariff"`
	Granted   int64      `json:"meetingsGranted"` // встреч оплачено
	Done      int64      `json:"meetingsDone"`    // встреч проведено
	PaidEntry int64      `json:"paidEntry"`       // оплачено (вход)
	RestEntry int64      `json:"restEntry"`       // остаток (вход)
	RenewDebt int64      `json:"renewDebt"`       // долг продление
	Former    bool       `json:"former"`
	Exception bool       `json:"exception"` // не штрафуется за отчёты
	Admin     bool       `json:"admin"`
	Source    string     `json:"source"`
	JoinedAt  *time.Time `json:"joinedAt,omitempty"`
	LeftAt    *time.Time `json:"leftAt,omitempty"`
	Months    int64      `json:"months"`
	Note      string     `json:"note"`
	Partner   string     `json:"partner"`
	// Archived: the row comes from «Бывшие резиденты», not from the debet
	// sheet. The app's bundle lists only the debet sheet.
	Archived bool `json:"archived,omitempty"`

	// What the sheet itself computed (formulas F, J, K). Only for the
	// import check; the server computes these on its own.
	SheetLeft  *int64 `json:"-"`
	SheetFines *int64 `json:"-"`
	SheetTotal *int64 `json:"-"`
}

// Payment is one row of «Учет ДДС».
type Payment struct {
	Row        int       `json:"row"` // row number in the sheet, for traceability
	Date       time.Time `json:"date"`
	Income     int64     `json:"income"`
	Expense    int64     `json:"expense"`
	IncomeCat  string    `json:"incomeCat"`  // «Источник» (статья дохода)
	Resident   string    `json:"resident"`   // «Категория +», фактически имя резидента
	ExpenseCat string    `json:"expenseCat"` // «Категория -»
	Applied    bool      `json:"applied"`    // «учтено»: приход разнесён по долгам
}

// Fine is one row of «Штрафы».
type Fine struct {
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Amount int64     `json:"amount"`
	Date   time.Time `json:"date"`
	Paid   bool      `json:"paid"`
	// Row is the sheet row (the app sends it back to find the fine); 0 for a
	// fine the server wrote that the sheet has not numbered yet.
	Row int `json:"row,omitempty"`
	// Status is the «Статус» cell as is ("Не оплатил" when empty).
	Status string `json:"status,omitempty"`
	// Owed: R69: what is still owed by the payments ledger (a part paid);
	// nil when not known (the sheet's data): then the whole amount.
	Owed *int64 `json:"-"`
}

// Due: what the resident still owes on the fine: nothing when it is paid or
// written off («Списан»), else what the ledger leaves of the amount.
func (f Fine) Due() int64 {
	st := strings.TrimSpace(f.Status)
	if f.Paid || st == "Оплатил" || st == "Списан" {
		return 0
	}
	if f.Owed != nil {
		if *f.Owed < 0 {
			return 0
		}
		return *f.Owed
	}
	return f.Amount
}

// Meeting is one row of «Расписание».
type Meeting struct {
	Resident string    `json:"resident"`
	Date     time.Time `json:"date"`
	Time     string    `json:"time"` // HH:MM
	Place    string    `json:"place"`
	Link     string    `json:"link"`
	Online   bool      `json:"online"`
	Sent3d   bool      `json:"sent3d"`
	Sent1d   bool      `json:"sent1d"`
	Sent1h   bool      `json:"sent1h"`
	Done     bool      `json:"done"`
	EventID  string    `json:"eventId"`
	// Status: R69: "done", "noshow" (не пришёл без предупреждения) or "".
	Status string `json:"status,omitempty"`
	// The sheet's row and its cells E, F and H as they are: the app's bundle
	// shows them so, without the clean-up done for Place and Link.
	Row      int    `json:"row,omitempty"`
	AddrCell string `json:"-"`
	LinkCell string `json:"-"`
	HCell    string `json:"-"`
}

// ReportEntry is one row of «Лог отчётов».
type ReportEntry struct {
	At       time.Time `json:"at"`
	Username string    `json:"username"`
	Name     string    `json:"name"`
	Text     string    `json:"text"`
	TgUserID int64     `json:"tgUserId"`
	Thread   string    `json:"thread"`
	Late     bool      `json:"late"`
	// ShownAt is column A («Дата») as the sheet shows it: the app's bundle
	// dates a report by it. Older rows carry the report's day at 23:59 there,
	// not the moment it was sent.
	ShownAt *time.Time `json:"shownAt,omitempty"`
}

// MeetingLogEntry is one row of «Лог встреч».
type MeetingLogEntry struct {
	Date     time.Time `json:"date"`
	Resident string    `json:"resident"`
	Time     string    `json:"time,omitempty"` // «Дата+время записи» as shown
}

// Setting is one bot text from «Настройки».
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PLRow is one line of the «PL» sheet as the sheet has it.
type PLRow struct {
	Name    string    `json:"name"`
	Section string    `json:"section"` // income, expense, total_income, total_expense, profit, dividends, cash
	Values  [12]int64 `json:"values"`
	Set     [12]bool  `json:"-"` // the sheet cell was not empty
}

// PLSheet is the whole «PL» sheet: its structure (which lines exist) and,
// for the import check, its numbers.
type PLSheet struct {
	Year int     `json:"year"`
	Rows []PLRow `json:"rows"`
}

// Snapshot is everything read from the sheet in one import.
type Snapshot struct {
	Residents  []Resident        `json:"residents"`
	Payments   []Payment         `json:"payments"`
	Fines      []Fine            `json:"fines"`
	Meetings   []Meeting         `json:"meetings"`
	Reports    []ReportEntry     `json:"reports"`
	MeetingLog []MeetingLogEntry `json:"meetingLog"`
	Settings   []Setting         `json:"settings"`
	PL         *PLSheet          `json:"pl,omitempty"`
	// Raw keeps the «PL» sheet and every sheet the server does not parse
	// (e.g. «Профили») as displayed: the app's bundle reads them as the
	// script does.
	Raw Sheets `json:"-"`
}
