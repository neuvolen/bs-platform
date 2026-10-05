package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// After the cutover (club.SheetMode off or mirror) the server is the only
// source of truth: nothing calls the Apps Script, nothing waits for it.
//
//   - SheetCutover: the one-time switch. While the sheet still fed the
//     server (master "sheet", an import in the last two days), the server
//     takes one final import from the script, puts its own newer writes back
//     on top, and becomes the master. Without that import within
//     cutoverWait it becomes the master anyway, with the data it has.
//   - ClubWrites.doLocal: a club write from the app or the platform is made
//     on the server only, with the messages the script used to send.
//   - AppGateway.ownCall: the app's other calls are answered by the server.

// errSheetOff: the script is not called after the cutover.
var errSheetOff = errors.New("таблица отключена: данные ведутся на платформе")

const (
	metaCutover   = "sheet_cutover" // "pending:<RFC3339>" | "done:<RFC3339>"
	cutoverWait   = 3 * time.Hour   // the script updates itself within the hour
	cutoverRecent = 48 * time.Hour
)

// CutoverRepo is what the cutover needs of the club data.
type CutoverRepo interface {
	Master(ctx context.Context) (string, error)
	SetMaster(ctx context.Context, m string) error
	LastImportAt(ctx context.Context) (*time.Time, error)
	SettleOpenWrites(ctx context.Context, why string) (int64, error)
	SyncReportLog(ctx context.Context, since time.Time) (int64, error)
}

// SheetCutover is the one-time move of the club off the sheet.
type SheetCutover struct {
	repo CutoverRepo
	meta MetaStore
	now  func() time.Time
	// OnDone runs once the server is the master (the platform's sections refresh).
	OnDone func()

	mu      sync.Mutex
	state   string
	stateAt time.Time
}

func NewSheetCutover(repo CutoverRepo, meta MetaStore) *SheetCutover {
	return &SheetCutover{repo: repo, meta: meta, now: time.Now}
}

