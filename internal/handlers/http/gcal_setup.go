package http

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"html"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/gcal"
	"github.com/gin-gonic/gin"
)

// R67: «Google Календарь перестал окрашивать в зелёный и ставить галочку».
// The server keeps the calendar itself (internal/gcal). The owner allows it
// once: the bot's /calendar gives a private link (valid 3 days) to this page;
// it takes the OAuth client (two fields, once) and sends him to Google's
// «Разрешить». The callback keeps the token and paints the last two weeks.

// GcalSetup serves the setup page and the OAuth callback.
type GcalSetup struct {
	Sync *gcal.Sync
	// Tell: a message to the team (the catch-up's result).
	Tell func(ctx context.Context, text string)
	now  func() time.Time
}

func NewGcalSetup(s *gcal.Sync) *GcalSetup { return &GcalSetup{Sync: s, now: time.Now} }

const gcalKeyTTL = 72 * time.Hour

// GcalRedirect: the address Google returns to; the same one goes into the
// OAuth client's «Authorized redirect URIs».
func GcalRedirect() string {
	if u := strings.TrimSpace(os.Getenv("GCAL_REDIRECT_URL")); u != "" {
		return u
	}
	return "https://app.bxclub.kz/api/v1/gcal/callback"
}

// SetupLink makes a fresh private link to the setup page.
func (h *GcalSetup) SetupLink(ctx context.Context) (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	k := hex.EncodeToString(b)
	if err := h.Sync.C.Store.SetMeta(ctx, gcal.MetaSetupKey, k+"|"+strconv.FormatInt(h.now().Add(gcalKeyTTL).Unix(), 10)); err != nil {
		return "", err
	}
	return strings.TrimSuffix(GcalRedirect(), "/callback") + "/setup?k=" + k, nil
}

func (h *GcalSetup) keyOK(ctx context.Context, k string) bool {
	v, _ := h.Sync.C.Store.GetMeta(ctx, gcal.MetaSetupKey)
	key, exp, ok := strings.Cut(v, "|")
	if !ok || key == "" || k == "" {
		return false
	}
	until, _ := strconv.ParseInt(exp, 10, 64)
	return subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 && h.now().Unix() < until
}

// Status: one line for /calendar.
func (h *GcalSetup) Status(ctx context.Context) string {
	c := h.Sync.C
	if !c.Connected(ctx) {
		return "не подключён"
	}
	st := "подключён"
	if e, _ := c.Store.GetMeta(ctx, gcal.MetaError); e != "" {
		st += ", последняя ошибка: " + e
	}
	return st
}

func (h *GcalSetup) Register(r *gin.Engine) {
	r.GET("/api/v1/gcal/setup", h.page)
	r.POST("/api/v1/gcal/setup", h.save)
	r.GET("/api/v1/gcal/callback", h.callback)
}

const gcalCSS = `<style>body{margin:0;background:#0b0b0c;color:#ececec;font:16px/1.55 -apple-system,'Segoe UI',Manrope,sans-serif}
.w{max-width:620px;margin:0 auto;padding:28px 16px 60px}h1{font-size:22px;margin:0 0 6px}p{margin:8px 0;color:#c9c9c9}
.c{background:#151517;border:1px solid #26262a;border-radius:14px;padding:18px;margin:16px 0}ol{padding-left:20px;margin:8px 0}li{margin:6px 0}
code{background:#222;padding:2px 6px;border-radius:6px;word-break:break-all;color:#fff}label{display:block;font-size:13px;color:#9a9a9a;margin:12px 0 4px}
input{width:100%;box-sizing:border-box;background:#0f0f10;border:1px solid #333;border-radius:10px;color:#fff;padding:12px;font:inherit}
.b{display:block;width:100%;margin-top:16px;padding:14px;border:0;border-radius:12px;background:#2e7d32;color:#fff;font:700 16px inherit;text-align:center;text-decoration:none;cursor:pointer}
.ok{color:#81c784}.er{color:#e57373}.s{font-size:13px;color:#8a8a8a}</style>`

