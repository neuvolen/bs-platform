package http

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// Заявки с сайта (Tilda: bxclub.kz, business.surgery).
//
// Tilda шлёт вебхук формы прямо на сервер:
//
//	POST /api/v1/public/tilda/<ключ>
//
// (application/x-www-form-urlencoded или JSON; при подключении Tilda шлёт
// test=test и ждёт «ok»). Ключ: TILDA_TOKEN из окружения или выданный
// сервером один раз (bot_meta tilda:token); его можно передать и в пути, и
// параметром/заголовком «API key» из настроек вебхука Tilda.
//
// Пока Tilda смотрит на адрес Apps Script, скрипт пересылает заявку сюда,
// подписав тело токеном бота (X-BS-Signature, как bsSignedPost). Если сервер
// не ответил, скрипт сам записывает лида по-старому: заявка не теряется.
//
// Лид ложится в «CRM Лиды» (как addLead скрипта: дубль за 30 дней не
// добавляется) и в CRM платформы (bs_crm, слияние по телефону). Команда
// получает то же «🔥 Новый лид», что слал скрипт, один раз на заявку.

const (
	tildaMetaToken = "tilda:token"
	tildaMetaLast  = "tilda:last"
	tildaDedupe    = 10 * time.Minute
	tildaIPLimit   = 20  // заявок с одного адреса за tildaWindow
	tildaAllLimit  = 200 // всего за tildaWindow
	tildaWindow    = 10 * time.Minute
)

// TildaMeta: где сервер хранит ключ, отметку о последней заявке и дедуп (BotRepo).
type TildaMeta interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
	Once(ctx context.Context, key string, every time.Duration) (bool, error)
}

type TildaLeads struct {
	Writes   *ClubWrites // «CRM Лиды» и уведомление команде
	Docs     funnelDocs  // bs_crm
	Meta     TildaMeta
	BotToken string // подпись пересылки из скрипта
	// Notify: «🔥 Новый лид» команде; по умолчанию Writes.notices.
	Notify func(ctx context.Context, p map[string]string)

	now   func() time.Time
	mu    sync.Mutex
	hits  map[string][]time.Time
	all   []time.Time
	seen  map[string]time.Time // дедуп без Meta
	token string               // выданный ключ (кэш)
}

func NewTildaLeads(w *ClubWrites, docs funnelDocs, meta TildaMeta, botToken string) *TildaLeads {
	t := &TildaLeads{Writes: w, Docs: docs, Meta: meta, BotToken: strings.TrimSpace(botToken), now: time.Now,
		hits: map[string][]time.Time{}, seen: map[string]time.Time{}}
	if w != nil {
		t.Notify = func(ctx context.Context, p map[string]string) { w.notices(ctx, "addLead", p) }
	}
	return t
}

// Token: TILDA_TOKEN или ключ, выданный сервером (создаётся при первом обращении).
func (t *TildaLeads) Token(ctx context.Context) (tok string, fromEnv bool) {
	if v := strings.TrimSpace(os.Getenv("TILDA_TOKEN")); v != "" {
		return v, true
	}
	t.mu.Lock()
	tok = t.token
	t.mu.Unlock()
	if tok != "" || t.Meta == nil {
		return tok, false
	}
	if v, err := t.Meta.GetMeta(ctx, tildaMetaToken); err == nil && strings.TrimSpace(v) != "" {
		tok = strings.TrimSpace(v)
	} else if err == nil {
		b := make([]byte, 18)
		_, _ = rand.Read(b)
		tok = hex.EncodeToString(b)
		if err := t.Meta.SetMeta(ctx, tildaMetaToken, tok); err != nil {
			return "", false
		}
	} else {
		return "", false
	}
	t.mu.Lock()
	t.token = tok
	t.mu.Unlock()
	return tok, false
}

