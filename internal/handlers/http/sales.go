package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R51: продажи клуба на сервере. Четыре вещи, одобренные владельцем:
//
//  1. Сценарий после экспресс-разбора (sales_razbor.go): итоги разбора PDF,
//     личное предложение клуба под диагнозы лида, напоминания на 2, 5 и 10
//     день; кнопки «Хочу в клуб», «Есть вопрос», «Не сейчас».
//  2. Кейсы «было → стало» из замеров резидентов с их согласия
//     (sales_cases.go): черновик → утверждён → опубликован.
//  3. Итог периода резидента PDF (3 и 12 месяцев) и продление в 1 клик
//     (sales_report.go): платёж через Kaspi, пакет продлевается сам, когда
//     команда отметит оплату.
//  4. Еженедельный отчёт владельцу (sales_weekly.go): проверка системы и
//     цифры недели, одним сообщением, только то, что требует действия.
//
// Все сообщения людям уходят только 10:00-20:00 по Алматы (salesWindow);
// то, что пришлось на ночь, ждёт утра. Настройки и тексты правит команда
// (Настройки → «Продажи клуба»), документ server/sales_settings.

const (
	salesSettingsKey = "sales_settings" // scope server
	salesFrom        = 10               // proactive messages 10:00-20:00 Almaty
	salesUntil       = 20
	salesTick        = 5 * time.Minute
)

// salesWindow: a proactive message may go now.
func salesWindow(t time.Time) bool {
	h := t.In(almaty).Hour()
	return h >= salesFrom && h < salesUntil
}

// salesNextWindow: now, or the next 10:00 Almaty.
func salesNextWindow(t time.Time) time.Time {
	a := t.In(almaty)
	if a.Hour() < salesFrom {
		return time.Date(a.Year(), a.Month(), a.Day(), salesFrom, 0, 0, 0, almaty)
	}
	if a.Hour() >= salesUntil {
		n := a.AddDate(0, 0, 1)
		return time.Date(n.Year(), n.Month(), n.Day(), salesFrom, 0, 0, 0, almaty)
	}
	return t
}

// ── настройки ──

