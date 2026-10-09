package http

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/gcal"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

// R71: «Подключить Google Календарь» in «Календарь работы» (platform and
// Mini App). The sync itself is internal/gcal/user.go; here are the button's
// endpoints, the consent's return (the owner's /api/v1/gcal/callback, state
// "u.…") and what the work calendar's GET adds: the status and the person's
// Google events.

var gcalUsersP atomic.Pointer[gcal.UserSync]

// SetGcalUsers wires the sync (app/calendar_wiring.go).
func SetGcalUsers(u *gcal.UserSync) { gcalUsersP.Store(u) }

func gcalUsers() *gcal.UserSync { return gcalUsersP.Load() }

// gcalTouch: the work calendar of scope was edited: push it in a few seconds.
func gcalTouch(scope string) {
	if u := gcalUsers(); u != nil && strings.HasPrefix(scope, "user:") {
		u.Touch(scope, 4*time.Second)
	}
}

// gcalCalExtra: added to the work calendar's GET. owner: the viewer is the
// calendar's person (sees titles and the button).
func gcalCalExtra(ctx context.Context, scope string, owner bool) gin.H {
	u := gcalUsers()
	if u == nil {
		return nil
	}
	st, evs := u.View(ctx, scope, owner)
	if st.Connected {
		u.Opened(ctx, scope)
	}
	if evs == nil {
		evs = []gcal.ShownEvent{}
	}
	return gin.H{"gcal": gin.H{"status": st, "owner": owner}, "gev": evs}
}

// GcalUsersModule: the endpoints.
type GcalUsersModule struct {
	G      *AppGateway
	secret []byte
}

func NewGcalUsersModule(g *AppGateway, secret []byte) *GcalUsersModule {
	return &GcalUsersModule{G: g, secret: secret}
}

func (m *GcalUsersModule) Register(r *gin.Engine) {
	p := r.Group("/api/v1/platform/gcal")
	p.Use(middleware.AuthJWT(m.secret))
	p.Use(middleware.RequireRole("admin", "moderator", "resident"))
	p.GET("", m.platform(m.status))
	p.POST("/connect", m.platform(m.connect))
	p.POST("/settings", m.platform(m.settings))
	p.POST("/disconnect", m.platform(m.disconnect))
	p.POST("/sync", m.platform(m.syncNow))

	r.GET("/api/v1/app/gcal", m.app(m.status))
	r.POST("/api/v1/app/gcal/connect", m.app(m.connect))
	r.POST("/api/v1/app/gcal/settings", m.app(m.settings))
	r.POST("/api/v1/app/gcal/disconnect", m.app(m.disconnect))
	r.POST("/api/v1/app/gcal/sync", m.app(m.syncNow))
}

type gcalWho struct {
	scope  string
	origin string // "p" platform, "a" app
}

// platform: the person's own calendar (a resident's, or a team member's own).
func (m *GcalUsersModule) platform(f func(*gin.Context, gcalWho)) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := platformUser(c)
		if u == "" {
			forbidden(c, "no_user")
			return
		}
		f(c, gcalWho{scope: "user:" + u, origin: "p"})
	}
}

// app: the Mini App's person (initData); the admin's preview of a resident
// may not connect someone else's Google.
func (m *GcalUsersModule) app(f func(*gin.Context, gcalWho)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if m.G == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
			return
		}
		if strings.TrimSpace(c.Query("name")) != "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "preview", "detail": "Google Календарь подключает сам резидент"})
			return
		}
		_, tg, ok := m.G.syncWho(c)
		if !ok {
			return
		}
		f(c, gcalWho{scope: fmt.Sprintf("user:tg:%d", tg), origin: "a"})
	}
}

func gcalNoSync(c *gin.Context) bool {
	if gcalUsers() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_configured"})
		return true
	}
	return false
}