// tildaFields: поля заявки (ключи в нижнем регистре) из формы, JSON и адреса.
func tildaFields(r *http.Request, body []byte) map[string]string {
	f := map[string]string{}
	put := func(k, v string) {
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			return
		}
		if r := []rune(v); len(r) > 2000 {
			v = string(r[:2000])
		}
		if _, ok := f[k]; !ok {
			f[k] = v
		}
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	trimmed := bytes.TrimSpace(body)
	switch {
	case ct == "application/json" || (len(trimmed) > 0 && trimmed[0] == '{'):
		var m map[string]any
		if json.Unmarshal(trimmed, &m) == nil {
			for k, v := range m {
				switch x := v.(type) {
				case string:
					put(k, x)
				case float64, bool:
					put(k, fmt.Sprint(x))
				case nil:
				default:
					b, _ := json.Marshal(x)
					put(k, string(b))
				}
			}
		}
	case ct == "multipart/form-data":
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseMultipartForm(1 << 20); err == nil && r.MultipartForm != nil {
			for k, vs := range r.MultipartForm.Value {
				put(k, strings.Join(vs, ", "))
			}
		}
	default:
		if q, err := url.ParseQuery(string(body)); err == nil {
			for k, vs := range q {
				put(k, strings.Join(vs, ", "))
			}
		}
	}
	for k, vs := range r.URL.Query() {
		put(k, strings.Join(vs, ", "))
	}
	// Tilda «Отправлять cookies»: COOKIES=…; TILDAUTM=utm_source%3Dig%7C%7C%7Cutm_medium%3D…
	if ck := f["cookies"]; ck != "" {
		for _, part := range strings.Split(ck, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "TILDAUTM") {
				continue
			}
			if d, err := url.QueryUnescape(v); err == nil {
				v = d
			}
			for _, kv := range strings.FieldsFunc(v, func(r rune) bool { return r == '|' || r == '&' }) {
				if uk, uv, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(strings.ToLower(uk), "utm_") {
					if d, err := url.QueryUnescape(uv); err == nil {
						uv = d
					}
					put(uk, uv)
				}
			}
		}
	}
	return f
}

func tildaPick(f map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := f[k]; v != "" {
			return v
		}
	}
	return ""
}

// tildaPhone: +7XXXXXXXXXX для номеров Казахстана (8…, 7…, 10 цифр), иначе +цифры.
func tildaPhone(s string) string {
	d := phoneDigits(s)
	if len(d) < 7 {
		return ""
	}
	return "+" + d
}

func tildaTg(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://t.me/", "http://t.me/", "t.me/", "https://telegram.me/"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
		}
	}
	s = strings.TrimSpace(strings.Trim(s, "/"))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "@") && strings.Trim(s, "+0123456789 -()") != "" {
		s = "@" + s
	}
	return s
}

type tildaLead struct {
	Name, Phone, Email, Tg, Comment, Niche, Revenue string
	Form, TranID, FormID, Page                      string
	Source, Campaign                                string
	UTM                                             map[string]string
}

func tildaParse(f map[string]string) tildaLead {
	l := tildaLead{
		Name:    tildaPick(f, "name", "имя", "imya", "fio", "фио", "your_name", "username_name"),
		Phone:   tildaPhone(tildaPick(f, "phone", "телефон", "tel", "phone_number", "whatsapp")),
		Email:   tildaPick(f, "email", "e-mail", "почта"),
		Tg:      tildaTg(tildaPick(f, "telegram", "tg", "телеграм", "telegram_username", "ник_в_telegram")),
		Comment: tildaPick(f, "comment", "комментарий", "message", "сообщение", "request", "запрос", "вопрос", "textarea"),
		Niche:   tildaPick(f, "niche", "nisha", "ниша", "сфера", "sfera", "business"),
		Revenue: tildaPick(f, "revenue", "oborot", "оборот"),
		Form:    tildaPick(f, "formname", "form_name"),
		TranID:  tildaPick(f, "tranid"),
		FormID:  tildaPick(f, "formid"),
		Page:    tildaPick(f, "page", "referer", "url"),
		Source:  tildaPick(f, "source"),
		UTM:     map[string]string{},
	}
	for _, k := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"} {
		if v := f[k]; v != "" {
			l.UTM[k] = v
		}
	}
	l.Campaign = tildaPick(f, "utm_campaign", "campaign")
	if l.Source == "" {
		l.Source = "Сайт"
		if l.Form != "" {
			l.Source = "Сайт: " + l.Form
		}
	}
	if l.Name == "" && l.Email != "" {
		l.Name = l.Email
	}
	return l
}

func (l tildaLead) utmLine() string {
	var out []string
	for _, k := range []string{"utm_source", "utm_medium", "utm_content", "utm_term"} {
		if v := l.UTM[k]; v != "" {
			out = append(out, k+"="+v)
		}
	}
	return strings.Join(out, " · ")
}