func gcalPage(c *gin.Context, status int, body string) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Data(status, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Google Календарь BS</title>`+
		gcalCSS+`</head><body><div class="w">`+body+`</div></body></html>`))
}

func (h *GcalSetup) page(c *gin.Context) {
	ctx := c.Request.Context()
	k := c.Query("k")
	if !h.keyOK(ctx, k) {
		gcalPage(c, http.StatusForbidden, `<h1>Ссылка устарела</h1><p>Напишите боту <b>/calendar</b>, он пришлёт новую ссылку.</p>`)
		return
	}
	cl := h.Sync.C
	if cr, ok := cl.Creds(ctx); ok && c.Query("edit") != "1" {
		u, err := cl.ConsentURL(ctx, GcalRedirect(), k)
		if err != nil {
			gcalPage(c, http.StatusInternalServerError, `<p class="er">`+html.EscapeString(err.Error())+`</p>`)
			return
		}
		state := `<p>Сейчас: <b>` + html.EscapeString(h.Status(ctx)) + `</b></p>`
		gcalPage(c, http.StatusOK, `<h1>Google Календарь</h1>`+state+`<div class="c"><p>Нажмите кнопку, войдите в Google аккаунт, где календарь «Business Surgery Meetings», и нажмите «Разрешить».</p>`+
			`<p class="s">Если Google напишет «Google hasn’t verified this app»: нажмите «Advanced» (Дополнительно), затем «Go to … (unsafe)» (Перейти). Это ваше собственное приложение.</p>`+
			`<a class="b" href="`+html.EscapeString(u)+`">Подключить Google Календарь</a></div>`+
			`<p class="s">Client ID: `+html.EscapeString(cr.ID[:min(12, len(cr.ID))])+`… <a style="color:#9ab" href="?k=`+html.EscapeString(k)+`&edit=1">заменить</a></p>`)
		return
	}
	gcalPage(c, http.StatusOK, `<h1>Google Календарь: один раз</h1>
<p>Платформа будет сама красить прошедшие встречи в зелёный, ставить ✅ и не присылать уведомления. Для этого Google нужен ключ вашего аккаунта (Client ID и Client Secret).</p>
<div class="c"><ol>
<li>Откройте <a style="color:#9cf" href="https://console.cloud.google.com/apis/library/calendar-json.googleapis.com" target="_blank" rel="noopener">Google Calendar API</a> под тем Google аккаунтом, где календарь «Business Surgery Meetings»; вверху выберите любой проект (или «New project» → «Create»), нажмите <b>Enable</b>.</li>
<li>Откройте <a style="color:#9cf" href="https://console.cloud.google.com/auth/overview" target="_blank" rel="noopener">Google Auth Platform</a> → <b>Get started</b>: App name «BS Platform», support email ваш, <b>Next</b>; Audience: <b>External</b>, Next; Contact: ваш email, Next; галочка согласия, <b>Create</b>.</li>
<li>Слева <b>Audience</b> → кнопка <b>Publish app</b> → <b>Confirm</b> (иначе Google отключает доступ через 7 дней).</li>
<li>Слева <b>Clients</b> → <b>Create client</b>: Application type <b>Web application</b>, Name «BS Platform». В «Authorized redirect URIs» нажмите <b>Add URI</b> и вставьте:<br><code>`+html.EscapeString(GcalRedirect())+`</code><br>Нажмите <b>Create</b>.</li>
<li>Скопируйте <b>Client ID</b> и <b>Client secret</b> из окна в поля ниже.</li>
</ol></div>
<form method="post" action="/api/v1/gcal/setup"><input type="hidden" name="k" value="`+html.EscapeString(k)+`">
<label>Client ID</label><input name="id" autocomplete="off" placeholder="1234…apps.googleusercontent.com" required>
<label>Client secret</label><input name="secret" autocomplete="off" placeholder="GOCSPX-…" required>
<button class="b" type="submit">Сохранить и перейти к Google</button></form>`)
}

func (h *GcalSetup) save(c *gin.Context) {
	ctx := c.Request.Context()
	k := c.PostForm("k")
	if !h.keyOK(ctx, k) {
		gcalPage(c, http.StatusForbidden, `<h1>Ссылка устарела</h1><p>Напишите боту <b>/calendar</b>.</p>`)
		return
	}
	if err := h.Sync.C.SaveCreds(ctx, gcal.Creds{ID: c.PostForm("id"), Secret: c.PostForm("secret")}); err != nil {
		gcalPage(c, http.StatusBadRequest, `<p class="er">`+html.EscapeString(err.Error())+`</p><p><a style="color:#9cf" href="/api/v1/gcal/setup?k=`+html.EscapeString(k)+`&edit=1">Назад</a></p>`)
		return
	}
	u, err := h.Sync.C.ConsentURL(ctx, GcalRedirect(), k)
	if err != nil {
		gcalPage(c, http.StatusInternalServerError, `<p class="er">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	c.Redirect(http.StatusSeeOther, u)
}