func (s *SheetCutover) read(ctx context.Context) string {
	s.mu.Lock()
	if s.state != "" && s.now().Sub(s.stateAt) < 10*time.Second {
		v := s.state
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	v, err := s.meta.GetMeta(ctx, metaCutover)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	s.state, s.stateAt = v, s.now()
	s.mu.Unlock()
	return v
}

func (s *SheetCutover) write(ctx context.Context, v string) error {
	if err := s.meta.SetMeta(ctx, metaCutover, v); err != nil {
		return err
	}
	s.mu.Lock()
	s.state, s.stateAt = v, s.now()
	s.mu.Unlock()
	return nil
}

// Pending: the server waits for the sheet's final import (writes stay open
// so that import puts them back on top).
func (s *SheetCutover) Pending(ctx context.Context) bool {
	if s == nil || club.SheetLegacy() {
		return false
	}
	return strings.HasPrefix(s.read(ctx), "pending:")
}

// State for the status page: "", "pending" or "done", and since when.
func (s *SheetCutover) State(ctx context.Context) (string, string) {
	if s == nil {
		return "", ""
	}
	st, at, _ := strings.Cut(s.read(ctx), ":")
	return st, at
}

// Begin runs at start: nothing in legacy mode; otherwise the server becomes
// the master, after a final import when the sheet was still feeding it.
func (s *SheetCutover) Begin(ctx context.Context) error {
	if s == nil || club.SheetLegacy() {
		return nil
	}
	st := s.read(ctx)
	if strings.HasPrefix(st, "done:") || strings.HasPrefix(st, "pending:") {
		return nil
	}
	m, err := s.repo.Master(ctx)
	if err != nil {
		return err
	}
	if m == "server" {
		return s.write(ctx, "done:"+s.now().UTC().Format(time.RFC3339))
	}
	imp, err := s.repo.LastImportAt(ctx)
	if err != nil {
		return err
	}
	if imp == nil || s.now().Sub(*imp) > cutoverRecent {
		return s.finish(ctx, "no recent import")
	}
	log.Printf("sheet cutover: waiting for the sheet's final import (up to %s)", cutoverWait)
	return s.write(ctx, "pending:"+s.now().UTC().Format(time.RFC3339))
}

// ImportDone: an import was saved; the final one ends the waiting.
func (s *SheetCutover) ImportDone(ctx context.Context) {
	if !s.Pending(ctx) {
		return
	}
	if err := s.finish(ctx, "final import"); err != nil {
		log.Printf("sheet cutover: %v", err)
	}
}

// Check ends the waiting when it took too long.
func (s *SheetCutover) Check(ctx context.Context) {
	if !s.Pending(ctx) {
		return
	}
	since, err := time.Parse(time.RFC3339, strings.TrimPrefix(s.read(ctx), "pending:"))
	if err == nil && s.now().Sub(since) < cutoverWait {
		return
	}
	if err := s.finish(ctx, "no final import in time"); err != nil {
		log.Printf("sheet cutover: %v", err)
	}
}

func (s *SheetCutover) finish(ctx context.Context, why string) error {
	since := s.now().Add(-cutoverWait - time.Hour)
	if v := s.read(ctx); strings.HasPrefix(v, "pending:") {
		if t, err := time.Parse(time.RFC3339, strings.TrimPrefix(v, "pending:")); err == nil {
			since = t.Add(-time.Hour)
		}
	}
	if err := s.repo.SetMaster(ctx, "server"); err != nil {
		return err
	}
	n, err := s.repo.SettleOpenWrites(ctx, "на сервере (таблица отключена)")
	if err != nil {
		return err
	}
	// Reports the server counted while the sheet's copy came over.
	r, err := s.repo.SyncReportLog(ctx, since)
	if err != nil {
		log.Printf("sheet cutover: report log: %v", err)
	}
	if err := s.write(ctx, "done:"+s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	log.Printf("sheet cutover done (%s): the server keeps the club's data; %d writes settled, %d reports put back", why, n, r)
	if s.OnDone != nil {
		go s.OnDone()
	}
	return nil
}

// Loop checks the waiting every minute.
func (s *SheetCutover) Loop(ctx context.Context) {
	if s == nil || club.SheetLegacy() {
		return
	}
	if err := s.Begin(ctx); err != nil {
		log.Printf("sheet cutover: %v", err)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.Begin(ctx); err != nil { // a failed start is tried again
			log.Printf("sheet cutover: %v", err)
		}
		s.Check(ctx)
	}
}

// ── Club writes on the server only ──

// residentLoader gives the residents (names, Telegram ids, format).
type residentLoader interface {
	LoadResidents(ctx context.Context) ([]club.Resident, error)
}

// WriteNotify is how a club write reaches people (the bot).
type WriteNotify struct {
	Send  func(ctx context.Context, chatID int64, text string, kb map[string]any) error
	Topic func(ctx context.Context, chat, thread int64, text string) error
	// Admins get the team's notes (a diagnostic request).
	Admins []int64
	// Resident: R38c: a notice to a resident by the channel the resident chose
	// (WhatsApp or Telegram, bot.Service.SendResident); nil: Telegram only.
	Resident func(ctx context.Context, kind, key, name string, tg int64, text string, kb map[string]any) error
}

// teamOnly: the club's own data; the app's sections are anyone's (as the script).
func clubTeamOnly(action string) bool { return !pg.SectionWrite(action) }

func (w *ClubWrites) doLocal(ctx context.Context, source string, u *platformTgUser, action string, q url.Values, team bool) []byte {
	p := writeParams(q)
	if clubTeamOnly(action) && !team {
		return mustJSON(map[string]string{"error": "Это может только команда"})
	}
	if !pg.AppliesHere(action) {
		return mustJSON(map[string]string{"error": "Это действие теперь делается на платформе"})
	}
	if dup := w.duplicate(ctx, action, p); dup != nil {
		return dup
	}
	who := fullName(u)
	if n, ok := w.gw.Admins[u.ID]; ok && n != "" {
		who = n
	}
	if _, err := w.Local(ctx, source, u.ID, who, action, p); err != nil {
		return mustJSON(map[string]string{"error": err.Error()})
	}
	go w.notices(context.WithoutCancel(ctx), action, p)
	return w.answer(ctx, action, p)
}

// Local makes one club write on the server only: kept, applied, done (or,
// while the final import is awaited, left open so that import keeps it).
func (w *ClubWrites) Local(ctx context.Context, source string, tg int64, who, action string, p map[string]string) (*pg.ClubWrite, error) {
	rec, err := w.repo.NewWrite(ctx, pg.ClubWrite{Source: source, TgID: tg, Who: who, Action: action, Params: p, At: w.now()}, true)
	if err != nil {
		return nil, err
	}
	bg := context.WithoutCancel(ctx)
	if rec.ApplyError != "" || !rec.Applied {
		why := rec.ApplyError
		if why == "" {
			why = "нечего менять"
		}
		_ = w.repo.WriteRejected(bg, rec.ID, why, false)
		w.changed()
		return nil, errors.New(why)
	}
	if !w.Cutover.Pending(ctx) {
		if err := w.repo.WriteSent(bg, rec.ID, "на сервере"); err != nil {
			log.Printf("club write %d: %v", rec.ID, err)
		}
	}
	w.tablesChanged()
	w.gw.dropBundles()
	w.changed()
	return rec, nil
}

// duplicate answers a write the club already has, as the script did.
func (w *ClubWrites) duplicate(ctx context.Context, action string, p map[string]string) []byte {
	if action != "addSchedule" || w.gw.Club == nil {
		return nil
	}
	d, ok := club.Date(strings.TrimSpace(p["date"]))
	if !ok {
		return nil
	}
	snap, err := w.gw.Club.LoadBundle(ctx, w.now().Add(-bundleLoadSpan))
	if err != nil || snap == nil {
		return nil
	}
	for _, m := range snap.Meetings {
		if strings.TrimSpace(m.Resident) == strings.TrimSpace(p["res"]) && m.Date.Format("2006-01-02") == d.Format("2006-01-02") {
			return mustJSON(map[string]any{"ok": true, "duplicate": true, "message": "У резидента уже есть встреча в этот день"})
		}
	}
	return nil
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, "|") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// offlineNames: who an offline day is for (as the applier picks them).
func offlineNames(list []club.Resident) []string {
	var out []string
	for _, r := range list {
		f := strings.TrimSpace(r.Format)
		if r.Archived || r.Former || r.Exception || r.Name == "" || (f != "" && f != "Офлайн") {
			continue
		}
		out = append(out, r.Name)
	}
	return out
}

func (w *ClubWrites) residentsList(ctx context.Context) []club.Resident {
	if rl, ok := w.repo.(residentLoader); ok {
		if list, err := rl.LoadResidents(ctx); err == nil {
			return list
		}
	}
	return nil
}

// answer is what the app gets, shaped as the script's answers.
func (w *ClubWrites) answer(ctx context.Context, action string, p map[string]string) []byte {
	out := map[string]any{"ok": true}
	switch action {
	case "markAttendance":
		out["marked"] = splitNames(p["names"])
	case "addOfflineGroup":
		names := offlineNames(w.residentsList(ctx))
		out["count"], out["names"] = len(names), names
	}
	return mustJSON(out)
}

// notices sends the messages the script sent for a write.
func (w *ClubWrites) notices(ctx context.Context, action string, p map[string]string) {
	n := w.Notify
	if n == nil || n.Send == nil {
		return
	}
	list := w.residentsList(ctx)
	tgOf := func(name string) int64 {
		for _, r := range list {
			if strings.TrimSpace(r.Name) == strings.TrimSpace(name) && r.TgID != 0 && !r.Archived {
				return r.TgID
			}
		}
		return 0
	}
	sched := map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "📱 Открыть в BS", "web_app": map[string]string{"url": bot.WebAppBase + "?p=schedule"}}}}}
	send := func(name, text string, kb map[string]any) {
		if n.Resident != nil {
			if err := n.Resident(ctx, "notice", action+"|"+time.Now().In(club.Almaty).Format("2006-01-02")+"|"+text, name, tgOf(name), text, kb); err != nil {
				log.Printf("club write notice %s: %v", action, err)
			}
			return
		}
		if id := tgOf(name); id != 0 {
			if err := n.Send(ctx, id, text, kb); err != nil {
				log.Printf("club write notice %s: %v", action, err)
			}
		}
	}
	switch action {
	case "addFine":
		amount, _ := strconv.ParseInt(strings.TrimSpace(p["amount"]), 10, 64)
		if amount == 0 {
			amount = 10000
		}
		typ := p["type"]
		if typ == "" {
			typ = "Штраф"
		}
		send(p["name"], fmt.Sprintf("%s, выставлен штраф %s тг (%s).\nОплатить: %s", firstWord(p["name"]), bot.Money(amount), typ, bot.KaspiLink), nil)
	case "addSchedule":
		t := "Назначена встреча\n\nДата: " + p["date"] + "\nВремя: " + p["time"]
		if l := strings.TrimSpace(p["link"]); l != "" {
			t += "\nФормат: онлайн\nСсылка: " + l
		} else if pl := strings.TrimSpace(p["place"]); pl != "" {
			t += "\nФормат: офлайн\nМесто: " + pl
		}
		send(p["res"], t+"\n\nЕсли время не подходит, напишите заранее", nil)
	case "deleteSchedule":
		send(p["res"], strings.TrimSpace("❌ Встреча отменена\n📅 "+p["date"]+" "+p["time"]), sched)
	case "updateMeeting":
		nd, nt := p["newDate"], p["newTime"]
		if nd == "" {
			nd = p["oldDate"]
		}
		if nt == "" {
			nt = p["oldTime"]
		}
		send(p["oldRes"], "🔄 Встреча перенесена\n📅 Было: "+strings.TrimSpace(p["oldDate"]+" "+p["oldTime"])+"\n📅 Стало: "+strings.TrimSpace(nd+" "+nt), sched)
	case "markAttendance":
		for _, name := range splitNames(p["names"]) {
			send(name, "✅ Встреча "+p["date"]+" подтверждена\nГалочка в посещениях поставлена", sched)
		}
	case "addOfflineGroup":
		names := offlineNames(list)
		addr := "г.Алматы, Достык 44"
		if s, ok := w.repo.(interface {
			Setting(ctx context.Context, key string) (string, error)
		}); ok {
			if v, _ := s.Setting(ctx, "offline_address"); strings.TrimSpace(v) != "" {
				addr = v
			}
		}
		if n.Topic != nil && len(names) > 0 {
			msg := "📌 Офлайн день BS\n📅 " + p["date"] + "\n⏰ " + p["time"] + "\n📍 " + addr + "\n\n👥 Все офлайн-резиденты:\n• " +
				strings.Join(names, "\n• ") + "\n\n⚠️ За опоздание штраф 10 000 тг"
			if err := n.Topic(ctx, bot.DefaultGroupID, ImportantTopic, msg); err != nil {
				log.Printf("club write notice offline group: %v", err)
			}
		}
		for _, name := range names {
			send(name, "📌 Офлайн встреча!\n📅 "+p["date"]+" "+p["time"]+"\n📍 "+addr, sched)
		}
	case "addLead":
		t := "🔥 Новый лид\n\n"
		for _, x := range [][2]string{{"👤 ", p["name"]}, {"📱 ", p["phone"]}, {"💬 ", p["telegram"]}} {
			if strings.TrimSpace(x[1]) != "" {
				t += x[0] + strings.TrimSpace(x[1]) + "\n"
			}
		}
		src := strings.TrimSpace(p["source"])
		if src == "" {
			src = "не указан"
		}
		t += "📍 Источник: " + src
		if v := strings.TrimSpace(p["campaign"]); v != "" {
			t += "\n🎯 " + v
		}
		if v := strings.TrimSpace(p["niche"]); v != "" {
			t += "\n💼 " + v
		}
		if v := []rune(strings.TrimSpace(p["request"])); len(v) > 0 {
			if len(v) > 200 {
				v = v[:200]
			}
			t += "\n\n📝 " + string(v)
		}
		for _, id := range n.Admins {
			_ = n.Send(ctx, id, t, nil)
		}
	case "submitDiagnosticRequest":
		name, phone := strings.TrimSpace(p["name"]), strings.TrimSpace(p["phone"])
		t := "⚡ Новая заявка на диагностику\n\n👤 " + name + "\n📞 " + phone + "\n"
		if v := strings.TrimSpace(p["niche"]); v != "" {
			t += "📌 " + v + "\n"
		}
		if v := strings.TrimSpace(p["request"]); v != "" {
			t += "\n💬 " + v + "\n"
		}
		t += "\n🆔 " + p["chatId"]
		for _, id := range n.Admins {
			_ = n.Send(ctx, id, t, nil)
		}
		if id, err := strconv.ParseInt(p["chatId"], 10, 64); err == nil && id > 0 {
			_ = n.Send(ctx, id, "✅ "+name+", заявка принята!\n\nМы свяжемся с тобой в течение дня по WhatsApp "+phone+
				" чтобы согласовать время диагностики.\n\nЭкспресс-разбор 50 000 ₸ ведут оба основателя (Рустам и Береке). 1 час, точный диагноз и план задач на 10 дней.", nil)
		}
	}
}

