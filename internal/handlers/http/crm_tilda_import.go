package http

import (
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// R47: импорт экспорта заявок Tilda (CSV).
//
// История заявок сайта через API Tilda не отдаётся (API Tilda: проекты и
// страницы); заявки лежат в разделе сайта «Заявки» (Экспорт CSV) или в Tilda
// CRM (Экспорт / Расширенный экспорт). Владелец скачивает CSV и загружает его
// в «Импорт базы»: платформа узнаёт формат Tilda по колонкам (formname,
// tranid, sent, referer, utm_…) и отдаёт файл сюда.
//
// R50: настоящий экспорт bxclub.kz (Email;Name;Phone;Date;Input;Hidden;
// Договор_офферты;tranid;formid;…;Ваш_бизнес;Status ID;Stage):
//   - формы приходят только номером (formid): у номера есть человеческое
//     имя (tildaFormDefaults, команда правит его в просмотре, имена
//     запоминаются в bs_crm.tildaForms);
//   - телефон бывает в Phone, бывает в Input (старые формы); Input без
//     телефона это «Цель»; вопросы квиза («Что…», «Какая…») уходят в заметку;
//     «Ваш бизнес» в нишу;
//   - Hidden «1-ый тариф» и Договор_офферты=yes: человек хотел купить тариф,
//     сегмент «Хотели купить тариф (сайт)», горячий, приоритет высокий;
//   - телефоны Казахстана приводятся к +7XXXXXXXXXX (8…, 7…, +7, пробелы,
//     скобки); иностранные и битые номера остаются, но с пометкой;
//   - тестовые заявки (номера из одних семёрок, …000000, имена test/тест,
//     ааа, бизнес «тест») не становятся лидами: просмотр показывает их число;
//   - один человек (телефон, почта, @ник) = одна карточка: берётся последняя
//     заявка, ответы и даты всех заявок уходят в историю карточки.
//
// Уже живущие в CRM карточки не перезаписываются: им дописывается форма,
// отметка «заявка с сайта» и намерение купить тариф. dry: только просмотр.

type tildaCol struct {
	H string `json:"h"`
	F string `json:"f"`
}

// TildaForm: a Tilda form of the file and its human name.
type TildaForm struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Rows  int    `json:"rows"`
	Tests int    `json:"tests"`
	Leads int    `json:"leads"` // people whose latest request came from it
	Def   bool   `json:"def"`   // the name is the default one, not the team's
}

// TildaImportReport: what the file holds and what the import did.
type TildaImportReport struct {
	File     string              `json:"file"`
	Rows     int                 `json:"rows"`
	Fresh    int                 `json:"fresh"`
	Dup      int                 `json:"dup"`     // people already in the CRM
	DupFile  int                 `json:"dupFile"` // requests merged into another one of the same person
	Deleted  int                 `json:"deleted"` // people the team deleted before
	Skip     int                 `json:"skip"`    // no phone, Telegram or e-mail
	Tests    int                 `json:"tests"`   // test requests, not imported
	Flagged  int                 `json:"flagged"` // people with a foreign or broken phone
	People   int                 `json:"people"`  // distinct people (new + in CRM + deleted)
	Updated  int                 `json:"updated"` // CRM cards marked as site leads
	Forms    map[string]int      `json:"forms"`   // form name → requests (tests excluded)
	FormList []TildaForm         `json:"formList"`
	Segs     map[string]int      `json:"segs"` // segment → people (tests: requests)
	From     string              `json:"from,omitempty"`
	To       string              `json:"to,omitempty"`
	Columns  []tildaCol          `json:"columns"`
	Sample   []map[string]string `json:"sample"`
	TestList []map[string]string `json:"testList"`
	FlagList []map[string]string `json:"flagList"`
	Tilda    bool                `json:"tilda"` // the columns look like Tilda's export
	Done     bool                `json:"done"`
	At       string              `json:"at,omitempty"`
}

// The segments the preview counts.
const (
	TildaSegTariff = "Хотели купить тариф (сайт)"
	TildaSegSite   = "Заявки с сайта"
	TildaSegTests  = "Тестовые"
)

// tildaFormDefaults: the forms of bxclub.kz by their Tilda id (the export
// has no form names). The team renames them in the preview.
var tildaFormDefaults = map[string]string{
	"form848255043":  "Заявка январь 2025 (лид-магнит?)",
	"form1447358181": "Заявка на разбор (бизнес и цель)",
	"form1447374631": "Квиз: стадия и боли",
	"form846585592":  "Покупка тарифа",
	"form1234981126": "Заявка с сайта",
	"form1234985931": "Заявка с сайта",
	"form1269547391": "Заявка с сайта",
	"form1768964711": "Заявка с сайта",
}

