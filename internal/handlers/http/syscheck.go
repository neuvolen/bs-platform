package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/buildinfo"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R36: «Проверка системы»: the owner sees the server's state himself,
// without Railway: the bot's /status (admins), the button in the platform
// settings, and one message after a deploy when something changed or
// something is failing.
//
//	ИИ Claude        a tiny live call: the model that answered, the key's source
//	Заявки Tilda     the last real lead, its path (Tilda → server, or via the sheet's script), today's count
//	Голос            built-in recordings or ElevenLabs, how many tour phrases are ready
//	Webhook Telegram Telegram calls the server
//	Экспорт в таблицу on / off
//	Версия           the commit (or build time)
//	База данных      answers
//
// After a deploy (on start, 2 minutes later) the server runs the check and
// compares it with the last result kept in bot_meta (syscheck:last). Only a
// change or a failure is sent, at most once per deploy (syscheck:sent =
// the commit), never between 22:00 and 09:00 Almaty: then at 09:00.

const (
	metaSysLast = "syscheck:last" // {key: sig} of the last check
	metaSysSent = "syscheck:sent" // the deploy whose message went out
)

// CheckItem: one line of the check.
type CheckItem struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	State string `json:"state"` // ok | warn | fail | off
	Text  string `json:"text"`
	// Sig: the stable part compared with the last check ("" = not compared:
	// the version changes with every deploy, the count of leads every day).
	Sig string `json:"-"`
	// Note: the sentence the owner gets when this line changed.
	Note string `json:"-"`
}

// CheckResult: the whole check.
type CheckResult struct {
	At      time.Time   `json:"at"`
	Version string      `json:"version"`
	Items   []CheckItem `json:"items"`
}

func (r CheckResult) Failing() []CheckItem {
	var out []CheckItem
	for _, it := range r.Items {
		if it.State == "fail" {
			out = append(out, it)
		}
	}
	return out
}

// Text: the check as the bot sends it.
func (r CheckResult) Text() string {
	var b strings.Builder
	b.WriteString("🩺 Проверка системы\n")
	for _, it := range r.Items {
		b.WriteString("\n" + stateMark(it.State) + " " + it.Title + ": " + it.Text)
	}
	b.WriteString("\n\nПроверено " + r.At.In(club.Almaty).Format("02.01 15:04"))
	return b.String()
}

func stateMark(s string) string {
	switch s {
	case "ok":
		return "✅"
	case "warn":
		return "⚠️"
	case "fail":
		return "❌"
	}
	return "⚪"
}

// SysCheck runs the check. Every source is optional: a missing one is «нет данных».
type SysCheck struct {
	AI      *ai.Client
	Premium *PremiumVoice
	Meta    interface {
		GetMeta(ctx context.Context, key string) (string, error)
		SetMeta(ctx context.Context, key, value string) error
	}
	DB func(ctx context.Context) error
	// Webhook: whether Telegram calls the server; nil when there is no bot.
	Webhook func(ctx context.Context) (owned bool, url string)
	Export  func(ctx context.Context) bool
	// Builtin: the tour phrases with a recording built into the app.
	Builtin func() (ready, total int)
	Build   func() buildinfo.Info
	Send    func(ctx context.Context, chatID int64, text string) error
	Owner   int64
	Now     func() time.Time
	// NoQuiet: send at any hour (SYSCHECK_QUIET=off, test stands).
	NoQuiet bool

	mu      sync.Mutex
	running bool
}

func (s *SysCheck) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SysCheck) build() buildinfo.Info {
	if s.Build != nil {
		return s.Build()
	}
	return buildinfo.Get()
}

// Run: every line, the slow ones at once (Claude, Telegram, the database).
func (s *SysCheck) Run(ctx context.Context) CheckResult {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r := CheckResult{At: s.now(), Version: versionText(s.build())}
	items := make([]CheckItem, 7)
	var wg sync.WaitGroup
	par := func(i int, f func() CheckItem) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if e := recover(); e != nil {
					items[i] = CheckItem{Key: fmt.Sprint(i), Title: "Проверка", State: "warn", Text: "не удалась"}
				}
			}()
			items[i] = f()
		}()
	}
	par(0, func() CheckItem { return s.claude(ctx) })
	par(1, func() CheckItem { return s.tilda(ctx) })
	par(2, func() CheckItem { return s.voice(ctx) })
	par(3, func() CheckItem { return s.webhook(ctx) })
	par(4, func() CheckItem { return s.export(ctx) })
	par(5, func() CheckItem {
		return CheckItem{Key: "version", Title: "Версия", State: "ok", Text: r.Version}
	})
	par(6, func() CheckItem { return s.db(ctx) })
	wg.Wait()
	r.Items = items
	return r
}