// ImportantTopic is the group's «ВАЖНОЕ» topic (the script's IMPORTANT_TOPIC_ID).
const ImportantTopic = 1980

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// ── The app's other calls ──

// rawSheets gives the sheets the server keeps as displayed.
type rawSheets interface {
	Sheets(ctx context.Context) (club.Sheets, error)
}

// ownCall answers an app action after the cutover; the script is never asked.
func (g *AppGateway) ownCall(c *gin.Context, u *platformTgUser, action string, in url.Values) {
	ctx := c.Request.Context()
	_, team := g.Admins[u.ID]
	ok := func(v map[string]any) {
		if v == nil {
			v = map[string]any{}
		}
		if _, has := v["error"]; !has {
			v["ok"] = true
		}
		g.note(true, u.ID)
		c.JSON(http.StatusOK, v)
	}
	fail := func(msg string) { c.JSON(http.StatusOK, gin.H{"error": msg}) }
	raw := func() club.Sheets {
		if rs, okk := g.Club.(rawSheets); okk {
			if s, err := rs.Sheets(ctx); err == nil {
				return s
			}
		}
		return club.Sheets{}
	}
	switch action {
	case "saveAvatar":
		ok(nil) // the server reads Telegram photos itself (app_avatar.go)
	case "getMonthlyPL":
		srv, _, err := g.ServerBundle(ctx, u.ID)
		if err != nil {
			fail("Нет данных PL")
			return
		}
		var pl map[string]any
		_ = json.Unmarshal(srv["monthlyPL"], &pl)
		if pl == nil {
			fail("Нет данных PL")
			return
		}
		ok(pl)
	case "getOfflineResidents":
		snap, err := g.Club.LoadBundle(ctx, g.now().Add(-bundleLoadSpan))
		if err != nil || snap == nil {
			fail("Нет данных")
			return
		}
		var onl, off []map[string]any
		for _, r := range snap.Residents {
			if r.Name == "" || r.Former || r.Exception || r.Admin || r.Archived {
				continue
			}
			f := strings.TrimSpace(r.Format)
			if f == "" {
				f = "Офлайн"
			}
			cid := ""
			if r.TgID != 0 {
				cid = strconv.FormatInt(r.TgID, 10)
			}
			it := map[string]any{"name": r.Name, "res": r.Name, "format": f, "chatId": cid}
			if f == "Онлайн" {
				onl = append(onl, it)
			} else {
				off = append(off, it)
			}
		}
		by := func(l []map[string]any) {
			sort.Slice(l, func(i, j int) bool { return l[i]["name"].(string) < l[j]["name"].(string) })
		}
		by(onl)
		by(off)
		ok(map[string]any{"residents": append(append([]map[string]any{}, onl...), off...), "onlineCount": len(onl), "offlineCount": len(off)})
	case "getWheel":
		name := strings.TrimSpace(in.Get("name"))
		all := club.WheelAll(raw())
		w := all[name]
		if w == nil {
			life := club.WheelLifeAxes
			if cu := club.WheelAxes(club.ScriptProps(raw()), name); cu != nil {
				life = append([]string{"Бизнес"}, cu...)
			}
			w = &club.WheelOf{DNAAxes: club.WheelDNAAxes, LifeAxes: life, Businesses: []club.WheelBiz{}, DNA: []club.WheelRec{}, Life: []club.WheelRec{}}
		}
		if in.Get("history") != "1" { // the script gives the last two of each kind
			cut2 := func(r []club.WheelRec) []club.WheelRec {
				if len(r) > 2 {
					return r[:2]
				}
				return r
			}
			for i := range w.Businesses {
				w.Businesses[i].Rows = cut2(w.Businesses[i].Rows)
			}
			w.DNA, w.Life = cut2(w.DNA), cut2(w.Life)
		}
		b, _ := json.Marshal(w)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		ok(m)
	case "getProfit":
		name := strings.TrimSpace(in.Get("name"))
		var recs []map[string]any
		for i, r := range raw()[club.SheetProfit] {
			if i == 0 || len(r) < 4 || strings.TrimSpace(r[1]) != name || name == "" {
				continue
			}
			num := func(s string) int64 {
				n, _ := strconv.ParseInt(strings.Map(func(c rune) rune {
					if c >= '0' && c <= '9' || c == '-' {
						return c
					}
					return -1
				}, s), 10, 64)
				return n
			}
			d := r[0]
			if t, okd := club.Date(d); okd {
				d = t.Format("02.01.06")
			}
			recs = append(recs, map[string]any{"date": d, "revenue": num(r[2]), "profit": num(r[3])})
		}
		if recs == nil {
			recs = []map[string]any{}
		}
		ok(map[string]any{"records": recs})
	case "getCustdevResponses":
		if !team {
			fail("Только для команды")
			return
		}
		all := []map[string]any{}
		by := map[string][]map[string]any{}
		for i, r := range raw()[club.SheetNPS] {
			if i == 0 || len(r) < 4 || strings.TrimSpace(r[1]) == "" {
				continue
			}
			it := map[string]any{"date": r[0], "name": r[1], "chatId": r[2], "text": r[3]}
			all = append(all, it)
			by[r[1]] = append(by[r[1]], it)
		}
		ok(map[string]any{"responses": all, "byResident": by})
	case "getSubscribers":
		if !team {
			fail("Только для команды")
			return
		}
		// The bot's subscribers live in the platform's CRM since the lead funnel
		// moved to the server; the sheet's list came over as an archive.
		var list []map[string]any
		for i, r := range raw()["Подписчики канала"] {
			if i == 0 || len(r) < 2 || strings.TrimSpace(r[1]) == "" {
				continue
			}
			it := map[string]any{"date": cell(r, 0), "chatId": cell(r, 1), "username": cell(r, 2), "name": cell(r, 3), "status": cell(r, 4)}
			list = append(list, it)
		}
		if list == nil {
			list = []map[string]any{}
		}
		ok(map[string]any{"subscribers": list, "needCheck": false, "archive": true})
	case "checkChannelMembership":
		ok(map[string]any{"checked": 0, "message": "Подписчики теперь в CRM на платформе"})
	case "requestPhone":
		if g.Contact == nil {
			fail("Бот недоступен")
			return
		}
		reason := strings.TrimSpace(in.Get("reason"))
		if reason == "" {
			reason = "Чтобы отправить результат диагностики и связаться с вами, нужен номер телефона.\n\nНажмите кнопку ниже. номер подставится автоматически из вашего профиля Telegram."
		}
		if err := g.Contact(ctx, u.ID, reason); err != nil {
			fail("Не удалось отправить запрос в чат")
			return
		}
		ok(nil)
	default:
		// Calendar events, SMM and content tools, channel moderation, AI
		// analysis: these lived in Google's services next to the sheet.
		fail("Эта функция перенесена на платформу BS")
	}
}