// sheetParams: поля addLead («CRM Лиды»), как их собирал скрипт.
func (l tildaLead) sheetParams() map[string]string {
	p := map[string]string{"name": l.Name, "phone": l.Phone, "telegram": l.Tg, "source": l.Source,
		"campaign": l.Campaign, "niche": l.Niche, "revenue": l.Revenue, "request": l.Comment}
	var cm []string
	if l.Email != "" {
		cm = append(cm, "email: "+l.Email)
	}
	if u := l.utmLine(); u != "" {
		cm = append(cm, u)
	}
	if l.TranID != "" {
		cm = append(cm, "tranid "+l.TranID)
	}
	p["comment"] = strings.Join(cm, " · ")
	for k, v := range p {
		if v == "" {
			delete(p, k)
		} else if r := []rune(v); len(r) > 500 {
			p[k] = string(r[:500])
		}
	}
	return p
}

// limited: защита от наплыва (не для подписанной пересылки из скрипта).
func (t *TildaLeads) limited(ip string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	keep := func(ts []time.Time) []time.Time {
		out := ts[:0]
		for _, x := range ts {
			if now.Sub(x) < tildaWindow {
				out = append(out, x)
			}
		}
		return out
	}
	t.all = keep(t.all)
	h := keep(t.hits[ip])
	if len(h) >= tildaIPLimit || len(t.all) >= tildaAllLimit {
		t.hits[ip] = h
		return true
	}
	t.hits[ip] = append(h, now)
	t.all = append(t.all, now)
	if len(t.hits) > 5000 {
		for k, v := range t.hits {
			if len(keep(v)) == 0 {
				delete(t.hits, k)
			}
		}
	}
	return false
}

// first: true, если такого ключа не было последние 10 минут.
func (t *TildaLeads) first(ctx context.Context, key string) bool {
	if t.Meta != nil {
		if ok, err := t.Meta.Once(ctx, "tilda:"+key, tildaDedupe); err == nil {
			return ok
		}
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, at := range t.seen {
		if now.Sub(at) >= tildaDedupe {
			delete(t.seen, k)
		}
	}
	if _, ok := t.seen[key]; ok {
		return false
	}
	t.seen[key] = now
	return true
}

func tildaEq(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// authorized: подпись скрипта или ключ (путь, поле, заголовок «API key»).
func (t *TildaLeads) authorized(ctx context.Context, c *gin.Context, body []byte, f map[string]string) (ok, viaScript bool) {
	if sig := c.GetHeader("X-BS-Signature"); sig != "" && t.BotToken != "" && VerifyBotSignature(body, sig, t.BotToken) {
		var x struct {
			TS int64 `json:"ts"`
		}
		if json.Unmarshal(body, &x) == nil {
			if d := t.now().Sub(time.Unix(x.TS, 0)); d <= time.Hour && d >= -5*time.Minute {
				return true, true
			}
		}
		return false, false
	}
	tok, _ := t.Token(ctx)
	if tok == "" {
		return false, false
	}
	if tildaEq(strings.TrimSpace(c.Param("token")), tok) {
		return true, false
	}
	for _, k := range []string{"token", "api_key", "apikey", "key", "secret", "tilda_token"} {
		if tildaEq(f[k], tok) {
			return true, false
		}
	}
	for _, vs := range c.Request.Header {
		for _, v := range vs {
			v = strings.TrimSpace(v)
			if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
				v = strings.TrimSpace(v[7:])
			}
			if tildaEq(v, tok) {
				return true, false
			}
		}
	}
	return false, false
}

type tildaLast struct {
	At    string `json:"at"`
	Form  string `json:"form,omitempty"`
	Name  string `json:"name,omitempty"`
	Via   string `json:"via"` // tilda | script
	Test  bool   `json:"test,omitempty"`
	Dup   bool   `json:"dup,omitempty"`
	Count int    `json:"count"`
}

func (t *TildaLeads) mark(ctx context.Context, x tildaLast) {
	if t.Meta == nil {
		return
	}
	var prev tildaLast
	if v, err := t.Meta.GetMeta(ctx, tildaMetaLast); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &prev)
	}
	x.Count = prev.Count
	if !x.Test && !x.Dup {
		x.Count++
	}
	x.At = t.now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(x)
	if err := t.Meta.SetMeta(ctx, tildaMetaLast, string(b)); err != nil {
		log.Printf("tilda: last: %v", err)
	}
}