func versionText(b buildinfo.Info) string {
	var parts []string
	if b.Commit != "" {
		parts = append(parts, b.Short())
	}
	if !b.Built.IsZero() {
		parts = append(parts, "сборка "+b.Built.In(club.Almaty).Format("02.01.2006 15:04"))
	}
	if len(parts) == 0 {
		return "неизвестна"
	}
	return strings.Join(parts, ", ")
}

func keySourceText(src string) string {
	switch src {
	case "env":
		return "ключ из Railway"
	case "settings":
		return "ключ из настроек платформы"
	}
	return "ключа нет"
}

// keySourceFull: the source with the variable's name (R37), never the value.
func keySourceFull(c *ai.Client) string {
	src := c.KeySource()
	if src == "env" && c.Env != nil && c.Env.Name != "" {
		t := "ключ из Railway (" + c.Env.Name + ")"
		if c.Env.Cleaned {
			t += ", убраны лишние кавычки или пробелы"
		}
		return t
	}
	return keySourceText(src)
}

// RailwayDeployHint: a variable is in the process only after a deploy.
const RailwayDeployHint = "Railway: после добавления переменной нажмите Deploy (изменения применяются только после деплоя)"

// noKeyText: why there is no key: the related names, the saved key's
// problem, where to put it (R37). Names only, never values.
func noKeyText(c *ai.Client) string {
	parts := []string{"ключа нет"}
	if c != nil && c.Keys != nil && c.Keys.Problem() != "" {
		parts = append(parts, c.Keys.Problem())
	}
	if c != nil && c.Env != nil && len(c.Env.Related) > 0 {
		var ns []string
		for _, n := range c.Env.Related {
			if why := c.Env.Unusable[n]; why != "" {
				ns = append(ns, "«"+n+"» ("+why+")")
			} else {
				ns = append(ns, "«"+n+"»")
			}
		}
		parts = append(parts, "в Railway есть похожие переменные: "+strings.Join(ns, ", "))
	} else {
		svc := strings.TrimSpace(os.Getenv("RAILWAY_SERVICE_NAME"))
		w := "в переменных сервера нет ни ANTHROPIC_API_KEY, ни CLAUDE_API_KEY, ни значения sk-ant-…"
		if svc != "" {
			w += " (сервис «" + svc + "»: переменная должна быть именно в нём)"
		}
		parts = append(parts, w)
	}
	parts = append(parts, RailwayDeployHint, "или вставьте ключ: Настройки платформы → «Ключ Claude»")
	return strings.Join(parts, "; ")
}

func (s *SysCheck) claude(ctx context.Context) CheckItem {
	it := CheckItem{Key: "claude", Title: "ИИ Claude"}
	if s.AI == nil || !s.AI.HasClaude() {
		it.State, it.Text, it.Sig = "fail", noKeyText(s.AI), "nokey"
		it.Note = "❌ ИИ Claude не подключён: нет ключа (Настройки платформы → «Ключ Claude» или ANTHROPIC_API_KEY в Railway и Deploy)"
		return it
	}
	src := keySourceFull(s.AI)
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	model, err := s.AI.Ping(c, "")
	if err != nil {
		msg := ai.UserMessage(err)
		sig := "err"
		if ai.IsQuota(err) {
			msg, sig = ai.QuotaMessage, "quota"
		}
		if d := ai.HTTPDetail(err); d != "" {
			msg += "; " + d
		}
		if h := s.AI.ClaudeKeyHint(); h != "" {
			msg += ". Причина: " + h
		}
		it.State, it.Text, it.Sig = "fail", "ошибка: "+msg+" ("+src+")", sig
		it.Note = "❌ ИИ Claude не отвечает: " + msg
		return it
	}
	it.State, it.Text, it.Sig = "ok", "отвечает, модель "+model+", "+src, "ok"
	it.Note = "✅ ИИ Claude подключён и отвечает (" + model + ")"
	return it
}