func cell(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return ""
}

// ownPost answers the app's POST (pictures) after the cutover.
func (g *AppGateway) ownPost(c *gin.Context, u *platformTgUser, action string, body map[string]any) {
	if action != "sendImage" || g.Photo == nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Загрузка картинок перенесена на платформу BS"})
		return
	}
	b64, _ := body["image"].(string)
	caption, _ := body["caption"].(string)
	if i := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && i > 0 {
		b64 = b64[i+1:]
	}
	img, err := decodeB64(b64)
	if err != nil || len(img) == 0 {
		c.JSON(http.StatusOK, gin.H{"error": "Нет данных"})
		return
	}
	key := fmt.Sprintf("story_%d_%d", u.ID, g.now().UnixNano())
	if err := g.Photo(c.Request.Context(), u.ID, key, img, caption, nil); err != nil {
		c.JSON(http.StatusOK, gin.H{"error": "Telegram не принял картинку"})
		return
	}
	g.note(true, u.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// callOff is the gateway's Call after the cutover.
func (g *AppGateway) callOff(c *gin.Context, u *platformTgUser, action string, in, q url.Values) {
	ctx := c.Request.Context()
	if g.portedCall(c, u, action, in) { // R32d: app_ported.go
		return
	}
	switch {
	case action == "getBotCache":
		if g.Club == nil || !g.serveServerBundle(c, q, u, in.Get("fresh") == "1") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Данные временно недоступны, попробуйте через минуту"})
		}
		return
	case action == "checkUserRole":
		g.roleOff(c, u)
		return
	case action == "requestResident":
		if !g.claimFromApp(c, u) {
			c.JSON(http.StatusOK, gin.H{"error": "Заявка не принята, напишите куратору"})
		}
		return
	case pg.ClubWriteActions[action] && g.Writes != nil:
		_, team := g.Admins[u.ID]
		body := g.Writes.Do(ctx, "app", u, action, q, team)
		g.dropBundles()
		g.logOp(ctx, "app", u, action, q, body)
		if action == "confirmMeeting" || action == "markAttendance" {
			g.noteDone(action, map[string]string{"res": in.Get("res"), "date": in.Get("date"), "time": in.Get("time"), "names": in.Get("names")})
		}
		g.note(true, u.ID)
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	g.ownCall(c, u, action, in)
}

// roleOff: who opened the app, from the server's data only.
func (g *AppGateway) roleOff(c *gin.Context, u *platformTgUser) {
	g.note(true, u.ID)
	if _, team := g.Admins[u.ID]; team {
		c.JSON(http.StatusOK, gin.H{"role": "admin", "source": "server"})
		return
	}
	if g.Club != nil && g.serverRole(c, u) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"role": "lead", "source": "server"})
}