var tildaQuizRe = regexp.MustCompile(`^(что|какая|какой|какое|какие|как|сколько|где|почему|зачем|когда|кто|чем|есть ли|хотите ли)\s`)

// tildaHead: a column of the export → a field.
func tildaHead(h string) string {
	k := strings.ToLower(strings.TrimSpace(strings.Trim(h, "\ufeff\"")))
	k = strings.NewReplacer("ё", "е", "_", " ", "-", " ").Replace(k)
	k = strings.Join(strings.Fields(k), " ")
	switch {
	case k == "":
		return ""
	case strings.HasPrefix(k, "utm "):
		return "utm_" + strings.TrimPrefix(k, "utm ")
	case k == "formname" || k == "form name" || k == "название формы" || k == "форма" || k == "имя формы":
		return "form"
	case k == "formid" || k == "form id" || k == "id формы":
		return "formid"
	case k == "tranid" || k == "requestid" || k == "request id" || k == "id заявки" || k == "номер заявки" || k == "id лида" || k == "lead id":
		return "tranid"
	case k == "sent" || k == "date" || k == "дата" || k == "created" || k == "дата создания" || k == "дата заявки" || k == "время" || k == "дата и время" || k == "date created":
		return "date"
	case k == "referer" || k == "referrer" || k == "page" || k == "url" || k == "страница" || k == "адрес страницы" || k == "источник страницы":
		return "page"
	case k == "cookies" || k == "tildautm" || k == "ip" || k == "user agent" || k == "useragent" || k == "ma name" || k == "ma email" || k == "stage" || k == "этап" || k == "ответственный" ||
		k == "status id" || k == "status" || k == "статус" || k == "stage id":
		return "skip"
	case k == "input":
		return "input" // a phone in the old forms, the goal in the new ones
	case k == "hidden" || strings.Contains(k, "тариф") || k == "tariff" || k == "plan":
		return "hidden"
	case strings.Contains(k, "оферт") || strings.Contains(k, "offer") || strings.Contains(k, "договор"):
		return "offer"
	case strings.HasSuffix(k, "?") || tildaQuizRe.MatchString(k+" ") && len(strings.Fields(k)) >= 3:
		return "quiz"
	case strings.Contains(k, "telegram") || strings.Contains(k, "телеграм") || k == "tg" || k == "тг":
		return "tg"
	case strings.Contains(k, "phone") || strings.Contains(k, "телефон") || k == "tel" || strings.Contains(k, "whatsapp") || strings.Contains(k, "номер"):
		return "phone"
	case strings.Contains(k, "mail") || strings.Contains(k, "почта"):
		return "email"
	case k == "name" || k == "имя" || k == "fio" || k == "фио" || strings.Contains(k, "your name") || k == "имя и фамилия" || k == "контакт" || k == "клиент" || k == "full name":
		return "name"
	case strings.Contains(k, "цель") || k == "goal":
		return "goal"
	case strings.Contains(k, "ниша") || strings.Contains(k, "niche") || strings.Contains(k, "сфера") || strings.Contains(k, "бизнес") || strings.Contains(k, "business"):
		return "niche"
	case strings.Contains(k, "оборот") || strings.Contains(k, "выручк") || strings.Contains(k, "revenue"):
		return "revenue"
	case strings.Contains(k, "comment") || strings.Contains(k, "коммент") || strings.Contains(k, "message") || strings.Contains(k, "сообщени") || strings.Contains(k, "вопрос") || strings.Contains(k, "запрос") || k == "textarea":
		return "comment"
	case k == "source" || k == "источник":
		return "source"
	}
	return "extra"
}

// ParseTildaCSV reads the export (comma, semicolon or tab; UTF-8 or Windows-1251 decoded by the platform).
func ParseTildaCSV(text string) ([][]string, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	r := csv.NewReader(strings.NewReader(text))
	r.LazyQuotes, r.FieldsPerRecord = true, -1
	switch {
	case strings.Count(first, "\t") > 0:
		r.Comma = '\t'
	case strings.Count(first, ";") > strings.Count(first, ","):
		r.Comma = ';'
	}
	rows, err := r.ReadAll()
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	var out [][]string
	for _, row := range rows {
		keep := false
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
			if row[i] != "" {
				keep = true
			}
		}
		if keep {
			out = append(out, row)
		}
	}
	return out, nil
}

// LooksTilda: the header has Tilda's own columns.
func LooksTilda(head []string) bool {
	n := 0
	for _, h := range head {
		switch tildaHead(h) {
		case "form", "formid", "tranid", "page":
			n++
		}
		if strings.HasPrefix(strings.ToLower(h), "utm_") {
			n++
		}
	}
	return n >= 2
}

