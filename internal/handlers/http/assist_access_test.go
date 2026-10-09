package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

const asSecret = "assist-test-secret-0123456789"

type asEnv struct {
	r    *gin.Engine
	db   *pg.DB
	repo *pg.PlatformRepo
	auth *PlatformAuthHandler
	acc  *AssistAccess
}

func newAsEnv(t *testing.T) *asEnv {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	if err := pg.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`TRUNCATE platform_assistants, platform_login_links, platform_sessions, platform_session_cutoff, platform_assist_log`,
		`DELETE FROM platform_residents WHERE tg_id IN (72001, 72002, 72003)`,
		`INSERT INTO platform_residents (tg_id, name, active) VALUES (72001, 'Альфа Резидентова', true), (72003, 'Бета Резидентова', true)`,
		`DELETE FROM club_residents WHERE name IN ('Елена Безтелеграмова', 'Бывшая Резидентка')`,
		`INSERT INTO club_residents (name, tg_id) VALUES ('Елена Безтелеграмова', NULL)`,
		`INSERT INTO club_residents (name, tg_id, former) VALUES ('Бывшая Резидентка', NULL, true)`,
		`DELETE FROM platform_boards WHERE id IN ('asb1', 'asb2')`,
		`DELETE FROM platform_docs WHERE scope LIKE 'user:tg:7200%'`,
	} {
		if _, err := db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	repo := pg.NewPlatformRepo(db)
	// the club's seed with money of the resident: an assistant must not see it
	seed := `{"LIVE":{"source":"server"},"RESIDENTS":[{"name":"Альфа Резидентова","format":"Онлайн","start":"01.09.2026","debt":250000,"paid":100000}],` +
		`"FINES":[{"res":"Альфа Резидентова","sum":5000}],"SDATA":{"schedule":[{"res":"Альфа Резидентова","date":"12.10.2026"}],"visits":[]}}`
	if err := repo.PutServerDoc(ctx, platformSeedKey, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PutBoard(ctx, "asb1", 0, json.RawMessage(`{"id":"asb1","name":"Разбор Альфы","info":{"res":"Альфа Резидентова"},"nodes":[]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformHandler(repo)
	a := NewPlatformAuthHandler(testBotToken, "72999:Команда", asSecret)
	a.repo, a.names = repo, h.names
	pm := &PlatformModule{h: h, auth: a, secret: []byte(asSecret), AI: NewPlatformAI(repo, nil)}
	pm.Access = NewAssistAccess(repo, a, h.names)
	a.access = pm.Access
	middleware.SessionGuard = pm.Access.Guard
	t.Cleanup(func() { middleware.SessionGuard = nil })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	pm.Register(r)
	return &asEnv{r: r, db: db, repo: repo, auth: a, acc: pm.Access}
}

func (e *asEnv) call(method, path, tok string, body any, ip ...string) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit Version/17.0 Mobile Safari/604.1")
	req.Host = "app.bxclub.kz"
	if len(ip) > 0 {
		req.RemoteAddr = ip[0] + ":5000"
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func asJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not json (%d): %s", w.Code, w.Body.String())
	}
	return m
}

func (e *asEnv) login(t *testing.T, tg int64, first string) map[string]any {
	t.Helper()
	w := e.call("POST", "/api/v1/platform/auth/telegram", "", map[string]any{"widget": widgetFor(tg, first, "")})
	if w.Code != 200 {
		t.Fatalf("login %d: %d %s", tg, w.Code, w.Body.String())
	}
	return asJSON(t, w)
}

func tokenOf(m map[string]any) string { s, _ := m["token"].(string); return s }

func lastPath(u string) string { return u[strings.LastIndex(u, "/")+1:] }

func TestAssistAccessFlow(t *testing.T) {
	e := newAsEnv(t)
	ctx := context.Background()

	// 1. resident logs in with Telegram: the token has a session, listed as this device
	res := e.login(t, 72001, "Альфа")
	rt := tokenOf(res)
	w := e.call("GET", "/api/v1/platform/sessions", rt, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"current":true`) || !strings.Contains(w.Body.String(), "iPhone · Safari") {
		t.Fatalf("sessions: %d %s", w.Code, w.Body.String())
	}

	// 2. invite: one-time link, 72 hours, stored only as a hash
	w = e.call("POST", "/api/v1/platform/assist/invite", rt, map[string]any{"preset": "tasks", "label": "Айгерим"})
	if w.Code != 200 {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	inv := asJSON(t, w)
	url, _ := inv["url"].(string)
	if !strings.HasPrefix(url, "https://app.bxclub.kz/assist/") {
		t.Fatalf("invite url %q", url)
	}
	tok := lastPath(url)
	var n int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_assistants WHERE invite_hash = $1`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("the raw invite token is stored")
	}
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_assistants WHERE invite_hash = $1 AND invite_expires BETWEEN now() + interval '71 hours' AND now() + interval '73 hours'`, hashSecretToken(tok)).Scan(&n)
	if n != 1 {
		t.Fatal("invite not stored as a 72 h hash")
	}
	if w := e.call("GET", "/assist/"+tok, "", nil); w.Code != 200 || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invite page: %d %v", w.Code, w.Header())
	}
	if w := e.call("GET", "/api/v1/platform/assist/invite-info?t="+tok, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Альфа") || !strings.Contains(w.Body.String(), "Задачи и отчёты") {
		t.Fatalf("invite info: %d %s", w.Code, w.Body.String())
	}

	// 3. the resident cannot accept their own invite
	if w := e.call("POST", "/api/v1/platform/assist/accept", "", map[string]any{"token": tok, "widget": widgetFor(72001, "Альфа", "")}); w.Code != http.StatusConflict {
		t.Fatalf("own invite: %d %s", w.Code, w.Body.String())
	}

	// 4. the assistant accepts with their own Telegram: works for the resident
	w = e.call("POST", "/api/v1/platform/assist/accept", "", map[string]any{"token": tok, "widget": widgetFor(72002, "Айгерим", "")})
	if w.Code != 200 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	acc := asJSON(t, w)
	at := tokenOf(acc)
	user, _ := acc["user"].(map[string]any)
	asst, _ := user["assistant"].(map[string]any)
	if user["id"] != "tg:72001" || user["role"] != "resident" || asst["preset"] != "tasks" || asst["resident"] != "Альфа Резидентова" || asst["name"] != "Айгерим" {
		t.Fatalf("assistant user: %v", user)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "bs_session=") {
		t.Fatal("no session cookie")
	}
	// one-time
	if w := e.call("POST", "/api/v1/platform/assist/accept", "", map[string]any{"token": tok, "widget": widgetFor(72002, "Айгерим", "")}); w.Code != http.StatusGone {
		t.Fatalf("second accept: %d %s", w.Code, w.Body.String())
	}

	// 5. the assistant sees the resident's workspace without money
	w = e.call("GET", "/api/v1/platform/sync", at, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Разбор Альфы") {
		t.Fatalf("assistant sync: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "250000") || strings.Contains(w.Body.String(), `\"sum\":5000`) || !strings.Contains(w.Body.String(), "12.10.2026") {
		t.Fatalf("assistant seed must keep meetings and drop money: %s", w.Body.String())
	}
	if w := e.call("GET", "/api/v1/platform/sync", rt, nil); !strings.Contains(w.Body.String(), "250000") {
		t.Fatalf("the resident keeps their money line: %s", w.Body.String())
	}

	// 6. «Задачи и отчёты»: the board and the calendar yes, other sections and access management no
	if w := e.call("PUT", "/api/v1/platform/docs/bs_mycal", at, map[string]any{"version": 0, "value": `{"b":[]}`}); w.Code != 200 {
		t.Fatalf("assistant calendar: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("PUT", "/api/v1/platform/docs/bs_kanban", at, map[string]any{"version": 0, "value": `[]`}); w.Code != 403 || !strings.Contains(w.Body.String(), "assistant_preset") {
		t.Fatalf("tasks preset must refuse other sections: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("PUT", "/api/v1/platform/docs/bs_theme", at, map[string]any{"version": 0, "value": `light`}); w.Code != 403 {
		t.Fatalf("assistant must not change the resident's device settings: %d", w.Code)
	}
	b, _ := e.repo.GetBoard(ctx, "asb1")
	w = e.call("PUT", "/api/v1/platform/boards/asb1", at, map[string]any{"version": b.Version, "data": json.RawMessage(`{"id":"asb1","name":"Разбор Альфы","info":{"res":"Альфа Резидентова"},"nodes":[{"id":1,"type":"task","title":"Позвонить"}]}`)})
	if w.Code != 200 {
		t.Fatalf("assistant board edit: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("POST", "/api/v1/platform/assist/invite", at, map[string]any{}); w.Code != 403 {
		t.Fatalf("assistant must not invite: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sessions", at, nil); w.Code != 403 {
		t.Fatalf("assistant must not see the resident's devices: %d", w.Code)
	}

	// 7. the journal: who did what (a board saved many times is one line with a count)
	b, _ = e.repo.GetBoard(ctx, "asb1")
	_ = e.call("PUT", "/api/v1/platform/boards/asb1", at, map[string]any{"version": b.Version, "data": json.RawMessage(`{"id":"asb1","name":"Разбор Альфы","info":{"res":"Альфа Резидентова"},"nodes":[{"id":1,"type":"task","title":"Позвонить","done":true}]}`)})
	w = e.call("GET", "/api/v1/platform/assist", rt, nil)
	st := asJSON(t, w)
	if st["mode"] != "resident" {
		t.Fatalf("resident state: %s", w.Body.String())
	}
	logs, _ := st["log"].([]any)
	found := false
	for _, l := range logs {
		m := l.(map[string]any)
		if m["action"] == "Изменения в разборе" && m["detail"] == "Разбор Альфы" && m["name"] == "Айгерим" && m["n"] == float64(2) {
			found = true
		}
	}
	if !found || !strings.Contains(w.Body.String(), "Календарь работы") || !strings.Contains(w.Body.String(), "Вход на платформу") {
		t.Fatalf("journal: %s", w.Body.String())
	}

	// 8. «Только просмотр» applies at once
	aid := int64(asst["id"].(float64))
	if w := e.call("POST", "/api/v1/platform/assist/preset", rt, map[string]any{"id": aid, "preset": "view"}); w.Code != 200 {
		t.Fatalf("preset: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("PUT", "/api/v1/platform/docs/bs_mycal", at, map[string]any{"version": 1, "value": `{"b":[1]}`}); w.Code != 403 {
		t.Fatalf("view preset must refuse writes: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("GET", "/api/v1/platform/sync", at, nil); w.Code != 200 {
		t.Fatalf("view preset reads: %d", w.Code)
	}
	// another resident cannot touch this assistant
	r2 := tokenOf(e.login(t, 72003, "Бета"))
	if w := e.call("POST", "/api/v1/platform/assist/revoke", r2, map[string]any{"id": aid}); w.Code != 404 {
		t.Fatalf("foreign revoke: %d", w.Code)
	}

	// 9. one assistant, two residents: the switcher
	w = e.call("POST", "/api/v1/platform/assist/invite", r2, map[string]any{"preset": "nofin"})
	tok2 := lastPath(asJSON(t, w)["url"].(string))
	w = e.call("POST", "/api/v1/platform/assist/accept", "", map[string]any{"token": tok2, "widget": widgetFor(72002, "Айгерим", "")})
	at2 := tokenOf(asJSON(t, w))
	w = e.call("GET", "/api/v1/platform/assist", at2, nil)
	st = asJSON(t, w)
	list, _ := st["residents"].([]any)
	if st["mode"] != "assistant" || len(list) != 2 || st["own"] != false {
		t.Fatalf("assistant state: %s", w.Body.String())
	}
	// a plain Telegram login of the assistant lands in the last resident's workspace
	l2 := e.login(t, 72002, "Айгерим")
	if u, _ := l2["user"].(map[string]any); u["assistant"] == nil {
		t.Fatalf("assistant login: %v", l2)
	}
	w = e.call("POST", "/api/v1/platform/assist/switch", at2, map[string]any{"id": aid})
	if w.Code != 200 {
		t.Fatalf("switch: %d %s", w.Code, w.Body.String())
	}
	at3 := tokenOf(asJSON(t, w))
	if u := asJSON(t, w)["user"].(map[string]any); u["id"] != "tg:72001" {
		t.Fatalf("switch user: %v", u)
	}
	if w := e.call("GET", "/api/v1/platform/sync", at2, nil); w.Code != 401 {
		t.Fatalf("the session switched from must close: %d", w.Code)
	}
	if w := e.call("POST", "/api/v1/platform/assist/switch", at3, map[string]any{"id": 0}); w.Code != 403 {
		t.Fatalf("an assistant without an own workspace: %d %s", w.Code, w.Body.String())
	}

	// 10. revoke: the assistant is out at once
	if w := e.call("POST", "/api/v1/platform/assist/revoke", rt, map[string]any{"id": aid}); w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", at3, nil); w.Code != 401 {
		t.Fatalf("revoked assistant: %d %s", w.Code, w.Body.String())
	}
	if w := e.call("GET", "/api/v1/platform/sync", at, nil); w.Code != 401 {
		t.Fatalf("revoked assistant (first session): %d", w.Code)
	}

	// 11. logout closes the session on the server; «выйти на других устройствах»
	legacy, _, _ := e.auth.jwt.GenerateTokens("tg:72001", "resident", []string{}) // a token from before sessions
	if w := e.call("GET", "/api/v1/platform/sync", legacy, nil); w.Code != 200 {
		t.Fatalf("legacy token: %d", w.Code)
	}
	other := tokenOf(e.login(t, 72001, "Альфа"))
	time.Sleep(1100 * time.Millisecond)
	if w := e.call("POST", "/api/v1/platform/sessions/revoke-others", rt, map[string]any{}); w.Code != 200 {
		t.Fatalf("revoke others: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", other, nil); w.Code != 401 {
		t.Fatalf("other device must be out: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", legacy, nil); w.Code != 401 {
		t.Fatalf("legacy token must be out: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", rt, nil); w.Code != 200 {
		t.Fatalf("this device stays: %d", w.Code)
	}
	if w := e.call("POST", "/api/v1/platform/auth/logout", rt, nil); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", rt, nil); w.Code != 401 {
		t.Fatalf("logged out token: %d", w.Code)
	}
}

func TestAssistLoginLinkWithoutTelegram(t *testing.T) {
	e := newAsEnv(t)
	ctx := context.Background()
	team, _, _ := e.auth.jwt.GenerateTokens("tg:72999", "admin", []string{})
	res := tokenOf(e.login(t, 72001, "Альфа"))

	// only the team issues links
	if w := e.call("POST", "/api/v1/platform/access/link", res, map[string]any{"name": "Елена Безтелеграмова"}); w.Code != 403 {
		t.Fatalf("resident issued a link: %d", w.Code)
	}
	if w := e.call("POST", "/api/v1/platform/access/link", team, map[string]any{"name": "Бывшая Резидентка"}); w.Code != 404 {
		t.Fatalf("former resident got a link: %d %s", w.Code, w.Body.String())
	}
	w := e.call("GET", "/api/v1/platform/access/resident?name="+strings.ReplaceAll("Елена Безтелеграмова", " ", "%20"), team, nil)
	if w.Code != 200 || asJSON(t, w)["telegram"] != false {
		t.Fatalf("card: %d %s", w.Code, w.Body.String())
	}
	w = e.call("POST", "/api/v1/platform/access/link", team, map[string]any{"name": "елена  безтелеграмова"})
	if w.Code != 200 {
		t.Fatalf("link: %d %s", w.Code, w.Body.String())
	}
	lk := asJSON(t, w)
	url := lk["url"].(string)
	if !strings.HasPrefix(url, "https://app.bxclub.kz/in/") {
		t.Fatalf("link url %q", url)
	}
	tok := lastPath(url)
	var n int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_login_links WHERE token_hash = $1 AND expires_at BETWEEN now() + interval '29 days' AND now() + interval '31 days'`, hashSecretToken(tok)).Scan(&n)
	if n != 1 {
		t.Fatal("link not stored as a 30-day hash")
	}
	if w := e.call("GET", "/in/"+tok, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Войти на платформу") || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("link page: %d", w.Code)
	}
	// Елена opens it (twice, two devices): resident of her own workspace
	w = e.call("POST", "/api/v1/platform/auth/link", "", map[string]any{"token": tok})
	if w.Code != 200 {
		t.Fatalf("link login: %d %s", w.Code, w.Body.String())
	}
	in := asJSON(t, w)
	u := in["user"].(map[string]any)
	var cid int64
	_ = e.db.Pool.QueryRow(ctx, `SELECT id FROM club_residents WHERE name = 'Елена Безтелеграмова'`).Scan(&cid)
	if u["role"] != "resident" || u["id"] != "tg:-"+strconv.FormatInt(cid, 10) || u["name"] != "Елена Безтелеграмова" {
		t.Fatalf("link user: %v", u)
	}
	lt := tokenOf(in)
	if w := e.call("GET", "/api/v1/platform/sync", lt, nil); w.Code != 200 {
		t.Fatalf("link session sync: %d %s", w.Code, w.Body.String())
	}
	lt2 := tokenOf(asJSON(t, e.call("POST", "/api/v1/platform/auth/link", "", map[string]any{"token": tok})))
	// she can invite her own assistant too
	if w := e.call("POST", "/api/v1/platform/assist/invite", lt, map[string]any{}); w.Code != 200 {
		t.Fatalf("link resident invite: %d %s", w.Code, w.Body.String())
	}

	// an assistant without Telegram, by a link from the team
	w = e.call("POST", "/api/v1/platform/access/link", team, map[string]any{"name": "Елена Безтелеграмова", "assistantName": "Мадина", "preset": "nofin"})
	if w.Code != 200 {
		t.Fatalf("assistant link: %d %s", w.Code, w.Body.String())
	}
	atok := lastPath(asJSON(t, w)["url"].(string))
	w = e.call("POST", "/api/v1/platform/auth/link", "", map[string]any{"token": atok})
	ain := asJSON(t, w)
	au := ain["user"].(map[string]any)
	if w.Code != 200 || au["assistant"] == nil || au["id"] != "tg:-"+strconv.FormatInt(cid, 10) {
		t.Fatalf("assistant by link: %d %v", w.Code, ain)
	}
	if as := au["assistant"].(map[string]any); as["name"] != "Мадина" || as["preset"] != "nofin" {
		t.Fatalf("assistant by link: %v", as)
	}
	at := tokenOf(ain)
	// «Всё, кроме финансов»: other sections yes, device settings no
	if w := e.call("PUT", "/api/v1/platform/docs/bs_kanban", at, map[string]any{"version": 0, "value": `[]`}); w.Code != 200 {
		t.Fatalf("nofin write: %d %s", w.Code, w.Body.String())
	}

	// the team sees it all in the card
	w = e.call("GET", "/api/v1/platform/access/resident?name=Елена%20Безтелеграмова", team, nil)
	card := asJSON(t, w)
	if links, _ := card["links"].([]any); len(links) != 2 {
		t.Fatalf("card links: %s", w.Body.String())
	}
	if ss, _ := card["sessions"].([]any); len(ss) != 3 {
		t.Fatalf("card sessions: %s", w.Body.String())
	}

	// revoke the resident's link: both her devices are out, the link no longer opens
	if w := e.call("POST", "/api/v1/platform/access/link/revoke", team, map[string]any{"id": lk["id"]}); w.Code != 200 {
		t.Fatalf("revoke link: %d", w.Code)
	}
	for _, x := range []string{lt, lt2} {
		if w := e.call("GET", "/api/v1/platform/sync", x, nil); w.Code != 401 {
			t.Fatalf("revoked link session: %d", w.Code)
		}
	}
	if w := e.call("POST", "/api/v1/platform/auth/link", "", map[string]any{"token": tok}); w.Code != http.StatusGone {
		t.Fatalf("revoked link login: %d", w.Code)
	}
	// the assistant's link is separate and still works; the team closes that session
	sid := ""
	for _, s := range card["sessions"].([]any) {
		m := s.(map[string]any)
		if m["kind"] == "assistant" {
			sid, _ = m["id"].(string)
		}
	}
	if w := e.call("GET", "/api/v1/platform/sync", at, nil); w.Code != 200 {
		t.Fatalf("assistant link session: %d", w.Code)
	}
	if w := e.call("POST", "/api/v1/platform/access/session/revoke", team, map[string]any{"sid": sid}); w.Code != 200 {
		t.Fatalf("team session revoke: %d", w.Code)
	}
	if w := e.call("GET", "/api/v1/platform/sync", at, nil); w.Code != 401 {
		t.Fatalf("closed assistant session: %d", w.Code)
	}

	// rate limit: guessing links from one address stops
	got429 := false
	for i := 0; i < 25; i++ {
		w := e.call("POST", "/api/v1/platform/auth/link", "", map[string]any{"token": strings.Repeat("A", 43)}, "10.9.9.9")
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if w.Code != http.StatusGone {
			t.Fatalf("bad token: %d", w.Code)
		}
	}
	if !got429 {
		t.Fatal("no rate limit on link guessing")
	}
}

func TestAssistAllowedMatrix(t *testing.T) {
	cases := []struct {
		preset, method, path, key string
		want                      bool
	}{
		{"tasks", "GET", "/api/v1/platform/sync", "", true},
		{"tasks", "PUT", "/api/v1/platform/boards/:id", "", true},
		{"tasks", "DELETE", "/api/v1/platform/boards/:id", "", false},
		{"tasks", "PUT", "/api/v1/platform/docs/:key", "bs_mycal", true},
		{"tasks", "PUT", "/api/v1/platform/docs/:key", "bs_gallup", false},
		{"tasks", "POST", "/api/v1/platform/ai/gallup", "", false},
		{"nofin", "POST", "/api/v1/platform/ai/gallup", "", true},
		{"nofin", "PUT", "/api/v1/platform/docs/:key", "bs_onboard", false},
		{"nofin", "GET", "/api/v1/platform/sales/me", "", false},
		{"nofin", "POST", "/api/v1/platform/sales/me/renew", "", false},
		{"nofin", "POST", "/api/v1/platform/ai/forecast", "", false},
		{"nofin", "POST", "/api/v1/platform/gcal/connect", "", false},
		{"nofin", "POST", "/api/v1/platform/access/link", "", false},
		{"view", "GET", "/api/v1/platform/sync", "", true},
		{"view", "POST", "/api/v1/platform/presence", "", true},
		{"view", "POST", "/api/v1/platform/razbor/pdf", "", true},
		{"view", "PUT", "/api/v1/platform/boards/:id", "", false},
		{"view", "GET", "/api/v1/platform/sales/me", "", false},
		{"view", "POST", "/api/v1/platform/assist/switch", "", true},
		{"view", "POST", "/api/v1/platform/assist/revoke", "", false},
		{"bogus", "PUT", "/api/v1/platform/boards/:id", "", false},
	}
	for _, c := range cases {
		if got := assistAllowed(c.preset, c.method, c.path, c.key); got != c.want {
			t.Errorf("%s %s %s %s: %v, want %v", c.preset, c.method, c.path, c.key, got, c.want)
		}
	}
	if !validSecretToken(newSecretToken()) || validSecretToken("short") || validSecretToken(strings.Repeat("a", 42)+"/") {
		t.Fatal("token format")
	}
	if deviceOf("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15") != "Mac · Safari" {
		t.Fatal("device")
	}
}
