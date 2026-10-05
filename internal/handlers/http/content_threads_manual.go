package http

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/gin-gonic/gin"
)

// Ручной Threads: бесплатный режим, пока нет THREADS_TOKEN (проверку
// разработчика Meta владелец пройти не может).
//
// Пачка дня та же (польза из библиотеки и ИИ), но постов меньше: 4 в день
// (настройка «Постов в день (вручную)», manualPerDay), около 09:30, 12:30,
// 16:30 и 19:30 по Алматы, у каждого дня своё смещение. В своё время бот
// присылает владельцу (первый id PLATFORM_TEAM) одно сообщение: готовый
// текст поста и кнопки
//
//	«Опубликовать в Threads»  ссылка https://www.threads.net/intent/post?text=…
//	                          (открывает окно нового поста с текстом, в приложении на телефоне)
//	«✅ Опубликовал»          пост отмечается опубликованным вручную (byHand, время)
//	«Другой пост»             текст меняется местами со следующим постом плана (или из библиотеки)
//	«Пропустить»
//
// Сообщения только с 09:00 до 21:00. Нет отметки 3 часа: пост остаётся
// «не опубликован» (missed), без напоминаний; следующий придёт по плану.
// В каждом посте своя ссылка на бот t.me/bsurgery_bot?start=th_p<ггММддччмм>:
// лиды в CRM видны как «Threads: пост 05.10 14:48», SMM считает их по посту.

const (
	thManualDef   = 4
	thManualMax   = 12
	thManualFrom  = 9 * 60  // no messages before 09:00
	thManualTo    = 21 * 60 // nor after 21:00
	thManualWait  = 3 * time.Hour
	thManualGap   = 30 * time.Minute // two posts are never sent closer than this
	thIntentBase  = "https://www.threads.net/intent/post?text="
	thIntentMax   = 2000 // a longer URL button goes through the server's short link
	thManualCache = time.Minute
)

func (s contentSettings) manualPerDay() int {
	n := s.Channels.Threads.ManualPerDay
	if n <= 0 {
		n = thManualDef
	}
	if n > thManualMax {
		n = thManualMax
	}
	return n
}

// manual: Threads has no token (checked at most once a minute).
func (e *ContentEngine) manual(ctx context.Context) bool {
	if e.ThreadsManual == nil {
		return false
	}
	e.manMu.Lock()
	defer e.manMu.Unlock()
	if now := e.now(); e.manAt.IsZero() || now.Sub(e.manAt) >= thManualCache || now.Before(e.manAt) {
		e.manVal, e.manAt = e.ThreadsManual(ctx), now
	}
	return e.manVal
}

// stCached: the settings with the last known mode (no lookup).
func (e *ContentEngine) stCached(d *contentDoc) contentSettings {
	st := parseContentSettings(d.Settings)
	if e.ThreadsManual != nil {
		e.manMu.Lock()
		st.manual = e.manVal
		e.manMu.Unlock()
	}
	return st
}

// settings: the doc's settings and the current mode.
func (e *ContentEngine) settings(ctx context.Context, d *contentDoc) contentSettings {
	st := parseContentSettings(d.Settings)
	st.manual = e.manual(ctx)
	return st
}

// ── Times ──

var thManualAnchors = map[int][]int{
	1: {12*60 + 30},
	2: {12*60 + 30, 19*60 + 30},
	3: {9*60 + 30, 13*60 + 30, 19*60 + 30},
	4: {9*60 + 30, 12*60 + 30, 16*60 + 30, 19*60 + 30},
}

// threadsManualSlots: n times within 09:10-20:50, the 4 anchors moved by up
// to 12 minutes a day (never on :00, :15, :30, :45); more posts spread evenly.
func threadsManualSlots(day time.Time, n int) []time.Time {
	if n < 1 {
		return nil
	}
	a, ok := thManualAnchors[n]
	if !ok {
		return threadsSlots(day, n, thManualFrom+10, thManualTo-10)
	}
	base := dayStart(day)
	key := contentDay(base)
	out := make([]time.Time, 0, n)
	for i, m := range a {
		m += hashN(key+"/m"+fmt.Sprint(i), 25) - 12
		for m%15 == 0 {
			m++
		}
		out = append(out, base.Add(time.Duration(m)*time.Minute))
	}
	return out
}