type tildaRow struct {
	f     map[string]string
	extra []string
	quiz  []string
	at    time.Time
	n     int // the row's place in the file

	formID, form      string
	phone, key, flag  string // normalized phone, dedupe key, a flag for a foreign or broken one
	raw, phone2, goal string
	test              string // why it is a test request
	tariff            bool
}

func tildaRows(rows [][]string) ([]tildaRow, []tildaCol) {
	if len(rows) == 0 {
		return nil, nil
	}
	head := rows[0]
	cols := make([]tildaCol, len(head))
	for i, h := range head {
		cols[i] = tildaCol{H: h, F: tildaHead(h)}
	}
	var out []tildaRow
	for n, r := range rows[1:] {
		t := tildaRow{f: map[string]string{}, n: n}
		for i, v := range r {
			if i >= len(cols) || v == "" {
				continue
			}
			switch f := cols[i].F; f {
			case "", "skip":
			case "extra":
				if len(t.extra) < 6 && utf8.RuneCountInString(v) <= 200 {
					t.extra = append(t.extra, cols[i].H+": "+v)
				}
			case "quiz":
				q := strings.TrimSpace(strings.NewReplacer("_", " ").Replace(strings.Trim(cols[i].H, "\ufeff\"")))
				if !strings.HasSuffix(q, "?") {
					q += "?"
				}
				t.quiz = append(t.quiz, q+" "+v)
			case "comment":
				if t.f[f] != "" {
					t.f[f] += "\n" + v
				} else {
					t.f[f] = v
				}
			default:
				if t.f[f] == "" {
					t.f[f] = v
				}
			}
		}
		if d, ok := club.Date(t.f["date"]); ok {
			t.at = d
		}
		out = append(out, t)
	}
	return out, cols
}

var tildaPhoneLikeRe = regexp.MustCompile(`^\+?[\d\s()\-.]+$`)

// tildaPhoneLike: the cell is a phone number, not words.
func tildaPhoneLike(s string) bool {
	s = strings.TrimSpace(s)
	return tildaPhoneLikeRe.MatchString(s) && len(digitsRe.ReplaceAllString(s, "")) >= 2 && !(len(digitsRe.ReplaceAllString(s, "")) < 5 && !strings.HasPrefix(s, "0"))
}

// TildaPhoneNorm: the phone of a request. Kazakhstan → +7XXXXXXXXXX (key: its
// 10 digits); a foreign or broken number stays (key: its digits) with a flag.
func TildaPhoneNorm(raw string) (phone, key, flag string) {
	raw = strings.TrimSpace(raw)
	d := digitsRe.ReplaceAllString(raw, "")
	if d == "" {
		return "", "", ""
	}
	plus := strings.HasPrefix(raw, "+")
	nat := ""
	switch {
	case len(d) == 11 && (d[0] == '7' || (d[0] == '8' && !plus)):
		nat = d[1:]
	case len(d) == 10 && d[0] == '7' && !plus:
		nat = d
	}
	if nat != "" {
		if nat[0] == '7' || nat[0] == '6' {
			return "+7" + nat, nat, ""
		}
		return "+7" + nat, nat, "иностранный номер: Россия (+7 " + nat[:3] + ")"
	}
	foreign := map[string]string{"996": "Кыргызстан", "998": "Узбекистан", "992": "Таджикистан", "993": "Туркменистан", "994": "Азербайджан", "995": "Грузия", "374": "Армения", "375": "Беларусь", "380": "Украина", "90": "Турция", "86": "Китай", "971": "ОАЭ", "1": "США/Канада", "44": "Великобритания", "49": "Германия", "82": "Корея"}
	switch {
	case len(d) == 10 && d[0] == '0' && (d[1] == '5' || d[1] == '7' || d[1] == '2' || d[1] == '3' || d[1] == '9'):
		// Kyrgyzstan's local format: 0 5XX XXX XXX
		return "+996" + d[1:], "996" + d[1:], "иностранный номер: похоже, Кыргызстан (+996), в заявке без кода страны"
	case strings.HasPrefix(d, "00") && len(d) >= 11:
		d = d[2:]
		plus = true
	}
	if plus && len(d) >= 10 && len(d) <= 13 {
		for _, n := range []int{3, 2, 1} {
			if c, ok := foreign[d[:n]]; ok {
				return "+" + d, d, "иностранный номер: " + c
			}
		}
		return "+" + d, d, "иностранный номер"
	}
	return "", "x" + d, "некорректный номер"
}