func (m *GcalUsersModule) status(c *gin.Context, w gcalWho) {
	if gcalNoSync(c) {
		return
	}
	u := gcalUsers()
	ctx := c.Request.Context()
	st, _ := u.View(ctx, w.scope, true)
	out := gin.H{"status": st}
	if st.Connected && c.Query("calendars") == "1" {
		if conn, _ := u.Store.GcalConn(ctx, w.scope); conn != nil {
			cals, err := u.Calendars(ctx, conn)
			if err != nil {
				out["calendarsError"] = err.Error()
			} else {
				out["calendars"] = cals
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

func (m *GcalUsersModule) connect(c *gin.Context, w gcalWho) {
	if gcalNoSync(c) {
		return
	}
	u, err := gcalUsers().ConsentURL(c.Request.Context(), w.scope, w.origin, GcalRedirect())
	if errors.Is(err, gcal.ErrNoClient) {
		c.JSON(http.StatusConflict, gin.H{"error": "no_client", "detail": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": u})
}

func (m *GcalUsersModule) settings(c *gin.Context, w gcalWho) {
	if gcalNoSync(c) {
		return
	}
	var req struct {
		Read  string `json:"read"`
		Write string `json:"write"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_body"})
		return
	}
	conn, err := gcalUsers().Settings(c.Request.Context(), w.scope, req.Read, req.Write)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "settings", "detail": err.Error()})
		return
	}
	_ = conn
	gcalTouch(w.scope)
	st, _ := gcalUsers().View(c.Request.Context(), w.scope, true)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": st})
}

func (m *GcalUsersModule) disconnect(c *gin.Context, w gcalWho) {
	if gcalNoSync(c) {
		return
	}
	if err := gcalUsers().Disconnect(c.Request.Context(), w.scope); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "disconnect", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (m *GcalUsersModule) syncNow(c *gin.Context, w gcalWho) {
	if gcalNoSync(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	res, err := gcalUsers().SyncOne(ctx, w.scope)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "sync", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}

// gcalUserCallback: Google returns a person here (the owner's redirect URI).
func gcalUserCallback(c *gin.Context) {
	u := gcalUsers()
	if u == nil {
		gcalPage(c, http.StatusServiceUnavailable, `<h1>Недоступно</h1><p>Попробуйте позже.</p>`)
		return
	}
	ctx := c.Request.Context()
	scope, origin, err := u.ReadState(c.Query("state"))
	back := `<p>Вернитесь в Telegram: календарь в приложении BS.</p>`
	if origin == "p" {
		back = `<a class="b" href="` + html.EscapeString(ContentPlatformURL()+"#mycal") + `">Вернуться в календарь</a>`
	}
	if err != nil {
		gcalPage(c, http.StatusForbidden, `<h1>Ссылка устарела</h1><p class="er">`+html.EscapeString(err.Error())+`</p>`+back)
		return
	}
	if e := c.Query("error"); e != "" {
		msg := "Google ответил: " + e
		if e == "access_denied" {
			msg = "Доступ не выдан. Если Google написал «Доступ заблокирован» или «приложение не прошло проверку», напишите команде BS: приложению нужна публикация."
		}
		gcalPage(c, http.StatusOK, `<h1>Не подключено</h1><p class="er">`+html.EscapeString(msg)+`</p>`+back)
		return
	}
	if err := u.Connect(ctx, scope, c.Query("code"), GcalRedirect()); err != nil {
		log.Printf("gcal user %s: connect: %v", scope, err)
		gcalPage(c, http.StatusOK, `<h1>Не получилось</h1><p class="er">`+html.EscapeString(err.Error())+`</p>`+back)
		return
	}
	go func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := u.SyncOne(sctx, scope); err != nil {
			log.Printf("gcal user %s: first sync: %v", scope, err)
		}
	}()
	conn, _ := u.Store.GcalConn(ctx, scope)
	where := gcal.UserCalName
	if conn != nil && conn.WriteName != "" {
		where = conn.WriteName
	}
	gcalPage(c, http.StatusOK, `<h1 class="ok">✅ Google Календарь подключён</h1>`+
		`<p>Отметки «Календаря работы» появятся в календаре «`+html.EscapeString(where)+`» без напоминаний и писем, а ваши события Google покажутся на платформе. Обновление каждые 5 минут и при открытии календаря.</p>`+back)
}