func (s *SysCheck) tilda(ctx context.Context) CheckItem {
	it := CheckItem{Key: "tilda", Title: "Заявки Tilda"}
	if s.Meta == nil {
		it.State, it.Text = "off", "нет данных"
		return it
	}
	in, err := ReadTildaLeads(ctx, s.Meta, s.now())
	if err != nil {
		it.State, it.Text = "warn", "не прочиталось"
		return it
	}
	today := fmt.Sprintf("сегодня %d", in.Today)
	// R36c: the newest attempt, when it says more than the last lead: Tilda
	// knocks but is refused (a wrong key), only checks the connection, or
	// never called at all.
	try := ""
	rejected := false
	if in.Last != nil && (in.At.IsZero() || in.Last.Result != "accepted") {
		when := in.LastAt.In(club.Almaty)
		f := "02.01 15:04"
		if when.Format("2006-01-02") == s.now().In(club.Almaty).Format("2006-01-02") {
			f = "15:04"
		}
		try = " · последняя попытка " + when.Format(f) + ", " + tildaTryWord(*in.Last) + " (" + ruTries(in.Day) + " за сутки)"
		rejected = in.Last.Result == "rejected"
	}
	rejNote := func() {
		if rejected {
			it.State = "warn"
			it.Sig += "|rej"
			it.Note = "⚠️ Tilda обращается к серверу, но заявка отклонена: " + in.Last.Reason
		}
	}
	if in.At.IsZero() {
		it.State, it.Text, it.Sig = "off", "заявок ещё не было · "+today, "none"
		if in.Last == nil {
			it.Text += " · обращений с сайта не было"
		}
		it.Text += try
		rejNote()
		return it
	}
	when := in.At.In(club.Almaty).Format("02.01 15:04")
	if in.Via == "script" {
		it.State, it.Sig = "warn", "script"
		it.Text = "последняя " + when + ", через скрипт таблицы · " + today + try
		it.Note = "⚠️ Заявки с Tilda идут через скрипт таблицы: поставьте в Tilda вебхук сервера (Настройки → «Заявки с сайта»)"
		rejNote()
		return it
	}
	it.State, it.Sig = "ok", "tilda"
	it.Text = "последняя " + when + ", напрямую с сайта · " + today + try
	it.Note = "✅ Заявки с Tilda приходят напрямую на сервер"
	rejNote()
	return it
}

func (s *SysCheck) voice(ctx context.Context) CheckItem {
	it := CheckItem{Key: "voice", Title: "Голос"}
	bReady, bTotal := 0, 0
	if s.Builtin != nil {
		bReady, bTotal = s.Builtin()
	}
	builtin := func() {
		it.Text = fmt.Sprintf("встроенные записи, готово %d из %d фраз", bReady, bTotal)
		it.State = "ok"
		if bReady < bTotal {
			it.State = "warn"
		}
	}
	if s.Premium == nil {
		builtin()
		it.Sig = "builtin"
		return it
	}
	st := s.Premium.State(ctx)
	switch {
	case st.On:
		it.State = "ok"
		it.Text = fmt.Sprintf("ElevenLabs «%s», готово %d из %d фраз", st.Voice, st.Ready, st.Total)
		it.Sig = "el|" + st.Voice
		it.Note = fmt.Sprintf("✅ Голос тура: ElevenLabs «%s», готово %d из %d фраз", st.Voice, st.Ready, st.Total)
		if st.Ready < st.Total {
			it.State = "warn"
		}
	default:
		builtin()
		it.Sig = "builtin"
		it.Note = "✅ Голос тура: встроенные записи"
	}
	if st.Picked != "" {
		// R36c: the voice being made, in the owner's words: «ElevenLabs «X»: готово N из 33»
		if st.Running {
			it.Text = fmt.Sprintf("ElevenLabs «%s»: озвучивается, готово %d из %d фраз (пока играют %s)", st.Picked, st.Ready, st.Total, playing(st))
		} else if st.Error != "" {
			it.State = "warn"
			it.Text = fmt.Sprintf("ElevenLabs «%s»: остановлено на %d из %d фраз, %s (играют %s)", st.Picked, st.Ready, st.Total, voiceStop(st), playing(st))
			it.Sig += "|stopped"
			it.Note = fmt.Sprintf("⚠️ Озвучка голосом «%s» остановлена: %s", st.Picked, st.Error)
		} else {
			it.Text = fmt.Sprintf("ElevenLabs «%s»: готово %d из %d фраз, озвучка ждёт запуска (играют %s)", st.Picked, st.Ready, st.Total, playing(st))
		}
	} else if st.Error != "" && st.On {
		it.State = "warn"
		it.Text += " · " + voiceStop(st)
	}
	if st.EnvError != "" {
		it.State = "warn"
		if strings.Contains(st.EnvError, "не принят") {
			if h := ai.ElevenKeyHint(elevenEnvKey()); h != "" {
				st.EnvError += ". Причина: " + h
			}
		}
		it.Text += " · ElevenLabs не подключён: " + st.EnvError
		it.Sig += "|env"
		it.Note = "⚠️ Голос ElevenLabs: " + st.EnvError
	}
	return it
}