var (
	tildaTestNameRe = regexp.MustCompile(`^(test|testing|tест|тест|тестовый|тестовая|проверка|qwerty|asdf|йцукен)\s*\d*$`)
	// known test accounts of the team, hashed (sha1, lower case, no spaces)
	tildaTestNameHash = map[string]bool{"430d931437508450": true}
	tildaTestWordRe   = regexp.MustCompile(`(?i)(^|[^\pL])(test|тест)([^\pL]|$)`)
)

// tildaTest: why a request is a test one ("" if it is a real one).
func tildaTest(name, digits, niche string) string {
	n := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if digits != "" {
		same := len(digits) >= 5
		for i := 1; i < len(digits) && same; i++ {
			same = digits[i] == digits[0]
		}
		sub := digits
		if len(sub) > 7 {
			sub = sub[len(sub)-7:]
		}
		subSame := len(sub) == 7
		for i := 1; i < len(sub) && subSame; i++ {
			subSame = sub[i] == sub[0]
		}
		if same || subSame || (len(digits) >= 10 && strings.HasSuffix(digits, "000000")) {
			return "тестовый номер"
		}
	}
	if n != "" {
		h := sha1.Sum([]byte(strings.ReplaceAll(n, " ", "")))
		if tildaTestNameHash[hex.EncodeToString(h[:8])] || tildaTestNameRe.MatchString(n) {
			return "тестовое имя"
		}
		rs := []rune(strings.ReplaceAll(n, " ", ""))
		same := len(rs) >= 2 && len(rs) <= 6 && unicode.IsLetter(rs[0])
		for i := 1; i < len(rs) && same; i++ {
			same = rs[i] == rs[0]
		}
		if same {
			return "тестовое имя"
		}
	}
	if tildaTestWordRe.MatchString(strings.TrimSpace(niche)) && utf8.RuneCountInString(strings.TrimSpace(niche)) <= 30 {
		return "тест в поле «бизнес»"
	}
	return ""
}

func tildaKeys(phone, tg, email, tran string) []string {
	var k []string
	if d := phoneDigits(phone); len(d) >= 10 {
		k = append(k, "ph:"+d[len(d)-10:])
	}
	if t := strings.ToLower(strings.TrimPrefix(tildaTg(tg), "@")); t != "" {
		k = append(k, "@"+t)
	}
	if e := strings.ToLower(strings.TrimSpace(email)); strings.Contains(e, "@") {
		k = append(k, "em:"+e)
	}
	if tran != "" {
		k = append(k, "tr:"+tran)
	}
	return k
}

// tildaRowKeys: the contact keys of a request (the normalized phone first).
func tildaRowKeys(r *tildaRow) []string {
	var k []string
	if r.key != "" {
		if len(r.key) >= 10 && r.key[0] != 'x' {
			k = append(k, "ph:"+r.key[len(r.key)-10:])
		} else {
			k = append(k, "ph:"+r.key)
		}
	}
	for _, x := range tildaKeys("", r.f["tg"], r.f["email"], "") {
		k = append(k, x)
	}
	return k
}

// tildaFormNames: the team's names (the preview's edits, then bs_crm.tildaForms).
func tildaFormNames(crm map[string]any, edit map[string]string) map[string]string {
	out := map[string]string{}
	if m, ok := crm["tildaForms"].(map[string]any); ok {
		for k, v := range m {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && v != nil {
				out[k] = s
			}
		}
	}
	for k, v := range edit {
		if s := strings.TrimSpace(v); s != "" {
			out[strings.TrimSpace(k)] = s
		}
	}
	return out
}

// ImportTilda merges the export into the CRM document (dry: counts only).
func ImportTilda(crm map[string]any, rows [][]string, file string, now time.Time, dry bool) TildaImportReport {
	return ImportTildaWith(crm, rows, file, now, dry, nil)
}

type tildaGroup struct {
	rows []*tildaRow
	keys map[string]bool
}