func inManualHours(t time.Time) bool {
	a := t.In(almaty)
	m := a.Hour()*60 + a.Minute()
	return m >= thManualFrom && m < thManualTo
}

// ── The post's text and link ──

// thPostParam: the post's own start code, th_p + its slot (ггММддччмм).
func thPostParam(it *contentItem) string {
	at, ok := parseContentAt(it.At)
	if !ok {
		return ""
	}
	return "th_p" + at.In(almaty).Format("0601021504")
}

var (
	thStartRe   = regexp.MustCompile(`t\.me/bsurgery_bot\?start=[A-Za-z0-9_-]+`)
	thPostRe    = regexp.MustCompile(`^p(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$`)
	thManLines  = []string{"Чек-листы для собственника, бесплатно: ", "Ещё чек-листы в боте: "}
	thManLinesS = "Бот: "
)

// thPostSource: the CRM label of a post's code (p2610051448 → «пост 05.10 14:48»).
func thPostSource(rest string) string {
	m := thPostRe.FindStringSubmatch(rest)
	if m == nil {
		return ""
	}
	return "Threads: пост " + m[3] + "." + m[2] + " " + m[4] + ":" + m[5]
}

// manualize gives a post its own bot link: the link in the text becomes
// th_p…, a text without one gets a short last line when it fits 500 signs.
// A series becomes one post (the parts are added when they fit). Idempotent.
func manualize(it *contentItem) {
	p := thPostParam(it)
	if p == "" || it.Channel != "threads" {
		return
	}
	link := contentBotLink + p
	text := strings.TrimSpace(it.Text)
	if len(it.Parts) > 0 {
		whole := text + "\n\n" + strings.Join(it.Parts, "\n\n")
		if utf8.RuneCountInString(thStartRe.ReplaceAllString(whole, link)) <= threadsMaxText {
			text = whole
		}
		it.Parts = nil
	}
	if thStartRe.MatchString(text) {
		text = thStartRe.ReplaceAllString(text, link)
	} else {
		added := false
		for _, l := range append(append([]string{}, thManLines...), thManLinesS) {
			if x := text + "\n\n" + l + link; utf8.RuneCountInString(x) <= threadsMaxText {
				text, added = x, true
				break
			}
		}
		if !added {
			link = ""
		}
	}
	it.Text, it.Link = text, link
}

