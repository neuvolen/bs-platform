package bot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Report rules, as the Apps Script bot applies them (BS_Script, _upd, group part):
//   - a report is a text message in the group's ОТЧЁТЫ topic from a resident;
//   - at least 100 characters (counted as JavaScript counts them);
//   - it counts for the Almaty day it was sent; sent between 00:00 and 06:00
//     it is late and counts for no day;
//   - video notes, videos, commands and empty texts are not reports.
const (
	DefaultGroupID      = -1002494126345
	DefaultReportsTopic = "9"
	MinReportLen        = 100
	LateBeforeHour      = 6
)

// Verdicts of the server bot on a group message.
const (
	VerdictReport      = "report"
	VerdictShort       = "short"
	VerdictWrongTopic  = "wrong_topic"
	VerdictNotResident = "not_resident"
)

// GroupMessage is the part of a Telegram update the report rules look at.
type GroupMessage struct {
	UpdateID  int64
	MessageID int64
	ChatID    int64
	Sent      time.Time
	Thread    string
	FromID    int64
	FirstName string
	LastName  string
	Username  string
	Text      string // trimmed
	Media     bool   // video note or video: never a report
	ReplyTo   int64  // message answered; in a forum topic the topic's first message is not a reply
}

// IsReply: the person answers someone in the chat (not just writes into the topic).
func (m *GroupMessage) IsReply() bool {
	return m.ReplyTo != 0 && strconv.FormatInt(m.ReplyTo, 10) != m.Thread
}