// Hook: POST /api/v1/public/tilda[/<ключ>]
func (t *TildaLeads) Hook(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too_big"})
		return
	}
	ctx := c.Request.Context()
	f := tildaFields(c.Request, body)
	ok, viaScript := t.authorized(ctx, c, body, f)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_token"})
		return
	}
	via := "tilda"
	if viaScript {
		via = "script"
	}
	// Проверка подключения в Tilda.
	if strings.EqualFold(f["test"], "test") {
		t.mark(context.WithoutCancel(ctx), tildaLast{Via: via, Test: true})
		c.String(http.StatusOK, "ok")
		return
	}
	if !viaScript && t.limited(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many"})
		return
	}
	l := tildaParse(f)
	if l.Name == "" && l.Phone == "" && l.Tg == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Пустой лид"})
		return
	}
	// Дубль за 10 минут: та же заявка (tranid) или тот же телефон.
	dup := false
	if l.TranID != "" && !t.first(ctx, "tran:"+l.TranID) {
		dup = true
	}
	if !dup && l.Phone != "" && !t.first(ctx, "ph:"+phoneDigits(l.Phone)) {
		dup = true
	}
	if !dup && l.Phone == "" && l.Tg != "" && !t.first(ctx, "tg:"+strings.ToLower(l.Tg)) {
		dup = true
	}
	if dup {
		t.mark(context.WithoutCancel(ctx), tildaLast{Via: via, Form: l.Form, Name: l.Name, Dup: true})
		c.JSON(http.StatusOK, gin.H{"ok": true, "duplicate": true})
		return
	}
	res, err := t.Save(ctx, l, via)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": err.Error()})
		return
	}
	t.mark(context.WithoutCancel(ctx), tildaLast{Via: via, Form: l.Form, Name: l.Name})
	c.JSON(http.StatusOK, res)
}

// Save кладёт заявку в «CRM Лиды» и bs_crm и один раз сообщает команде.
func (t *TildaLeads) Save(ctx context.Context, l tildaLead, via string) (gin.H, error) {
	p := l.sheetParams()
	stored, sheetNew := false, false
	if t.Writes != nil {
		if _, err := t.Writes.Local(ctx, "form", 0, "Форма на сайте", "addLead", p); err == nil {
			stored, sheetNew = true, true
		} else if err.Error() == "нечего менять" {
			stored = true // уже есть за 30 дней: скрипт тоже не добавлял и не сообщал
		} else {
			log.Printf("tilda: CRM Лиды: %v", err)
		}
	}
	crmNew, crmOK := false, false
	if t.Docs != nil {
		n, err := t.upsertCRM(ctx, l, via)
		if err != nil {
			log.Printf("tilda: bs_crm: %v", err)
		} else {
			crmOK, crmNew = true, n
		}
	}
	if !stored && !crmOK {
		return nil, fmt.Errorf("заявка не сохранилась")
	}
	// Как addLead скрипта: новая заявка (нет такой за 30 дней в «CRM Лиды»
	// или нет такой карточки в CRM платформы) сообщается команде один раз.
	notify := sheetNew || crmNew
	if notify && t.Notify != nil {
		go t.Notify(context.WithoutCancel(ctx), p)
	}
	out := gin.H{"ok": true}
	if !sheetNew && !crmNew {
		out["duplicate"] = true
	}
	return out, nil
}