// SalesCfg: prices, the Kaspi link, the bonus, switches and the texts.
type SalesCfg struct {
	YearPrice int64             `json:"yearPrice"`
	Q3Price   int64             `json:"q3Price"` // 0: no 3-month option
	KaspiClub string            `json:"kaspiClub"`
	BonusText string            `json:"bonusText"` // "" = no bonus
	BonusDays int               `json:"bonusDays"` // the bonus lasts N days after the offer
	SeqOff    bool              `json:"seqOff"`
	ReportOff bool              `json:"reportOff"`
	WeeklyOff bool              `json:"weeklyOff"`
	Texts     map[string]string `json:"texts"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
	UpdatedBy string            `json:"updatedBy,omitempty"`
	// KaspiSeeded (R55): when the server put the owner's Kaspi link into an
	// empty KaspiClub (once: a link the team clears later stays cleared).
	KaspiSeeded string `json:"kaspiSeeded,omitempty"`
	// RefBonus (R75): the referral bonus for each new resident, ₸ (0: RefBonus).
	RefBonus int64 `json:"refBonus"`
}

const (
	salesYearPrice = 1500000
	salesQ3Price   = 500000
)

// Placeholders the texts may use; the server fills them.
var salesVars = []string{"{имя}", "{диагнозы}", "{что_даст_клуб}", "{шаг}", "{инструмент}", "{кейс}", "{цены}", "{бонус}", "{дата}", "{период}", "{до}"}

// salesTextNames: the texts the team edits, in order.
var salesTextNames = []struct{ Key, Name, Hint string }{
	{"offer", "Предложение клуба (сразу с PDF)", "{имя}, {диагнозы}, {что_даст_клуб}, {цены}, {бонус}"},
	{"d2", "День 2: как первые шаги", "{имя}, {шаг}, {инструмент}"},
	{"d5", "День 5: кейс похожего бизнеса", "{имя}, {кейс}"},
	{"d10", "День 10: последнее напоминание", "{имя}, {цены}, {бонус}"},
	{"later", "Отложено: напоминание в выбранный день", "{имя}, {цены}"},
	{"report", "Итог периода: подпись к PDF", "{имя}, {период}, {цены}"},
	{"renew14", "Продление: за 14 дней", "{имя}, {до}, {цены}"},
	{"renew3", "Продление: за 3 дня", "{имя}, {до}, {цены}"},
	{"consent", "Запрос согласия на кейс", "{имя}"},
}

var salesDefaultTexts = map[string]string{
	"offer": "{имя}, спасибо за разбор! Итоги в PDF выше: диагнозы, первые шаги и цифры.\n\n" +
		"Что даст клуб именно вашему бизнесу:\n{что_даст_клуб}\n\n" +
		"Как это устроено: разбор каждые 10 дней с Рустамом и Береке, план задач на цикл, ежедневные отчёты, группа из 5 собственников, ваша доска на платформе.\n\n" +
		"{цены}{бонус}",
	"d2":      "{имя}, как первые шаги после разбора? Напомню первый: {шаг}.\n\n{инструмент}Если что-то застопорилось, напишите: подскажем.",
	"d5":      "{имя}, вот как это было у похожего бизнеса:\n\n{кейс}\n\nВ клубе такие изменения собираются за 1-3 цикла по 10 дней.",
	"d10":     "{имя}, последнее напоминание о клубе. Диагнозы с разбора никуда не делись, а с планом и трекерами их закрывают быстрее.\n\n{цены}{бонус}",
	"later":   "{имя}, вы просили напомнить о клубе Business Surgery. Предложение в силе:\n\n{цены}",
	"report":  "{имя}, ваш итог: {период}. В PDF: что изменилось в цифрах, закрытые задачи, дисциплина и план на следующий период.\n\n{цены}",
	"renew14": "{имя}, ваш пакет в клубе действует до {до}. Чтобы цикл разборов не прервался, продлите заранее:\n\n{цены}",
	"renew3":  "{имя}, через 3 дня, {до}, заканчивается ваш пакет в клубе. Продлить можно в 1 клик:\n\n{цены}",
	"consent": "{имя}, ваши результаты (цифры было → стало) могут попасть в кейсы клуба анонимно: только ниша, город и цифры. Хотите с именем или не показывать совсем? Решение можно поменять в профиле в любой момент.",
}

func defaultSalesCfg() SalesCfg {
	return SalesCfg{YearPrice: salesYearPrice, Q3Price: salesQ3Price, RefBonus: RefBonus, Texts: map[string]string{}}
}

func (c *SalesCfg) norm() {
	if c.YearPrice <= 0 {
		c.YearPrice = salesYearPrice
	}
	if c.Q3Price < 0 {
		c.Q3Price = 0
	}
	if c.RefBonus <= 0 {
		c.RefBonus = RefBonus
	}
	if c.BonusDays < 0 {
		c.BonusDays = 0
	}
	if c.Texts == nil {
		c.Texts = map[string]string{}
	}
	c.KaspiClub = strings.TrimSpace(c.KaspiClub)
	c.BonusText = strings.TrimSpace(noLongDash(c.BonusText))
}

// Text: the team's text, or the default.
func (c SalesCfg) Text(key string) string {
	if t := strings.TrimSpace(c.Texts[key]); t != "" {
		return t
	}
	return salesDefaultTexts[key]
}

func noLongDash(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "—", "-"), "–", "-")
}

// fill puts the values into a text; empty lines left by empty values go.
func salesFill(t string, vals map[string]string) string {
	for k, v := range vals {
		t = strings.ReplaceAll(t, k, v)
	}
	for _, k := range salesVars {
		t = strings.ReplaceAll(t, k, "")
	}
	for strings.Contains(t, "\n\n\n") {
		t = strings.ReplaceAll(t, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(noLongDash(t))
}

// ── ClubSales ──

// SalesClub: the club data the sales flows read (pg.ClubRepo).
type SalesClub interface {
	LoadResidents(ctx context.Context) ([]club.Resident, error)
	Load(ctx context.Context) (*club.Snapshot, error)
	ReportDays(ctx context.Context, from, to time.Time) ([]pg.ReportDay, error)
}

// SalesBoards: residents' boards and Telegram ids (pg.PlatformRepo).
type SalesBoards interface {
	LiveBoards(ctx context.Context) ([]pg.PlatformBoard, error)
	ResidentByTg(ctx context.Context, tgID int64) (string, bool, error)
	ResidentTgByName(ctx context.Context, name string) (int64, string, error)
}

// ClubSales runs the four R51 flows.
type ClubSales struct {
	docs funnelDocs
	// F sends to leads through the bot (the lead's dialog keeps it); nil: Send.
	F *LeadFunnel
	// Send: a message from the bot (the team, residents); Doc: a file.
	Send func(ctx context.Context, chat int64, text string, kb map[string]any) error
	Doc  func(ctx context.Context, chat int64, key, name string, data []byte, fileID, caption string, kb map[string]any) error
	// Resident: a personal message to a resident (WhatsApp for those who chose it); nil: Send.
	Resident func(ctx context.Context, kind, key, name string, tg int64, text string, kb map[string]any) error
	// WA: a lead without Telegram (the WhatsApp queue).
	WA func(ctx context.Context, m bot.WAMessage) error
	// WARoute: the WhatsApp phone of a resident who chose WhatsApp ("" for Telegram).
	WARoute func(ctx context.Context, name string) string
	Admins  []int64
	Owner   int64
	Club    SalesClub
	Boards  SalesBoards
	// Write: a club write on the server (setMeetings after a renewal).
	Write func(ctx context.Context, action string, params map[string]string) error
	// AI: an optional model for a case's story (template without it).
	AI func(ctx context.Context, system, user string) (string, error)
	// Check: the system check (syscheck.go) for the weekly report.
	Check func(ctx context.Context) CheckResult
	// Meta keeps the weekly marks and counters (bot meta).
	Meta interface {
		GetMeta(ctx context.Context, key string) (string, error)
		SetMeta(ctx context.Context, key, value string) error
	}
	// AIUsed: requests per provider today (ai.Client.ProviderStates).
	AIUsed func() map[string]int
	// Errors counts the server's error lines (sales_weekly.go).
	Errors *ErrCounter
	// OnCases: the published cases changed (the public /about).
	OnCases func(list []PublicCaseView)
	Secret  []byte
	Now     func() time.Time

	mu           sync.Mutex
	running      bool
	salesMemMeta *salesMemMeta
}

func NewClubSales(docs funnelDocs, secret []byte) *ClubSales {
	return &ClubSales{docs: docs, Secret: secret, Now: time.Now}
}

func (s *ClubSales) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Settings: the saved settings with the defaults.
func (s *ClubSales) Settings(ctx context.Context) SalesCfg {
	c := defaultSalesCfg()
	if d, err := s.docs.GetDoc(ctx, "server", salesSettingsKey); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &c)
	}
	c.norm()
	return c
}

// mutate reads a doc, lets fn change it and writes it back (retrying on conflict).
func (s *ClubSales) mutate(ctx context.Context, scope, key string, fn func(doc map[string]any) bool) error {
	for try := 0; try < 6; try++ {
		doc := map[string]any{}
		base := 0
		if d, err := s.docs.GetDoc(ctx, scope, key); err == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), &doc)
			}
			if doc == nil {
				doc = map[string]any{}
			}
		} else if err != nil {
			return err
		}
		if !fn(doc) {
			return nil
		}
		val, _ := json.Marshal(doc)
		if _, err := s.docs.PutDoc(ctx, scope, key, base, string(val), false, "server:sales"); err == nil {
			return nil
		}
		time.Sleep(time.Duration(20*(try+1)) * time.Millisecond)
	}
	return pg.ErrPlatformConflict
}

func (s *ClubSales) read(ctx context.Context, scope, key string) map[string]any {
	doc := map[string]any{}
	if d, err := s.docs.GetDoc(ctx, scope, key); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &doc)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc
}

// sendLead: to a lead through the funnel (kept in the lead's dialog).
func (s *ClubSales) sendLead(ctx context.Context, tg int64, text string, kb map[string]any) error {
	if s.F != nil && s.F.send != nil {
		return s.F.send(ctx, tg, text, kb)
	}
	if s.Send != nil {
		return s.Send(ctx, tg, text, kb)
	}
	return fmt.Errorf("нет бота")
}

func (s *ClubSales) docLead(ctx context.Context, tg int64, key, name string, data []byte, caption string, kb map[string]any) error {
	if s.F != nil && s.F.Doc != nil {
		return s.F.Doc(ctx, tg, key, name, data, "", caption, kb)
	}
	if s.Doc != nil {
		return s.Doc(ctx, tg, key, name, data, "", caption, kb)
	}
	return fmt.Errorf("нет бота")
}

func (s *ClubSales) toResident(ctx context.Context, kind, key, name string, tg int64, text string, kb map[string]any) error {
	if s.Resident != nil {
		return s.Resident(ctx, kind, key, name, tg, text, kb)
	}
	if tg == 0 || s.Send == nil {
		return fmt.Errorf("нет Telegram")
	}
	return s.Send(ctx, tg, text, kb)
}

// team: a note to everyone in the team.
func (s *ClubSales) team(ctx context.Context, text string, kb map[string]any) {
	if s.Send == nil {
		return
	}
	for _, a := range s.Admins {
		if err := s.Send(ctx, a, text, kb); err != nil {
			log.Printf("sales: team %d: %v", a, err)
		}
	}
}

// sign: the signature of an open link (a PDF for WhatsApp).
func (s *ClubSales) sign(kind, id string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte("sales|" + kind + "|" + id))
	return hex.EncodeToString(m.Sum(nil))[:24]
}

func (s *ClubSales) openURL(kind, id string) string {
	base := publicBase()
	if base == "" {
		base = "https://app.bxclub.kz"
	}
	return base + "/api/v1/public/sales/" + kind + "/" + id + "/" + s.sign(kind, id) + ".pdf"
}

// pricesLine: «Резидентство на 12 месяцев: 1 500 000 ₸ ...».
func pricesLine(c SalesCfg) string {
	var b strings.Builder
	b.WriteString("Резидентство на 12 месяцев: " + tenge(c.YearPrice))
	if c.Q3Price > 0 {
		b.WriteString("\nНа 3 месяца: " + tenge(c.Q3Price))
	}
	return b.String()
}

func bonusLine(c SalesCfg, offerAt time.Time) string {
	if c.BonusText == "" {
		return ""
	}
	t := "\n\n🎁 " + c.BonusText
	if c.BonusDays > 0 && !offerAt.IsZero() {
		t += " (до " + offerAt.AddDate(0, 0, c.BonusDays).In(almaty).Format("02.01") + ")"
	}
	return t
}

func ruDate(t time.Time) string {
	a := t.In(almaty)
	return fmt.Sprintf("%d %s", a.Day(), ruMonths[a.Month()-1])
}

// ── маршруты ──

type SalesModule struct {
	S      *ClubSales
	G      *AppGateway
	Secret []byte
	// FV (R55): the funnel's video library (funnel_video.go); nil: none.
	FV *FunnelVideos
	// BL (R57): «Ссылка для клиента» (board_link.go); nil: none.
	BL *BoardLinks
}

func (m *SalesModule) Register(r *gin.Engine) {
	s := m.S
	if m.BL != nil {
		m.BL.Register(r)
	}
	if m.FV != nil {
		r.GET("/api/v1/public/fv/:name", m.FV.PublicFile)
		r.HEAD("/api/v1/public/fv/:name", m.FV.PublicFile)
	}
	r.GET("/api/v1/public/sales/:kind/:id/:sig", s.PublicPDF)
	r.HEAD("/api/v1/public/sales/:kind/:id/:sig", s.PublicPDF)
	if m.G != nil {
		r.GET("/api/v1/app/sales/me", func(c *gin.Context) { s.AppMe(c, m.G) })
		r.POST("/api/v1/app/sales/consent", func(c *gin.Context) { s.AppConsent(c, m.G) })
		r.POST("/api/v1/app/sales/renew", func(c *gin.Context) { s.AppRenew(c, m.G) })
		r.POST("/api/v1/app/sales/report/send", func(c *gin.Context) { s.AppReportSend(c, m.G) })
	}
	g := r.Group("/api/v1/platform/sales")
	g.Use(middleware.AuthJWT(m.Secret))
	g.Use(middleware.RequireRole("admin", "moderator", "resident"))
	// the resident's own (the team with ?name=)
	g.GET("/me", s.Me)
	g.PUT("/me/consent", s.PutMyConsent)
	g.POST("/me/renew", s.MyRenew)
	g.GET("/me/reports/:id/pdf", s.MyReportPDF)
	// the team
	t := g.Group("")
	t.Use(func(c *gin.Context) {
		if !teamOnly(c) {
			c.Abort()
			return
		}
		c.Next()
	})
	t.GET("/settings", s.GetSettings)
	t.PUT("/settings", s.PutSettings)
	if m.FV != nil { // R55: «Видео в воронке»
		t.GET("/funnel/videos", m.FV.List)
		t.POST("/funnel/videos", m.FV.Upload)
		t.PUT("/funnel/videos/:id", m.FV.Put)
		t.DELETE("/funnel/videos/:id", m.FV.Delete)
	}
	if s.F != nil {
		t.GET("/funnel/stats", s.F.StatsHTTP)
	}
	t.GET("/razbor/:lead", s.GetRazbor)
	t.PUT("/razbor/:lead", s.PutRazbor)
	t.GET("/razbor/:lead/pdf", s.RazborPDF)
	t.POST("/razbor/:lead/stop", s.StopRazbor)
	t.GET("/seq", s.SeqList)
	t.GET("/cases", s.Cases)
	t.POST("/cases/generate", s.GenerateCases)
	t.PUT("/cases/:id", s.PutCase)
	t.POST("/cases/:id/ai", s.CaseAI)
	t.DELETE("/cases/:id", s.DeleteCase)
	t.GET("/consent", s.ConsentList)
	t.PUT("/consent", s.PutConsent)
	t.POST("/consent/request", s.RequestConsent)
	t.GET("/reports", s.Reports)
	t.POST("/reports", s.NewReport)
	t.GET("/reports/:id/pdf", s.ReportPDF)
	t.PUT("/reports/:id", s.PutReport)
	t.POST("/reports/:id/send", s.SendReport)
	t.DELETE("/reports/:id", s.DeleteReport)
	t.GET("/renewals", s.Renewals)
	t.POST("/renewals/paid", s.RenewPaidByTeam)
	t.POST("/weekly", s.WeeklyNow)
}

func (s *ClubSales) GetSettings(c *gin.Context) {
	cfg := s.Settings(c.Request.Context())
	names := []gin.H{}
	for _, t := range salesTextNames {
		names = append(names, gin.H{"key": t.Key, "name": t.Name, "hint": t.Hint, "default": salesDefaultTexts[t.Key]})
	}
	c.JSON(http.StatusOK, gin.H{"settings": cfg, "texts": names})
}

func (s *ClubSales) PutSettings(c *gin.Context) {
	var in SalesCfg
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	in.norm()
	if in.KaspiClub != "" && !strings.HasPrefix(in.KaspiClub, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_link", "message": "Ссылка Kaspi должна начинаться с https://"})
		return
	}
	// R55: the owner pays everything through one Kaspi link (the fines' one
	// too), so it is accepted here as well.
	texts := map[string]string{}
	for _, t := range salesTextNames {
		v := strings.TrimSpace(noLongDash(in.Texts[t.Key]))
		if v != "" && v != salesDefaultTexts[t.Key] {
			if len([]rune(v)) > 3000 {
				v = string([]rune(v)[:3000])
			}
			texts[t.Key] = v
		}
	}
	in.Texts = texts
	in.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	in.UpdatedBy = platformUser(c)
	ctx := c.Request.Context()
	err := s.mutate(ctx, "server", salesSettingsKey, func(doc map[string]any) bool {
		if v, _ := doc["kaspiSeeded"].(string); v != "" {
			in.KaspiSeeded = v
		}
		b, _ := json.Marshal(in)
		for k := range doc {
			delete(doc, k)
		}
		_ = json.Unmarshal(b, &doc)
		return true
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	s.GetSettings(c)
}

// ── фон ──

// SeedKaspi (R55): the owner's Kaspi link (bot.KaspiLink, an open-amount
// link: the payer types the sum) goes into an empty «Ссылка на оплату
// клуба» once. A link the team set is never touched; one the team cleared
// after the seed stays cleared.
func (s *ClubSales) SeedKaspi(ctx context.Context) (bool, error) {
	seeded := false
	err := s.mutate(ctx, "server", salesSettingsKey, func(doc map[string]any) bool {
		seeded = false
		if k, _ := doc["kaspiClub"].(string); strings.TrimSpace(k) != "" {
			return false
		}
		if v, _ := doc["kaspiSeeded"].(string); v != "" {
			return false
		}
		if len(doc) == 0 {
			b, _ := json.Marshal(defaultSalesCfg())
			_ = json.Unmarshal(b, &doc)
		}
		doc["kaspiClub"] = bot.KaspiLink
		doc["kaspiSeeded"] = s.now().UTC().Format(time.RFC3339)
		seeded = true
		return true
	})
	if seeded {
		log.Printf("sales: Kaspi link of the club set to the owner's link (it was empty)")
	}
	return seeded, err
}

// Loop runs the flows every few minutes.
func (s *ClubSales) Loop(ctx context.Context) {
	if _, err := s.SeedKaspi(ctx); err != nil {
		log.Printf("sales: seed Kaspi: %v", err)
	}
	t := time.NewTicker(salesTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		s.Tick(c)
		cancel()
	}
}

// Tick: one pass of every flow (tests call it with a fixed clock).
func (s *ClubSales) Tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	safe := func(name string, f func()) {
		defer func() {
			if e := recover(); e != nil {
				log.Printf("sales: %s: panic %v", name, e)
			}
		}()
		f()
	}
	safe("razbor", func() { s.RazborTick(ctx) })
	safe("consent", func() { s.ConsentTick(ctx) })
	safe("reports", func() { s.ReportTick(ctx) })
	safe("weekly", func() { s.WeeklyTick(ctx) })
	if s.Errors != nil {
		safe("errors", func() { s.Errors.Flush(ctx, s.meta(), s.now()) })
	}
	safe("ai", func() { s.sampleAI(ctx) })
}

// ── мелочи ──

func sStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(m[k]))
}

func sMap(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

func sTime(m map[string]any, k string) time.Time {
	t, err := time.Parse(time.RFC3339, sStr(m, k))
	if err != nil {
		return time.Time{}
	}
	return t
}

func sStrs(v any) []string {
	out := []string{}
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if m[ks[i]] != m[ks[j]] {
			return m[ks[i]] > m[ks[j]]
		}
		return ks[i] < ks[j]
	})
	return ks
}

// ServerWrite: R51: a club write the server makes itself (the meetings after
// a renewal), the same way as the team's write from the platform.
func (w *ClubWrites) ServerWrite(ctx context.Context, owner int64, who, action string, p map[string]string) error {
	if !club.SheetLegacy() {
		_, err := w.Local(ctx, "server", owner, who, action, p)
		return err
	}
	q := make(map[string][]string, len(p))
	for k, v := range p {
		q[k] = []string{v}
	}
	u := &platformTgUser{ID: owner, FirstName: who}
	body := w.Do(ctx, "server", u, action, w.gw.params(q, action, u), true)
	if e := answerError(body); e != "" {
		return fmt.Errorf("%s", e)
	}
	return nil
}
