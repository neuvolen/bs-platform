package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
)

func TestTildaParse(t *testing.T) {
	for in, want := range map[string]string{"8 (701) 123-45-67": "+77011234567", "+7 701 123 45 67": "+77011234567",
		"7011234567": "+77011234567", "77011234567": "+77011234567", "123": ""} {
		if got := tildaPhone(in); got != want {
			t.Fatalf("phone %q: %q", in, got)
		}
	}
	for in, want := range map[string]string{"bsurgery": "@bsurgery", "@bs": "@bs", "https://t.me/bs_x": "@bs_x", "+7 701": "+7 701"} {
		if got := tildaTg(in); got != want {
			t.Fatalf("tg %q: %q", in, got)
		}
	}
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f := tildaFields(r, []byte("Name=%D0%90&Phone=87011234567&formname=Заявка&COOKIES="+
		url.QueryEscape("_ym=1; TILDAUTM=utm_source%3Dig%7C%7C%7Cutm_medium%3Dstories%7C%7C%7Cutm_campaign%3Dautumn")))
	l := tildaParse(f)
	if l.Name != "А" || l.Phone != "+77011234567" || l.Source != "Сайт: Заявка" || l.UTM["utm_source"] != "ig" ||
		l.UTM["utm_medium"] != "stories" || l.Campaign != "autumn" {
		t.Fatalf("%+v", l)
	}
}