// ReadGroupMessage returns the group message of an update, if it is one.
// Only new messages count: an edited report is not re-read, as in the script.
func ReadGroupMessage(body []byte) (*GroupMessage, bool) {
	var u struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			MessageID int64 `json:"message_id"`
			Date      int64 `json:"date"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Thread *int64 `json:"message_thread_id"`
			From   *struct {
				ID        int64  `json:"id"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Username  string `json:"username"`
			} `json:"from"`
			Text  string `json:"text"`
			Reply *struct {
				MessageID int64 `json:"message_id"`
			} `json:"reply_to_message"`
			VideoNote json.RawMessage `json:"video_note"`
			Video     json.RawMessage `json:"video"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil || u.Message == nil || u.Message.From == nil {
		return nil, false
	}
	m := u.Message
	if m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		return nil, false
	}
	g := &GroupMessage{
		UpdateID: u.UpdateID, MessageID: m.MessageID, ChatID: m.Chat.ID,
		Sent:   time.Unix(m.Date, 0).In(club.Almaty),
		FromID: m.From.ID, FirstName: m.From.FirstName, LastName: m.From.LastName, Username: m.From.Username,
		Text:  strings.Trim(m.Text, " \t\n\r\v\f \ufeff  　"),
		Media: len(m.VideoNote) > 0 || len(m.Video) > 0,
	}
	if m.Thread != nil {
		g.Thread = strconv.FormatInt(*m.Thread, 10)
	}
	if m.Reply != nil {
		g.ReplyTo = m.Reply.MessageID
	}
	if m.Date == 0 {
		g.Sent = time.Now().In(club.Almaty)
	}
	return g, true
}

// JSLen is the length of s as JavaScript's String.length counts it.
func JSLen(s string) int { return len(utf16.Encode([]rune(s))) }

// ResidentLookup finds the resident behind a Telegram account.
type ResidentLookup func(tgID int64, fullName string) (name string, ok bool)

// Decision is what the server bot makes of a group message.
type Decision struct {
	Record   bool // worth keeping
	Verdict  string
	Resident string
	Day      time.Time // Almaty midnight of the day the report counts for
	Late     bool
	Len      int
}

func (m *GroupMessage) FullName() string {
	return strings.TrimSpace(m.FirstName + " " + m.LastName)
}

var (
	reportWordsRe = regexp.MustCompile(`(?i)(отч[её]т|сделал|сделано|выполнил|выполнено|не успел|не получилось|план на завтра|на завтра|завтра|итог[иа]? дня|за день|сегодня)`)
	reportListRe  = regexp.MustCompile(`(?m)^\s*(\d+[.)]|[-•*✅❌☑️✔️🔹▪️])\s*\S`)
)

// LooksLikeReport: the text reads like a daily report, not a chat message:
// at least two report words, or one report word and a list of items.
func LooksLikeReport(text string) bool {
	words := map[string]bool{}
	for _, w := range reportWordsRe.FindAllString(strings.ToLower(text), -1) {
		words[w] = true
	}
	items := len(reportListRe.FindAllString(text, -1))
	return len(words) >= 2 || (len(words) >= 1 && items >= 2)
}

// Decide applies the report rules to one group message.
func Decide(m *GroupMessage, topic string, lookup ResidentLookup) Decision {
	d := Decision{Len: JSLen(m.Text)}
	if m.Media || m.Text == "" || strings.HasPrefix(m.Text, "/") {
		return d
	}
	name, isRes := lookup(m.FromID, m.FullName())
	d.Resident = name
	if topic != "" && m.Thread != topic {
		// A resident whose report went to another topic is warned. Plain
		// conversation in «Общение» is long too, so only a text that reads like
		// a report (report words, a list of done/plan) counts.
		if d.Len >= MinReportLen && LooksLikeReport(m.Text) {
			d.Record, d.Verdict = true, VerdictWrongTopic
		}
		return d
	}
	if !isRes {
		if d.Len >= MinReportLen {
			d.Record, d.Verdict = true, VerdictNotResident
		}
		return d
	}
	d.Record = true
	if d.Len < MinReportLen {
		d.Verdict = VerdictShort
		return d
	}
	d.Verdict = VerdictReport
	s := m.Sent.In(club.Almaty)
	d.Day = time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, club.Almaty)
	d.Late = s.Hour() < LateBeforeHour
	return d
}

// MatchByName links a sender without a Chat ID in the sheet to a resident.
// Stricter than the script's _autoLinkChatId, which also matched on any
// common name prefix: here the full name or a first name of 3+ letters must
// be equal, and only one resident may match.
func MatchByName(sender string, candidates []string) (string, bool) {
	s := club.NormName(sender)
	if s == "" {
		return "", false
	}
	sFirst := strings.Split(s, " ")[0]
	found := ""
	for _, c := range candidates {
		r := club.NormName(c)
		if r == "" {
			continue
		}
		rFirst := strings.Split(r, " ")[0]
		if r == s || (len([]rune(rFirst)) > 2 && rFirst == sFirst) {
			if found != "" && found != c {
				return "", false // two residents fit: do not guess
			}
			found = c
		}
	}
	return found, found != ""
}

// HadMeeting ports the script's _hadMeeting: names are compared ignoring case,
// spaces and ё; one may be the start of the other, or first names may match.
func HadMeeting(resident string, met []string) bool {
	t := club.NormName(resident)
	if t == "" {
		return false
	}
	tFirst := strings.Split(t, " ")[0]
	for _, x := range met {
		m := club.NormName(x)
		if m == "" {
			continue
		}
		if m == t || strings.HasPrefix(m, t) || strings.HasPrefix(t, m) {
			return true
		}
		mFirst := strings.Split(m, " ")[0]
		if len([]rune(mFirst)) > 2 && mFirst == tFirst {
			return true
		}
	}
	return false
}

// DayReport is a report counted for the day being checked.
type DayReport struct {
	TgID int64
	Name string
}

// DayInput is everything the daily check of one day needs.
type DayInput struct {
	Day          time.Time
	Residents    []club.Resident
	Server       []DayReport // reports the server bot counted (not late)
	Sheet        []DayReport // reports in the sheet's «Лог отчётов» (not late)
	Met          []string    // residents with a meeting that day (log and schedule)
	SheetFined   []string    // «Не сдан отчёт» fines the sheet set for that day
	FullCoverage bool        // the server received updates all day long
	Silent       bool        // no update at all for more than 24 hours
}

// DayResult is the comparison the team receives.
type DayResult struct {
	Day            string   `json:"day"`
	Active         int      `json:"active"`
	ServerCount    int      `json:"serverReports"`
	SheetCount     int      `json:"sheetReports"`
	OnlyServer     []string `json:"reportOnlyServer"`
	OnlySheet      []string `json:"reportOnlySheet"`
	WouldFine      []string `json:"wouldFine"`
	SheetFined     []string `json:"sheetFined"`
	FineOnlyServer []string `json:"fineOnlyServer"`
	FineOnlySheet  []string `json:"fineOnlySheet"`
	NoChatID       []string `json:"noChatId"`
	Meeting        []string `json:"meeting"`
	StopCrane      bool     `json:"stopCrane"`
	Partial        bool     `json:"partial"`
	Match          bool     `json:"match"`
}

func submitted(r club.Resident, list []DayReport) bool {
	n := club.NormName(r.Name)
	for _, x := range list {
		if (r.TgID != 0 && x.TgID == r.TgID) || (n != "" && club.NormName(x.Name) == n) {
			return true
		}
	}
	return false
}

// CheckDay decides whom the server would fine for a day and compares that,
// report by report and fine by fine, with what the sheet did.
func CheckDay(in DayInput) DayResult {
	res := DayResult{Day: in.Day.Format("02.01.2006"), Partial: !in.FullCoverage}
	var active []club.Resident
	for _, r := range in.Residents {
		if r.Name == "" || r.Former || r.Exception || r.Admin {
			continue
		}
		active = append(active, r)
	}
	res.Active = len(active)
	for _, r := range active {
		s, t := submitted(r, in.Server), submitted(r, in.Sheet)
		if s {
			res.ServerCount++
		}
		if t {
			res.SheetCount++
		}
		if s && !t {
			res.OnlyServer = append(res.OnlyServer, r.Name)
		}
		if t && !s {
			res.OnlySheet = append(res.OnlySheet, r.Name)
		}
	}
	// Same stop rule as the script: zero reports from a live club is a
	// failure of the bot, not ten people skipping at once.
	res.StopCrane = (res.Active >= 3 && res.ServerCount == 0) || in.Silent
	if !res.StopCrane {
		for _, r := range active {
			if submitted(r, in.Server) {
				continue
			}
			if r.TgID == 0 {
				res.NoChatID = append(res.NoChatID, r.Name)
				continue
			}
			if HadMeeting(r.Name, in.Met) {
				res.Meeting = append(res.Meeting, r.Name)
				continue
			}
			res.WouldFine = append(res.WouldFine, r.Name)
		}
	}
	res.SheetFined = uniqSorted(in.SheetFined)
	sort.Strings(res.WouldFine)
	res.FineOnlyServer = minus(res.WouldFine, res.SheetFined)
	res.FineOnlySheet = minus(res.SheetFined, res.WouldFine)
	res.Match = len(res.OnlyServer) == 0 && len(res.OnlySheet) == 0 &&
		len(res.FineOnlyServer) == 0 && len(res.FineOnlySheet) == 0
	return res
}

func uniqSorted(v []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range v {
		k := club.NormName(s)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(s))
	}
	sort.Strings(out)
	return out
}

func minus(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[club.NormName(s)] = true
	}
	var out []string
	for _, s := range a {
		if !in[club.NormName(s)] {
			out = append(out, s)
		}
	}
	return out
}

// Message is the daily note to the team.
func (r DayResult) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧪 Сервер-бот за %s (штрафы не выставляет, только сверка)\n\n", r.Day)
	if r.Partial {
		b.WriteString("⚠️ Сервер получал сообщения не весь день, сверка неполная.\n\n")
	}
	fmt.Fprintf(&b, "Отчёты: сервер %d, таблица %d из %d\n", r.ServerCount, r.SheetCount, r.Active)
	if r.StopCrane {
		b.WriteString("Сервер остановил бы проверку: ни одного отчёта, похоже на сбой\n")
	} else {
		fmt.Fprintf(&b, "Штрафы: сервер %s, таблица %s\n", names(r.WouldFine), names(r.SheetFined))
	}
	if r.Match {
		b.WriteString("\n✅ Совпадает с таблицей")
	} else {
		b.WriteString("\n❌ Расхождения:")
		line := func(title string, v []string) {
			if len(v) > 0 {
				fmt.Fprintf(&b, "\n• %s: %s", title, strings.Join(v, ", "))
			}
		}
		line("отчёт засчитал только сервер", r.OnlyServer)
		line("отчёт засчитала только таблица", r.OnlySheet)
		line("штраф выставил бы только сервер", r.FineOnlyServer)
		line("штраф выставила только таблица", r.FineOnlySheet)
	}
	if len(r.Meeting) > 0 {
		fmt.Fprintf(&b, "\n\nБыла встреча, отчёт не нужен: %s", strings.Join(r.Meeting, ", "))
	}
	if len(r.NoChatID) > 0 {
		fmt.Fprintf(&b, "\n\nБез Chat ID (не проверялись): %s", strings.Join(r.NoChatID, ", "))
	}
	return b.String()
}

func names(v []string) string {
	if len(v) == 0 {
		return "никого"
	}
	return strings.Join(v, ", ")
}