// playing: what the tour plays while another voice is made.
func playing(st PremiumState) string {
	if st.On {
		return "«" + st.Voice + "»"
	}
	return "встроенные записи"
}

// voiceStop: the reason a reading stopped, short.
func voiceStop(st PremiumState) string {
	switch st.Stopped {
	case "quota":
		return "лимит символов ElevenLabs исчерпан"
	case "key":
		return "ключ не принят"
	case "voice":
		return "голос не найден"
	}
	return st.Error
}

func (s *SysCheck) webhook(ctx context.Context) CheckItem {
	it := CheckItem{Key: "webhook", Title: "Webhook Telegram"}
	if s.Webhook == nil {
		it.State, it.Text, it.Sig = "off", "бот не настроен", "nobot"
		return it
	}
	owned, _ := s.Webhook(ctx)
	if owned {
		it.State, it.Text, it.Sig = "ok", "у сервера", "server"
		it.Note = "✅ Бот Telegram работает через сервер"
		return it
	}
	if club.SheetLegacy() {
		it.State, it.Text, it.Sig = "warn", "не у сервера (аварийный режим таблицы)", "legacy"
		return it
	}
	it.State, it.Text, it.Sig = "fail", "Telegram шлёт сообщения не серверу (сервер вернёт его сам в течение 15 минут)", "other"
	it.Note = "❌ Webhook Telegram не у сервера"
	return it
}

func (s *SysCheck) export(ctx context.Context) CheckItem {
	it := CheckItem{Key: "export", Title: "Экспорт в таблицу", State: "ok"}
	on := club.ExportOn(ctx)
	if s.Export != nil {
		on = s.Export(ctx)
	}
	if on {
		it.Text, it.Sig, it.Note = "включён (копия раз в час)", "on", "ℹ️ Экспорт в таблицу включён"
	} else {
		it.State, it.Text, it.Sig, it.Note = "off", "выключен", "off", "ℹ️ Экспорт в таблицу выключен"
	}
	return it
}