// upsertCRM: лид в CRM платформы; тот же телефон или Telegram дополняет карточку.
func (t *TildaLeads) upsertCRM(ctx context.Context, l tildaLead, via string) (isNew bool, err error) {
	f := &LeadFunnel{docs: t.Docs, now: t.now}
	now := t.now()
	digits := ""
	if l.Phone != "" {
		digits = phoneDigits(l.Phone)
	}
	note := l.Comment
	if l.Email != "" {
		note = strings.TrimSpace(note + "\nEmail: " + l.Email)
	}
	logText := "Заявка с сайта"
	if l.Form != "" {
		logText += ": " + l.Form
	}
	if u := l.utmLine(); u != "" {
		logText += " (" + u + ")"
	}
	if via == "script" {
		logText += " · через таблицу"
	}
	err = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		isNew = false
		leads, _ := crm["leads"].([]any)
		if del, _ := crm["deleted"].([]any); len(del) > 0 {
			for _, x := range del {
				s := fmt.Sprint(x)
				if (digits != "" && s == "ph:"+digits) || (l.Tg != "" && strings.EqualFold(s, "@"+strings.TrimPrefix(l.Tg, "@"))) {
					return false
				}
			}
		}
		var lead map[string]any
		for _, x := range leads {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			ph, _ := m["phone"].(string)
			tg, _ := m["tg"].(string)
			if (digits != "" && phoneDigits(ph) == digits) ||
				(l.Tg != "" && tg != "" && strings.EqualFold(strings.TrimPrefix(tg, "@"), strings.TrimPrefix(l.Tg, "@"))) {
				lead = m
				break
			}
		}
		if lead == nil {
			isNew = true
			id := "site" + strings.Map(func(r rune) rune {
				if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					return r
				}
				return -1
			}, l.TranID)
			if id == "site" {
				b := make([]byte, 5)
				_, _ = rand.Read(b)
				id += hex.EncodeToString(b)
			}
			name := l.Name
			if name == "" {
				name = l.Phone
			}
			lead = map[string]any{
				"id": id, "col": "new", "name": name, "phone": l.Phone, "tg": l.Tg, "source": l.Source,
				"niche": l.Niche, "note": note, "sum": "", "date": now.In(almaty).Format("02.01.2006"),
				"created": now.UTC().Format(time.RFC3339), "funnel": "site",
			}
			leads = append([]any{lead}, leads...)
		} else {
			for k, v := range map[string]string{"name": l.Name, "phone": l.Phone, "tg": l.Tg, "niche": l.Niche} {
				if s, _ := lead[k].(string); strings.TrimSpace(s) == "" && v != "" {
					lead[k] = v
				}
			}
			if note != "" {
				old, _ := lead["note"].(string)
				if !strings.Contains(old, note) {
					lead["note"] = strings.TrimSpace(old + "\n" + note)
				}
			}
		}
		if l.Email != "" {
			lead["email"] = l.Email
		}
		if l.Form != "" {
			lead["form"] = l.Form
		}
		if len(l.UTM) > 0 {
			u := map[string]any{}
			for k, v := range l.UTM {
				u[k] = v
			}
			lead["utm"] = u
		}
		if l.Revenue != "" {
			lead["revenue"] = l.Revenue
		}
		lead["siteAt"] = now.UTC().Format(time.RFC3339)
		addLog(lead, now, logText)
		crm["leads"] = leads
		return true
	})
	return isNew, err
}

// ── Настройки → «Заявки с сайта» (команда) ──

func tildaBase(c *gin.Context) string {
	if b := publicBase(); b != "" {
		return b
	}
	proto := "https"
	if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
		proto = strings.TrimSpace(strings.Split(p, ",")[0])
	} else if c.Request.TLS == nil && strings.HasPrefix(c.Request.Host, "localhost") {
		proto = "http"
	}
	return proto + "://" + c.Request.Host
}

// Status: GET /api/v1/platform/tilda
func (t *TildaLeads) Status(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx := c.Request.Context()
	tok, env := t.Token(ctx)
	if tok == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Ключ не выдан: нет связи с базой"})
		return
	}
	base := tildaBase(c)
	out := gin.H{
		"url": base + "/api/v1/public/tilda/" + tok, "base": base + "/api/v1/public/tilda",
		"token": tok, "fromEnv": env, "apiKeyName": "token",
	}
	if t.Meta != nil {
		if v, err := t.Meta.GetMeta(ctx, tildaMetaLast); err == nil && v != "" {
			var x tildaLast
			if json.Unmarshal([]byte(v), &x) == nil {
				out["last"] = x
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

type TildaModule struct {
	t      *TildaLeads
	secret []byte
}

func NewTildaModule(t *TildaLeads, secret []byte) *TildaModule {
	return &TildaModule{t: t, secret: secret}
}

func (m *TildaModule) Register(r *gin.Engine) {
	r.POST("/api/v1/public/tilda", m.t.Hook)
	r.POST("/api/v1/public/tilda/:token", m.t.Hook)
	g := r.Group("/api/v1/platform/tilda")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("", m.t.Status)
}