// ImportTildaWith: forms holds the team's names of the forms (formid → name).
func ImportTildaWith(crm map[string]any, rows [][]string, file string, now time.Time, dry bool, forms map[string]string) TildaImportReport {
	rep := TildaImportReport{File: file, Forms: map[string]int{}, Segs: map[string]int{}, Sample: []map[string]string{},
		TestList: []map[string]string{}, FlagList: []map[string]string{}, FormList: []TildaForm{}}
	if len(rows) > 0 {
		rep.Tilda = LooksTilda(rows[0])
	}
	list, cols := tildaRows(rows)
	rep.Columns = cols
	rep.Rows = len(list)
	names := tildaFormNames(crm, forms)

	// forms: the team's name, Tilda's own name, the default, the id
	formAt := map[string]int{}
	formOf := func(r *tildaRow) {
		id, own := strings.TrimSpace(r.f["formid"]), strings.TrimSpace(r.f["form"])
		key := firstNonEmptyS(id, own, "-")
		name, def := names[key], false
		if name == "" {
			if own != "" {
				name = own
			} else if d := tildaFormDefaults[id]; d != "" {
				name, def = d, true
			} else if id != "" {
				name, def = "Форма "+strings.TrimPrefix(id, "form"), true
			}
		}
		r.formID, r.form = id, name
		if _, ok := formAt[key]; !ok {
			formAt[key] = len(rep.FormList)
			rep.FormList = append(rep.FormList, TildaForm{ID: key, Name: name, Def: def})
		}
	}

	// each request: form, phone (Phone or Input), goal, tariff, test
	for i := range list {
		r := &list[i]
		f := r.f
		formOf(r)
		in := strings.TrimSpace(f["input"])
		src := f["phone"]
		if in != "" && tildaPhoneLike(in) {
			if src == "" {
				src = in
			} else if digitsRe.ReplaceAllString(in, "") != digitsRe.ReplaceAllString(src, "") {
				r.phone2 = in
			}
		} else if in != "" && strings.IndexFunc(in, unicode.IsLetter) >= 0 {
			r.goal = in
		}
		if g := strings.TrimSpace(f["goal"]); g != "" && strings.IndexFunc(g, unicode.IsLetter) >= 0 {
			r.goal = strings.TrimSpace(strings.Join([]string{r.goal, g}, " · "))
			r.goal = strings.Trim(r.goal, " ·")
		}
		r.raw = strings.TrimSpace(src)
		r.phone, r.key, r.flag = TildaPhoneNorm(src)
		hid := strings.ToLower(f["hidden"])
		offer := strings.ToLower(strings.TrimSpace(f["offer"]))
		r.tariff = strings.Contains(hid, "тариф") || strings.Contains(hid, "tariff") || offer == "yes" || offer == "да" || offer == "on" ||
			strings.Contains(strings.ToLower(r.form), "тариф")
		r.test = tildaTest(f["name"], digitsRe.ReplaceAllString(src, ""), f["niche"])
		fi := &rep.FormList[formAt[firstNonEmptyS(r.formID, strings.TrimSpace(f["form"]), "-")]]
		fi.Rows++
		if r.test != "" {
			fi.Tests++
		}
	}

	// the CRM's people
	leads := asList(crm["leads"])
	byKey := map[string]map[string]any{}
	for _, x := range leads {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		tran := ""
		if id := pStr(m, "id"); strings.HasPrefix(id, "site") && len(id) > 4 {
			tran = id[4:]
		}
		for _, k := range tildaKeys(pStr(m, "phone"), pStr(m, "tg"), pStr(m, "email"), tran) {
			byKey[k] = m
		}
		if pr := pStr(m, "phoneRaw"); pr != "" {
			if _, k, _ := TildaPhoneNorm(pr); k != "" {
				byKey["ph:"+k] = m
			}
		}
	}
	deleted := map[string]bool{}
	for _, x := range asList(crm["deleted"]) {
		s := strings.ToLower(fmt.Sprint(x))
		deleted[s] = true
		if strings.HasPrefix(s, "ph:") {
			if d := phoneDigits(s[3:]); len(d) >= 10 {
				deleted["ph:"+d[len(d)-10:]] = true
			}
		}
	}

	// people: requests with a shared phone, e-mail or @nick
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].at, list[j].at
		if a.IsZero() || b.IsZero() {
			return !a.IsZero() && b.IsZero()
		}
		return a.Before(b)
	})
	var groups []*tildaGroup
	gOf := map[string]*tildaGroup{}
	for i := range list {
		r := &list[i]
		if r.test != "" {
			rep.Tests++
			if len(rep.TestList) < 60 {
				rep.TestList = append(rep.TestList, map[string]string{"name": r.f["name"], "phone": r.raw, "form": r.form, "date": tildaFmt(r.at), "why": r.test})
			}
			continue
		}
		rep.Forms[firstNonEmptyS(r.form, "без названия")]++
		keys := tildaRowKeys(r)
		if len(keys) == 0 {
			rep.Skip++
			continue
		}
		if !r.at.IsZero() {
			d := r.at.In(almaty).Format("02.01.2006")
			if rep.From == "" || r.at.Before(tildaDay(rep.From)) {
				rep.From = d
			}
			if rep.To == "" || r.at.After(tildaDay(rep.To).Add(24*time.Hour-time.Second)) {
				rep.To = d
			}
		}
		var g *tildaGroup
		for _, k := range keys {
			if gOf[k] != nil {
				g = gOf[k]
				break
			}
		}
		if g == nil {
			g = &tildaGroup{keys: map[string]bool{}}
			groups = append(groups, g)
		}
		g.rows = append(g.rows, r)
		for _, k := range keys {
			g.keys[k] = true
			gOf[k] = g
		}
	}
	// the newest people first in the sample
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].rows[len(groups[i].rows)-1].at.After(groups[j].rows[len(groups[j].rows)-1].at)
	})

	var fresh []any
	for _, g := range groups {
		last := g.rows[len(g.rows)-1]
		first := g.rows[0]
		rep.DupFile += len(g.rows) - 1
		rep.People++
		tariff, flag := false, ""
		var tariffRow *tildaRow
		for _, r := range g.rows {
			if r.tariff {
				tariff, tariffRow = true, r
			}
		}
		pick := func(f string) string {
			for i := len(g.rows) - 1; i >= 0; i-- {
				if v := strings.TrimSpace(g.rows[i].f[f]); v != "" {
					return v
				}
			}
			return ""
		}
		// the phone: the latest valid one, else the latest one at all
		var phoneRow *tildaRow
		for i := len(g.rows) - 1; i >= 0; i-- {
			r := g.rows[i]
			if r.key != "" && (phoneRow == nil || (phoneRow.flag != "" && r.flag == "")) {
				phoneRow = r
			}
		}
		if phoneRow == nil {
			phoneRow = last
		}
		flag = phoneRow.flag
		seg := TildaSegSite
		if tariff {
			seg = TildaSegTariff
		}
		rep.Segs[seg]++
		rep.FormList[formAt[firstNonEmptyS(last.formID, strings.TrimSpace(last.f["form"]), "-")]].Leads++

		// the CRM card of this person, or the team deleted them
		var hit map[string]any
		st := "new"
		keys := []string{}
		for k := range g.keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, r := range g.rows {
			if t := strings.TrimSpace(r.f["tranid"]); t != "" {
				keys = append(keys, "tr:"+t)
			}
		}
		for _, k := range keys {
			if m := byKey[k]; m != nil {
				hit, st = m, "dup"
				break
			}
		}
		if hit == nil {
			for _, k := range keys {
				if deleted[k] {
					st = "deleted"
				}
			}
		}
		name := pick("name")
		if flag != "" {
			rep.Flagged++
			if len(rep.FlagList) < 40 {
				rep.FlagList = append(rep.FlagList, map[string]string{"name": name, "phone": phoneRow.raw, "flag": flag, "form": last.form, "date": tildaFmt(last.at)})
			}
		}
		if len(rep.Sample) < 15 {
			rep.Sample = append(rep.Sample, map[string]string{"name": name, "phone": firstNonEmptyS(phoneRow.phone, phoneRow.raw), "email": pick("email"), "tg": pick("tg"),
				"form": last.form, "date": tildaFmt(last.at), "st": st, "seg": seg, "n": fmt.Sprint(len(g.rows)), "flag": flag})
		}
		at := now
		if !last.at.IsZero() {
			at = last.at
		}
		// the history: every request with its date, form and answers
		history := func(card map[string]any) {
			for _, r := range g.rows {
				t := "Заявка с сайта (Tilda)" + prefixed(" «", r.form, "»")
				var add []string
				if r.tariff {
					add = append(add, "хотел купить тариф"+prefixed(" «", strings.TrimSpace(r.f["hidden"]), "»"))
				}
				if r.goal != "" {
					add = append(add, "цель: "+r.goal)
				}
				if v := strings.TrimSpace(r.f["niche"]); v != "" {
					add = append(add, "бизнес: "+v)
				}
				add = append(add, r.quiz...)
				if len(add) > 0 {
					t += ": " + strings.Join(add, "; ")
				}
				when := r.at
				if when.IsZero() {
					when = now
				}
				addLog(card, when, t)
			}
		}
		switch st {
		case "deleted":
			rep.Deleted++
			rep.People--
			rep.Segs[seg]--
		case "dup":
			rep.Dup++
			mark := hit != nil && pStr(hit, "tildaAt") == ""
			want := tariff && hit != nil && pStr(hit, "intent") == ""
			if (mark || want) && !dry {
				if last.form != "" && pStr(hit, "form") == "" {
					hit["form"] = last.form
				}
				if pStr(hit, "niche") == "" && pick("niche") != "" {
					hit["niche"] = pick("niche")
				}
				if mark {
					hit["tildaAt"] = at.UTC().Format(time.RFC3339)
					history(hit)
				}
				if want {
					hit["intent"], hit["hot"], hit["prio"] = "tariff", true, "high"
					hit["tariff"] = firstNonEmptyS(strings.TrimSpace(tariffRow.f["hidden"]), "тариф")
				}
				addLog(hit, now, "Найден в экспорте заявок Tilda ("+file+"): заявка "+at.In(almaty).Format("02.01.2006")+prefixed(" · форма «", last.form, "»")+
					map[bool]string{true: " · хотел купить тариф", false: ""}[want])
			}
			if mark || want {
				rep.Updated++
			}
		case "new":
			rep.Fresh++
			if dry {
				continue
			}
			tran := strings.Map(func(c rune) rune {
				if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					return c
				}
				return -1
			}, last.f["tranid"])
			id := "site" + tran
			if tran == "" {
				h := sha1.Sum([]byte(strings.Join(keys, "|") + "|" + last.f["date"]))
				id = "tl" + hex.EncodeToString(h[:5])
			}
			if name == "" {
				name = firstNonEmptyS(phoneRow.phone, pick("email"), tildaTg(pick("tg")))
			}
			var note, goals, quiz, formsSeen []string
			seen := map[string]bool{}
			uniq := func(dst *[]string, v string) {
				if v = strings.TrimSpace(v); v != "" && !seen[v] {
					seen[v] = true
					*dst = append(*dst, v)
				}
			}
			for i := len(g.rows) - 1; i >= 0; i-- {
				r := g.rows[i]
				uniq(&goals, r.goal)
				for _, q := range r.quiz {
					uniq(&quiz, q)
				}
				uniq(&formsSeen, r.form)
			}
			if tariff {
				note = append(note, "Хотел купить тариф на сайте"+prefixed(": ", strings.TrimSpace(tariffRow.f["hidden"]), "")+
					map[bool]string{true: ", договор оферты принят", false: ""}[strings.TrimSpace(tariffRow.f["offer"]) != ""]+" ("+tildaFmt(tariffRow.at)+")")
			}
			if flag != "" {
				note = append(note, "⚠ Телефон: "+flag+prefixed(" (в заявке: ", phoneRow.raw, ")"))
			}
			if len(quiz) > 0 {
				note = append(note, "Квиз:\n"+strings.Join(quiz, "\n"))
			}
			for i := len(g.rows) - 1; i >= 0; i-- {
				r := g.rows[i]
				if c := strings.TrimSpace(r.f["comment"]); c != "" {
					uniq(&note, c)
				}
				if r.phone2 != "" {
					uniq(&note, "второй телефон: "+r.phone2)
				}
			}
			if e := pick("email"); e != "" {
				note = append(note, "email: "+e)
			}
			if pg := pick("page"); pg != "" {
				note = append(note, "страница: "+pg)
			}
			for _, x := range last.extra {
				uniq(&note, x)
			}
			if len(g.rows) > 1 {
				var w []string
				for _, r := range g.rows {
					w = append(w, tildaFmt(r.at)+prefixed(" «", r.form, "»"))
				}
				note = append(note, fmt.Sprintf("Заявок с сайта: %d (%s)", len(g.rows), strings.Join(w, "; ")))
			}
			src := "Сайт (Tilda)" + prefixed(": ", last.form, "")
			lead := map[string]any{"id": id, "col": "new", "name": name, "phone": phoneRow.phone, "tg": tildaTg(pick("tg")), "source": src,
				"niche": pick("niche"), "note": strings.Join(note, "\n"), "sum": "", "date": at.In(almaty).Format("02.01.2006"),
				"created": at.UTC().Format(time.RFC3339), "tildaAt": at.UTC().Format(time.RFC3339), "funnel": "site", "imp": "tilda", "impFile": file}
			if last.form != "" {
				lead["form"] = last.form
			}
			if len(formsSeen) > 1 {
				lead["forms"] = formsSeen
			}
			if len(goals) > 0 {
				lead["goal"] = strings.Join(goals, " · ")
			}
			if len(g.rows) > 1 {
				lead["tildaN"] = len(g.rows)
				if !first.at.IsZero() {
					lead["firstAt"] = first.at.UTC().Format(time.RFC3339)
				}
			}
			if flag != "" {
				lead["phoneFlag"], lead["phoneRaw"] = flag, phoneRow.raw
			}
			if tariff {
				lead["intent"], lead["hot"], lead["prio"] = "tariff", true, "high"
				lead["tariff"] = firstNonEmptyS(strings.TrimSpace(tariffRow.f["hidden"]), "тариф")
			}
			if e := pick("email"); e != "" {
				lead["email"] = e
			}
			if v := pick("revenue"); v != "" {
				lead["revenue"] = v
			}
			u := map[string]any{}
			for _, r := range g.rows { // the latest request's utm wins
				for k, v := range r.f {
					if strings.HasPrefix(k, "utm_") && v != "" {
						u[k] = v
					}
				}
			}
			if len(u) > 0 {
				lead["utm"] = u
			}
			history(lead)
			addLog(lead, now, "Загружен из экспорта заявок Tilda ("+file+"): последняя заявка "+at.In(almaty).Format("02.01.2006 15:04")+prefixed(", форма «", last.form, "»")+
				map[bool]string{true: fmt.Sprintf(", всего заявок %d", len(g.rows)), false: ""}[len(g.rows) > 1])
			for _, k := range keys {
				byKey[k] = lead
			}
			fresh = append(fresh, lead)
		}
	}
	if rep.Tests > 0 {
		rep.Segs[TildaSegTests] = rep.Tests
	}
	for k, v := range rep.Segs {
		if v <= 0 {
			delete(rep.Segs, k)
		}
	}
	if !dry && len(fresh) > 0 {
		crm["leads"] = append(leads, fresh...)
	}
	if !dry {
		// the team's names of the forms stay for the next export
		if len(forms) > 0 {
			m, _ := crm["tildaForms"].(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			for k, v := range forms {
				if k = strings.TrimSpace(k); k != "" && strings.TrimSpace(v) != "" {
					m[k] = strings.TrimSpace(v)
				}
			}
			crm["tildaForms"] = m
		}
		rep.Done = true
		rep.At = now.UTC().Format(time.RFC3339)
	}
	return rep
}