func (h *GcalSetup) callback(c *gin.Context) {
	ctx := c.Request.Context()
	k := c.Query("state")
	if gcal.IsUserState(k) { // R71: a person's own calendar (gcal_users.go)
		gcalUserCallback(c)
		return
	}
	if !h.keyOK(ctx, k) {
		gcalPage(c, http.StatusForbidden, `<h1>Ссылка устарела</h1><p>Напишите боту <b>/calendar</b>, он пришлёт новую.</p>`)
		return
	}
	if e := c.Query("error"); e != "" {
		gcalPage(c, http.StatusOK, `<h1>Доступ не выдан</h1><p class="er">Google ответил: `+html.EscapeString(e)+`</p><p><a style="color:#9cf" href="/api/v1/gcal/setup?k=`+html.EscapeString(k)+`">Попробовать ещё раз</a></p>`)
		return
	}
	if err := h.Sync.C.Exchange(ctx, c.Query("code"), GcalRedirect()); err != nil {
		log.Printf("gcal: consent: %v", err)
		gcalPage(c, http.StatusOK, `<h1>Не получилось</h1><p class="er">`+html.EscapeString(err.Error())+`</p><p><a style="color:#9cf" href="/api/v1/gcal/setup?k=`+html.EscapeString(k)+`">Попробовать ещё раз</a></p>`)
		return
	}
	_ = h.Sync.C.Store.SetMeta(ctx, gcal.MetaSetupKey, "") // the link is used up
	_ = h.Sync.C.Store.SetMeta(ctx, gcal.MetaBackfill, "")
	log.Printf("gcal: connected")
	go h.catchup()
	gcalPage(c, http.StatusOK, `<h1 class="ok">✅ Календарь подключён</h1><p>Платформа красит прошедшие встречи за 14 дней и убирает уведомления у будущих. Итог придёт в Telegram через минуту.</p><p>Эту страницу можно закрыть.</p>`)
}

func (h *GcalSetup) catchup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	msg, err := h.Sync.Catchup(ctx)
	if err != nil {
		log.Printf("gcal: catch-up: %v", err)
		msg = "⚠️ Google Календарь подключён, но обработать встречи не вышло: " + err.Error()
	}
	if msg != "" && h.Tell != nil {
		h.Tell(ctx, msg)
	}
}

// AtStart: the catch-up, if the calendar is connected and it has not run.
func (h *GcalSetup) AtStart() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	connected := h.Sync.C.Connected(ctx)
	cancel()
	if !connected {
		log.Printf("gcal: not connected (the owner sends /calendar to the bot)")
		return
	}
	h.catchup()
}