// Reset drops every bundle kept: the data changed under them (the cutover).
func (g *AppGateway) Reset() { g.dropBundles() }

// LeadFromScript godoc
// @Summary  A lead from Tilda or an ad form, passed on by the dormant script
// @Description  Signed by the script (X-BS-Signature = HMAC-SHA256 of the body with the bot token). Body {ts, lead:{name, phone, telegram, source, campaign, niche, revenue, request, comment}}. Kept in «CRM Лиды» on the server, the team is told. The forms keep posting to the script's address; it forwards them here.
// @Tags     app
// @Router   /api/v1/bot/lead [post]
func (g *AppGateway) LeadFromScript(c *gin.Context) {
	if g.Writes == nil || g.token == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if !VerifyBotSignature(body, c.GetHeader("X-BS-Signature"), g.token) {
		if g.OnLead != nil {
			g.OnLead(c.Request.Context(), nil, errors.New("подпись скрипта не совпала: у скрипта другой токен бота, чем TELEGRAM_BOT_TOKEN сервера"))
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return
	}
	var req struct {
		TS   int64             `json:"ts"`
		Lead map[string]string `json:"lead"`
	}
	if json.Unmarshal(body, &req) != nil || req.Lead == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if d := time.Since(time.Unix(req.TS, 0)); d > time.Hour || d < -5*time.Minute {
		if g.OnLead != nil {
			g.OnLead(c.Request.Context(), nil, errors.New("подпись скрипта устарела"))
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return
	}
	p := map[string]string{}
	for _, k := range []string{"name", "phone", "telegram", "source", "campaign", "niche", "revenue", "request", "comment"} {
		if v := strings.TrimSpace(req.Lead[k]); v != "" {
			if r := []rune(v); len(r) > 500 {
				v = string(r[:500])
			}
			p[k] = v
		}
	}
	ctx := c.Request.Context()
	_, err = g.Writes.Local(ctx, "form", 0, "Форма на сайте", "addLead", p)
	if g.OnLead != nil {
		g.OnLead(ctx, p, err)
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	go g.Writes.notices(context.WithoutCancel(ctx), "addLead", p)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