func (s *SysCheck) db(ctx context.Context) CheckItem {
	it := CheckItem{Key: "db", Title: "База данных"}
	if s.DB == nil {
		it.State, it.Text = "off", "нет данных"
		return it
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := s.DB(c); err != nil {
		it.State, it.Text, it.Sig, it.Note = "fail", "не отвечает", "fail", "❌ База данных не отвечает"
		return it
	}
	it.State, it.Text, it.Sig = "ok", fmt.Sprintf("отвечает (%d мс)", time.Since(t0).Milliseconds()), "ok"
	it.Note = "✅ База данных отвечает"
	return it
}

// ── After a deploy ──

// QuietUntil: the time a message may go: now, or 09:00 Almaty when now is
// between 22:00 and 09:00.
func QuietUntil(now time.Time) time.Time {
	a := now.In(club.Almaty)
	nine := time.Date(a.Year(), a.Month(), a.Day(), 9, 0, 0, 0, club.Almaty)
	switch {
	case a.Hour() >= 22:
		return nine.AddDate(0, 0, 1)
	case a.Before(nine):
		return nine
	}
	return now
}

func sigs(r CheckResult) map[string]string {
	m := map[string]string{}
	for _, it := range r.Items {
		if it.Sig != "" {
			m[it.Key] = it.Sig
		}
	}
	return m
}

// DeployNote: the message after this deploy, "" when nothing changed and
// nothing fails. prev is the last kept {key: sig} (nil: the first check).
func DeployNote(r CheckResult, prev map[string]string) string {
	var lines []string
	seen := map[string]bool{}
	for _, it := range r.Items {
		if it.Sig == "" || prev[it.Key] == it.Sig {
			continue
		}
		seen[it.Key] = true
		n := it.Note
		if n == "" {
			n = stateMark(it.State) + " " + it.Title + ": " + it.Text
		}
		lines = append(lines, n)
	}
	for _, it := range r.Failing() {
		if !seen[it.Key] {
			lines = append(lines, "❌ "+it.Title+": "+it.Text)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "🩺 Сервер обновлён (версия " + r.Version + ")\n\n" + strings.Join(lines, "\n") + "\n\nПолная проверка: /status"
}

// deployID: one deploy (the commit; else the build time).
func (s *SysCheck) deployID() string {
	if id := s.build().ID(); id != "" {
		return id
	}
	return "start:" + s.now().UTC().Format(time.RFC3339)
}

// AfterDeploy: the check «delay» after start, the message if there is one.
// Between 22:00 and 09:00 Almaty it waits for 09:00 and checks then.
func (s *SysCheck) AfterDeploy(ctx context.Context, delay time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	if at := QuietUntil(s.now()); !s.NoQuiet && at.After(s.now()) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(at.Sub(s.now())):
		}
	}
	if sent, err := s.NotifyDeploy(ctx); err != nil {
		log.Printf("syscheck: after deploy: %v", err)
	} else if sent != "" {
		log.Printf("syscheck: the owner got the deploy check")
	}
}

// NotifyDeploy runs the check, sends what changed or fails (once per
// deploy) and keeps the result. It returns the text sent ("" when none).
func (s *SysCheck) NotifyDeploy(ctx context.Context) (string, error) {
	if s.Meta == nil {
		return "", fmt.Errorf("no meta store")
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return "", nil
	}
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()

	id := s.deployID()
	if v, err := s.Meta.GetMeta(ctx, metaSysSent); err != nil {
		return "", err
	} else if v == id {
		return "", nil // this deploy already had its message
	}
	r := s.Run(ctx)
	var prev map[string]string
	if v, _ := s.Meta.GetMeta(ctx, metaSysLast); v != "" {
		_ = json.Unmarshal([]byte(v), &prev)
	}
	keep := func() {
		b, _ := json.Marshal(sigs(r))
		if err := s.Meta.SetMeta(ctx, metaSysLast, string(b)); err != nil {
			log.Printf("syscheck: keep: %v", err)
		}
	}
	text := DeployNote(r, prev)
	if text == "" {
		keep()
		return "", nil
	}
	if s.Send == nil || s.Owner == 0 {
		keep()
		return "", fmt.Errorf("no bot or owner to tell")
	}
	if err := s.Send(ctx, s.Owner, text); err != nil {
		return "", fmt.Errorf("send: %w", err) // the next deploy tries again: nothing kept
	}
	keep()
	_ = s.Meta.SetMeta(ctx, metaSysSent, id)
	return text, nil
}

// ── HTTP: Настройки → «Проверка системы» (admin) ──

func (s *SysCheck) Check(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	r := s.Run(c.Request.Context())
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"at": r.At.UTC().Format(time.RFC3339), "version": r.Version, "items": r.Items, "text": r.Text()})
}

type SysCheckModule struct {
	s      *SysCheck
	secret []byte
}

func NewSysCheckModule(s *SysCheck, secret []byte) *SysCheckModule {
	return &SysCheckModule{s: s, secret: secret}
}

func (m *SysCheckModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/platform/system")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.POST("/check", m.s.Check)
	g.GET("/check", m.s.Check)
}