func TestTildaHook(t *testing.T) {
	t.Setenv("TILDA_TOKEN", "")
	t.Setenv("PUBLIC_URL", "https://srv.example.kz")
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='club' AND key='bs_crm'`); err != nil {
		t.Fatal(err)
	}
	docs := pg.NewPlatformRepo(e.db)
	tl := NewTildaLeads(e.writes, docs, pg.NewBotRepo(e.db), testBotToken)
	NewTildaModule(tl, []byte("tilda-secret")).Register(e.r)
	tok, env := tl.Token(ctx)
	if tok == "" || env {
		t.Fatalf("token %q %v", tok, env)
	}
	if again, _ := NewTildaLeads(nil, nil, pg.NewBotRepo(e.db), "").Token(ctx); again != tok {
		t.Fatal("the token is not kept")
	}
	do := func(path, ct, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		req.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		e.r.ServeHTTP(w, req)
		return w
	}
	const form = "application/x-www-form-urlencoded"
	hook := "/api/v1/public/tilda/" + tok

	// Tilda's connection check; a wrong or missing key is refused.
	if w := do(hook, form, "test=test", nil); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("test %d %s", w.Code, w.Body.String())
	}
	if w := do("/api/v1/public/tilda/wrong", form, "test=test", nil); w.Code != 401 {
		t.Fatalf("wrong token %d", w.Code)
	}
	if w := do("/api/v1/public/tilda", form, "Name=X&Phone=87010000000", nil); w.Code != 401 {
		t.Fatalf("no token %d", w.Code)
	}

	// 1. A form-encoded lead with UTM from the cookies.
	body := url.Values{"Name": {"Ерлан"}, "Phone": {"8 (701) 123-45-67"}, "Telegram": {"erlan_kz"}, "Comment": {"Хочу на разбор"},
		"formname": {"Заявка на разбор"}, "tranid": {"7777:111"}, "formid": {"form1"},
		"COOKIES": {"TILDAUTM=utm_source%3Dinstagram%7C%7C%7Cutm_campaign%3Doctober"}}.Encode()
	if w := do(hook, form, body, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("lead %d %s", w.Code, w.Body.String())
	}
	msg := e.tg.waitText(t, offOwner, "Новый лид")
	for _, s := range []string{"👤 Ерлан", "📱 +77011234567", "💬 @erlan_kz", "📍 Источник: Сайт: Заявка на разбор", "🎯 october", "📝 Хочу на разбор"} {
		if !strings.Contains(msg, s) {
			t.Fatalf("notice %q lacks %q", msg, s)
		}
	}
	if n := e.n(`SELECT count(*) FROM club_sheets WHERE name = 'CRM Лиды' AND rows::text LIKE '%+77011234567%' AND rows::text LIKE '%Сайт: Заявка на разбор%' AND rows::text LIKE '%utm_source=instagram%'`); n != 1 {
		t.Fatal("not in «CRM Лиды»")
	}

	// 2. The same submission again (tranid) and the same phone in 10 minutes: one lead, one notice.
	if w := do(hook, form, body, nil); !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("dup tranid %s", w.Body.String())
	}
	if w := do(hook, form, "Name=Ерлан&Phone=%2B77011234567&tranid=7777:112", nil); !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("dup phone %s", w.Body.String())
	}

	// 3. JSON with Tilda's «API key» as a header, on the address without the key.
	js := `{"name":"Айгерим","phone":"+7 777 555 44 33","email":"a@b.kz","formname":"Консультация","tranid":"8888:1","utm_source":"google","utm_campaign":"brand"}`
	if w := do("/api/v1/public/tilda", "application/json", js, map[string]string{"X-Tilda-Key": tok}); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("json %d %s", w.Code, w.Body.String())
	}
	e.tg.waitText(t, offOwner, "Айгерим")

	// 4. Forwarded by the script, signed with the bot token.
	fw, _ := json.Marshal(map[string]any{"ts": time.Now().Unix(), "Name": "Дана", "Phone": "87051112233", "formname": "Заявка", "tranid": "9999:1"})
	if w := do("/api/v1/public/tilda", "application/json", string(fw), map[string]string{"X-BS-Signature": sign(fw)}); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("script %d %s", w.Code, w.Body.String())
	}
	if w := do("/api/v1/public/tilda", "application/json", string(fw), map[string]string{"X-BS-Signature": "00"}); w.Code != 401 {
		t.Fatalf("bad signature %d", w.Code)
	}
	e.tg.waitText(t, offOwner, "Дана")

	// The platform CRM: three cards, fields and UTM kept.
	d, err := docs.GetDoc(ctx, "club", "bs_crm")
	if err != nil || d == nil {
		t.Fatal("no bs_crm", err)
	}
	var crm struct {
		Leads []map[string]any `json:"leads"`
	}
	_ = json.Unmarshal([]byte(d.Value), &crm)
	if len(crm.Leads) != 3 {
		t.Fatalf("crm %d leads: %s", len(crm.Leads), d.Value)
	}
	var er map[string]any
	for _, l := range crm.Leads {
		if l["phone"] == "+77011234567" {
			er = l
		}
	}
	if er == nil || er["source"] != "Сайт: Заявка на разбор" || er["col"] != "new" || er["tg"] != "@erlan_kz" || er["note"] != "Хочу на разбор" ||
		er["id"] != "site7777111" || er["form"] != "Заявка на разбор" {
		t.Fatalf("lead %v", er)
	}
	if u, _ := er["utm"].(map[string]any); u["utm_source"] != "instagram" || u["utm_campaign"] != "october" {
		t.Fatalf("utm %v", er["utm"])
	}

	// Each lead told the team once.
	time.Sleep(300 * time.Millisecond)
	cnt := 0
	for _, s := range e.tg.to(offOwner) {
		if strings.Contains(s, "Новый лид") {
			cnt++
		}
	}
	if cnt != 3 {
		t.Fatalf("%d notices: %q", cnt, e.tg.to(offOwner))
	}

	// Settings → «Заявки с сайта»: the address for Tilda and the last lead.
	jm := auth.NewManager("tilda-secret", time.Hour, time.Hour)
	get := func(role string) *httptest.ResponseRecorder {
		at, _, _ := jm.GenerateTokens("tg:453800951", role, []string{})
		req := httptest.NewRequest("GET", "/api/v1/platform/tilda", nil)
		req.Header.Set("Authorization", "Bearer "+at)
		w := httptest.NewRecorder()
		e.r.ServeHTTP(w, req)
		return w
	}
	w := get("admin")
	var st struct {
		URL  string    `json:"url"`
		Last tildaLast `json:"last"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || st.URL != "https://srv.example.kz/api/v1/public/tilda/"+tok || st.Last.Via != "script" || st.Last.At == "" || st.Last.Count < 3 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if w := get("resident"); w.Code != http.StatusForbidden {
		t.Fatalf("resident sees the key: %d", w.Code)
	}
}

func TestTildaRateLimit(t *testing.T) {
	tl := NewTildaLeads(nil, nil, nil, "")
	for i := 0; i < tildaIPLimit; i++ {
		if tl.limited("1.2.3.4") {
			t.Fatalf("limited at %d", i)
		}
	}
	if !tl.limited("1.2.3.4") || tl.limited("5.6.7.8") {
		t.Fatal("per-address limit")
	}
}