func tildaFmt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(almaty).Format("02.01.2006 15:04")
}

func prefixed(pre, s, post string) string {
	if s == "" {
		return ""
	}
	return pre + s + post
}

func tildaDay(d string) time.Time {
	t, _ := time.ParseInLocation("02.01.2006", d, almaty)
	return t
}

// TildaImport: POST /api/v1/platform/crm/tilda-import {csv | rows, file, dry, forms}.
func (p *Partners) TildaImport(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var in struct {
		CSV   string            `json:"csv"`
		Rows  [][]string        `json:"rows"`
		File  string            `json:"file"`
		Dry   bool              `json:"dry"`
		Forms map[string]string `json:"forms"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request", "message": "Файл не прочитан"})
		return
	}
	rows := in.Rows
	if len(rows) == 0 && in.CSV != "" {
		var err error
		if rows, err = ParseTildaCSV(in.CSV); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_csv", "message": "CSV не прочитан: " + err.Error()})
			return
		}
	}
	if len(rows) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "message": "В файле нет заявок"})
		return
	}
	for k, v := range in.Forms {
		if utf8.RuneCountInString(k) > 80 || utf8.RuneCountInString(v) > 120 || len(in.Forms) > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_forms", "message": "Слишком длинное имя формы"})
			return
		}
	}
	file := strings.TrimSpace(in.File)
	if file == "" {
		file = "Экспорт Tilda " + p.now().In(almaty).Format("02.01.2006")
	}
	rep, err := p.ImportTildaRows(c.Request.Context(), rows, file, in.Dry, in.Forms)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "store_failed", "message": "CRM не сохранилась, попробуйте ещё раз"})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// ImportTildaRows: the import over bs_crm; then the segments.
func (p *Partners) ImportTildaRows(ctx context.Context, rows [][]string, file string, dry bool, forms ...map[string]string) (TildaImportReport, error) {
	var names map[string]string
	if len(forms) > 0 {
		names = forms[0]
	}
	var rep TildaImportReport
	if dry {
		crm := map[string]any{}
		if d, err := p.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
			_ = json.Unmarshal([]byte(d.Value), &crm)
		}
		return ImportTildaWith(crm, rows, file, p.now(), true, names), nil
	}
	err := p.f().mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		rep = ImportTildaWith(crm, rows, file, p.now(), false, names)
		if rep.Fresh == 0 && rep.Updated == 0 {
			return len(names) > 0 // only the forms' names
		}
		hist := asList(crm["tildaImports"])
		hist = append(hist, map[string]any{"file": file, "at": rep.At, "rows": rep.Rows, "added": rep.Fresh, "dup": rep.Dup, "updated": rep.Updated,
			"tests": rep.Tests, "merged": rep.DupFile, "tariff": rep.Segs[TildaSegTariff], "from": rep.From, "to": rep.To})
		if len(hist) > 20 {
			hist = hist[len(hist)-20:]
		}
		crm["tildaImports"] = hist
		return true
	})
	if err != nil {
		return rep, err
	}
	if _, err := p.SegmentNow(ctx, false); err != nil {
		return rep, nil
	}
	return rep, nil
}