// thIntentURL: Threads' web intent with the text: on a phone it opens the
// app's new post with the text in it, on a computer threads.net.
func thIntentURL(text string) string {
	return thIntentBase + strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

// publicBase: the server's address (from PLATFORM_URL), for the short link.
func (e *ContentEngine) publicBase() string {
	u, err := url.Parse(e.PlatformURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// publishURL: the intent link, or the server's short link when the intent
// is too long for a Telegram button.
func (e *ContentEngine) publishURL(it *contentItem, short bool) string {
	if u := thIntentURL(it.Text); len(u) <= thIntentMax && !short {
		return u
	}
	if b := e.publicBase(); b != "" {
		return b + "/go/th/" + url.PathEscape(it.ID)
	}
	return ""
}

func cbData(s string) string {
	if len(s) > 64 {
		return ""
	}
	return s
}

func (e *ContentEngine) manualKB(it *contentItem, short bool) map[string]any {
	var rows [][]map[string]any
	switch it.Status {
	case "published":
		rows = append(rows, row(map[string]any{"text": "✅ Опубликован " + atClock(it.PublishedAt), "callback_data": cbData("cnt_th_done_" + it.ID)}))
	case "skipped":
		rows = append(rows, row(map[string]any{"text": "⏭ Пропущен", "callback_data": cbData("cnt_th_done_" + it.ID)}))
	default:
		if u := e.publishURL(it, short); u != "" {
			rows = append(rows, row(map[string]any{"text": "Опубликовать в Threads", "url": u}))
		}
		rows = append(rows, row(map[string]any{"text": "✅ Опубликовал", "callback_data": cbData("cnt_th_ok_" + it.ID)}))
		rows = append(rows, row(map[string]any{"text": "Другой пост", "callback_data": cbData("cnt_th_next_" + it.ID)},
			map[string]any{"text": "Пропустить", "callback_data": cbData("cnt_th_skip_" + it.ID)}))
	}
	return map[string]any{"inline_keyboard": rows}
}

// isURLRefusal: Telegram did not take the button's link.
func isURLRefusal(err error) bool {
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "button_url") || strings.Contains(low, "url") && strings.Contains(low, "button") || strings.Contains(low, "too long")
}

// ── Sending ──

// manualDue: the post whose time came goes to the owner (one at a time,
// 09:00-21:00); a sent post without a mark for 3 hours becomes «не опубликован».
func (e *ContentEngine) manualDue(ctx context.Context) {
	now := e.now()
	send := ""
	_, err := e.update(ctx, func(d *contentDoc) bool {
		send = ""
		st := e.stCached(d)
		if !st.Channels.Threads.On {
			return false
		}
		changed := false
		var lastSent time.Time
		var due []*contentItem
		for _, it := range d.Queue {
			if it.Channel != "threads" {
				continue
			}
			if t, ok := parseContentAt(it.SentAt); ok && t.After(lastSent) {
				lastSent = t
			}
			switch it.Status {
			case "sent":
				if t, ok := parseContentAt(it.SentAt); !ok || now.Sub(t) >= thManualWait {
					it.Status, it.Error = "missed", "Не опубликован: нет отметки за 3 часа"
					changed = true
				}
			case "planned", "approved":
				at, ok := parseContentAt(it.At)
				if !ok || at.After(now) {
					continue
				}
				switch {
				case now.Sub(at) > threadsLateBatch:
					it.Status, it.Error = "skipped", "Не отправлен вовремя: сервер был недоступен"
					changed = true
				case !inManualHours(now):
					it.Status, it.Error = "skipped", "Время вне 09:00-21:00: бот не пишет, чтобы не беспокоить"
					changed = true
				default:
					due = append(due, it)
				}
			}
		}
		if len(due) == 0 {
			return changed
		}
		// the latest one goes; earlier ones that waited are not sent in a heap
		last := due[0]
		for _, it := range due {
			if it.At > last.At {
				last = it
			}
		}
		for _, it := range due {
			if it != last {
				it.Status, it.Error = "skipped", "Пропущен, чтобы посты не шли пачкой"
				changed = true
			}
		}
		if !lastSent.IsZero() && now.Sub(lastSent) < thManualGap {
			last.Status, last.Error = "skipped", "Пропущен: предыдущий пост пришёл меньше 30 минут назад"
			return true
		}
		send = last.ID
		return changed
	})
	if err != nil {
		log.Printf("content: manual threads: %v", err)
		return
	}
	if send != "" {
		_, _ = e.sendManual(ctx, send, false)
	}
}

// sendManual sends the post to the owner with the buttons and marks it sent.
func (e *ContentEngine) sendManual(ctx context.Context, id string, now bool) (*contentItem, error) {
	d, _, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	it := findContent(d, id)
	if it == nil {
		return nil, errContentNotFound
	}
	cp := *it
	manualize(&cp)
	if e.Send == nil || e.Owner == 0 {
		return it, fmt.Errorf("Ручной Threads: бот не настроен, пост не отправлен")
	}
	cp.Status = "sent"
	mid, serr := e.Send(ctx, e.Owner, cp.Text, e.manualKB(&cp, false))
	if serr != nil && isURLRefusal(serr) {
		mid, serr = e.Send(ctx, e.Owner, cp.Text, e.manualKB(&cp, true))
	}
	at := e.now().UTC().Format(time.RFC3339)
	var out *contentItem
	_, uerr := e.update(ctx, func(d *contentDoc) bool {
		x := findContent(d, id)
		if x == nil {
			return false
		}
		x.Text, x.Link, x.Parts = cp.Text, cp.Link, nil
		if serr != nil {
			x.Status, x.Error = "missed", "Бот не отправил пост: "+serr.Error()
		} else {
			x.Status, x.SentAt, x.MsgID, x.Error, x.RetryAt = "sent", at, mid, "", ""
		}
		out = x
		return true
	})
	if uerr != nil {
		log.Printf("content: manual threads %s: %v", id, uerr)
	}
	if serr != nil {
		log.Printf("content: manual threads %s: send: %v", id, serr)
		return out, serr
	}
	if out == nil {
		out = &cp
	}
	return out, nil
}

// manualCallback answers cnt_th_ok_/skip_/next_/done_<id>.
func (e *ContentEngine) manualCallback(ctx context.Context, cb bot.CallbackUpdate, data string) string {
	act, id, ok := strings.Cut(data, "_")
	if !ok || id == "" {
		return ""
	}
	if act == "done" {
		return "Уже отмечено"
	}
	by := fmt.Sprintf("tg:%d", cb.FromID)
	now := e.now()
	toast := "Пост уже не в очереди"
	var mem *threadsMemory
	if act == "next" {
		if d, _, err := e.load(ctx); err == nil {
			s, _ := e.state(ctx)
			mem = e.threadsMemory(d, s, now)
		}
	}
	var res *contentItem
	_, err := e.update(ctx, func(d *contentDoc) bool {
		res = nil
		it := findContent(d, id)
		if it == nil {
			toast = "Пост уже не в очереди"
			return false
		}
		open := it.Status == "sent" || it.Status == "missed" || it.Status == "planned" || it.Status == "approved"
		if !open {
			toast = "Пост уже " + map[string]string{"published": "отмечен опубликованным", "skipped": "пропущен", "failed": "не вышел"}[it.Status]
			res = it
			return false
		}
		switch act {
		case "ok":
			it.Status, it.ByHand, it.PublishedAt, it.ApprovedBy, it.Error = "published", true, now.UTC().Format(time.RFC3339), by, ""
			toast = "Отмечено: опубликован"
		case "skip":
			it.Status, it.ApprovedBy, it.Error = "skipped", by, "Пропущен владельцем"
			toast = "Пропущено"
		case "next":
			if !e.swapNext(d, it, now, mem) {
				toast = "Другого поста нет"
				res = it
				return false
			}
			toast = "Другой пост"
		default:
			return false
		}
		res = it
		return true
	})
	if err != nil {
		log.Printf("content: manual callback %s: %v", cb.Data, err)
		return "Не получилось, попробуйте ещё раз"
	}
	if res != nil && e.Edit != nil && cb.MessageID != 0 {
		cp := *res
		if err := e.Edit(ctx, cb.ChatID, cb.MessageID, cp.Text, e.manualKB(&cp, false)); err != nil && isURLRefusal(err) {
			err = e.Edit(ctx, cb.ChatID, cb.MessageID, cp.Text, e.manualKB(&cp, true))
			if err != nil {
				log.Printf("content: manual edit: %v", err)
			}
		} else if err != nil {
			log.Printf("content: manual edit: %v", err)
		}
	}
	return toast
}

// swapNext gives it the next planned Threads post's text (that slot gets
// this one's), or a fresh library post when the plan has none.
func (e *ContentEngine) swapNext(d *contentDoc, it *contentItem, now time.Time, mem *threadsMemory) bool {
	var next *contentItem
	var nextAt time.Time
	for _, x := range d.Queue {
		if x == it || x.Channel != "threads" || (x.Status != "planned" && x.Status != "approved") {
			continue
		}
		at, ok := parseContentAt(x.At)
		if !ok || !at.After(now) {
			continue
		}
		if next == nil || at.Before(nextAt) {
			next, nextAt = x, at
		}
	}
	if next != nil {
		a, b := it.contentItemData, next.contentItemData
		swap := func(dst *contentItemData, src contentItemData) {
			dst.Src, dst.V, dst.Organ, dst.Title, dst.Rubric, dst.Text, dst.Parts = src.Src, src.V, src.Organ, src.Title, src.Rubric, src.Text, src.Parts
			dst.Format, dst.Gen, dst.CTA, dst.Edited = src.Format, src.Gen, src.CTA, src.Edited
		}
		swap(&it.contentItemData, b)
		swap(&next.contentItemData, a)
		manualize(it)
		manualize(next)
		return true
	}
	if mem == nil {
		mem = &threadsMemory{src: map[string]time.Time{}, root: map[string]time.Time{}, first: map[string]bool{}, organ: map[string]int{}}
	}
	mem.remember(it.Text)
	li, v, tx := libThreadsPost(e.lib(), mem, false, "", contentDay(now)+"/next/"+it.ID, map[string]bool{})
	if li == nil {
		return false
	}
	it.Src, it.V, it.Organ, it.Title, it.Rubric, it.Text, it.Parts = li.Src, v, li.Organ, li.Title, li.Rubric, tx, nil
	it.Format, it.Gen, it.CTA, it.Edited = "library", "lib", false, false
	manualize(it)
	return true
}

// ── The platform and /status ──

type threadsMode struct {
	Manual bool `json:"manual"`
	PerDay int  `json:"perDay"`
}

// noteMode keeps bs_content.threadsMode in step with the mode (only on a change).
func (e *ContentEngine) noteMode(ctx context.Context) {
	if e.ThreadsManual == nil {
		return
	}
	d, _, err := e.load(ctx)
	if err != nil {
		return
	}
	st := e.settings(ctx, d)
	want := threadsMode{Manual: st.manual, PerDay: st.threadsPerDay()}
	if d.ThreadsMode != nil && *d.ThreadsMode == want {
		return
	}
	if _, err := e.update(ctx, func(d *contentDoc) bool {
		if d.ThreadsMode != nil && *d.ThreadsMode == want {
			return false
		}
		m := want
		d.ThreadsMode = &m
		return true
	}); err != nil {
		log.Printf("content: threads mode: %v", err)
	}
}

// ThreadsStatus: the /status line: the mode and today's published posts.
func (e *ContentEngine) ThreadsStatus(ctx context.Context) (state, text, sig string) {
	if e == nil || e.docs == nil {
		return "off", "нет данных", ""
	}
	d, _, err := e.load(ctx)
	if err != nil {
		return "warn", "нет данных: " + err.Error(), ""
	}
	st := e.settings(ctx, d)
	if !st.Channels.Threads.On {
		return "off", "выключен в настройках", "off"
	}
	today := contentDay(e.now())
	pub := 0
	for _, it := range d.Queue {
		if it.Channel == "threads" && it.Status == "published" {
			if t, ok := parseContentAt(it.PublishedAt); ok && contentDay(t) == today {
				pub++
			}
		}
	}
	n := st.threadsPerDay()
	if st.manual {
		return "ok", fmt.Sprintf("ручной режим, сегодня опубликовано %d из %d", pub, n), "manual"
	}
	return "ok", fmt.Sprintf("автопубликация, сегодня вышло %d из %d", pub, n), "auto"
}

// goThreads: GET /go/th/:id opens Threads' new post with the post's text
// (the bot's button when the text is too long for a Telegram link).
func (m *ContentModule) goThreads(c *gin.Context) {
	if m.E == nil || m.E.docs == nil {
		c.String(http.StatusServiceUnavailable, "Нет хранилища")
		return
	}
	d, _, err := m.E.load(c.Request.Context())
	if err != nil {
		c.String(http.StatusServiceUnavailable, "Не получилось открыть пост")
		return
	}
	id := c.Param("id")
	var it *contentItem
	for _, list := range [][]*contentItem{d.Queue, d.History} {
		for _, x := range list {
			if x.ID == id && x.Channel == "threads" {
				it = x
			}
		}
	}
	if it == nil || strings.TrimSpace(it.Text) == "" {
		c.String(http.StatusNotFound, "Пост не найден")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, thIntentURL(it.Text))
}
