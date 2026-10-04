package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	// Tilda waits for a bare «ok» (else two more tries and «Webhook URL not available»).
	if w := do(hook, form, body, nil); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("lead %d %s", w.Code, w.Body.String())
	}
	if tr := ReadTildaTries(ctx, tl.Meta); len(tr) == 0 || tr[0].Result != "accepted" || tr[0].Via != "tilda" {
		t.Fatalf("attempt %+v", tr)
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
	if w := do(hook, form, body, nil); w.Body.String() != "ok" || ReadTildaTries(ctx, tl.Meta)[0].Result != "duplicate" {
		t.Fatalf("dup tranid %s", w.Body.String())
	}
	if w := do(hook, form, "Name=Ерлан&Phone=%2B77011234567&tranid=7777:112", nil); w.Body.String() != "ok" || ReadTildaTries(ctx, tl.Meta)[0].Result != "duplicate" {
		t.Fatalf("dup phone %s", w.Body.String())
	}

	// 3. JSON with Tilda's «API key» as a header, on the address without the key.
	js := `{"name":"Айгерим","phone":"+7 777 555 44 33","email":"a@b.kz","formname":"Консультация","tranid":"8888:1","utm_source":"google","utm_campaign":"brand"}`
	if w := do("/api/v1/public/tilda", "application/json", js, map[string]string{"X-Tilda-Key": tok}); w.Code != 200 || w.Body.String() != "ok" {
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

func TestTildaDerivedToken(t *testing.T) {
	a := &TildaLeads{BotToken: "123:abc"}
	b := &TildaLeads{BotToken: "123:abc"}
	if a.DerivedToken() == "" || a.DerivedToken() != b.DerivedToken() || len(a.DerivedToken()) != 32 {
		t.Fatalf("derived token not stable: %q", a.DerivedToken())
	}
	if (&TildaLeads{}).DerivedToken() != "" {
		t.Fatal("empty bot token must give empty key")
	}
}

// R36c: every attempt is logged (names of fields only), the address works
// with a slash at the end, the derived key works next to the stored one,
// a wrong key is shown with its reason, a field named by the owner is found.
func TestTildaAttempts(t *testing.T) {
	t.Setenv("TILDA_TOKEN", "")
	e := newOffEnv(t, "server", nil)
	ctx := context.Background()
	docs := pg.NewPlatformRepo(e.db)
	meta := pg.NewBotRepo(e.db)
	_ = meta.SetMeta(ctx, tildaMetaTries, "")
	_ = meta.SetMeta(ctx, tildaMetaToken, "stored-key-0123456789abcdef") // an earlier random key
	tl := NewTildaLeads(e.writes, docs, meta, testBotToken)
	NewTildaModule(tl, []byte("tilda-secret")).Register(e.r)
	do := func(method, path, ct, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.RemoteAddr = "10.0.0.9:1234"
		w := httptest.NewRecorder()
		e.r.ServeHTTP(w, req)
		return w
	}
	const form = "application/x-www-form-urlencoded"
	der := tl.DerivedToken()
	last := func() tildaTry {
		tr := ReadTildaTries(ctx, meta)
		if len(tr) == 0 {
			t.Fatal("no attempts")
		}
		return tr[0]
	}
	// a wrong key: 401, logged with the reason, values never kept
	if w := do("POST", "/api/v1/public/tilda/029f0a15e4bcef2a86e788d428445429", form, "Name=Секрет&Phone=87017778899"); w.Code != 401 {
		t.Fatalf("wrong key %d", w.Code)
	}
	x := last()
	if x.Result != "rejected" || x.Reason != "неверный ключ" || x.Path != "/tilda/029f…5429" || strings.Join(x.Fields, ",") != "Name,Phone" || x.CT != form {
		t.Fatalf("wrong key attempt %+v", x)
	}
	if raw, _ := meta.GetMeta(ctx, tildaMetaTries); strings.Contains(raw, "Секрет") || strings.Contains(raw, "8899") {
		t.Fatalf("values kept: %s", raw)
	}
	// no key at all
	if w := do("POST", "/api/v1/public/tilda", form, "Name=X"); w.Code != 401 || last().Reason != "нет ключа в адресе" {
		t.Fatalf("no key %d %+v", w.Code, last())
	}
	// the derived key, with a slash at the end: test ping, then GET from a browser
	if w := do("POST", "/api/v1/public/tilda/"+der+"/", form, "test=test"); w.Code != 200 || w.Body.String() != "ok" || last().Result != "test" {
		t.Fatalf("slash %d %q %+v", w.Code, w.Body.String(), last())
	}
	if w := do("GET", "/api/v1/public/tilda/"+der, "", ""); w.Code != 200 || last().Reason != "метод GET (Tilda шлёт POST)" {
		t.Fatalf("get %d %+v", w.Code, last())
	}
	// the stored key still works
	if w := do("POST", "/api/v1/public/tilda/stored-key-0123456789abcdef", form, "test=test"); w.Code != 200 {
		t.Fatalf("stored key %d", w.Code)
	}
	// a form whose fields the owner named himself
	body := url.Values{"Ваше_имя": {"Жанар"}, "Input_3": {"+7 (705) 222-33-44"}, "tranid": {"5555:1"}, "formid": {"form77"}}.Encode()
	if w := do("POST", "/api/v1/public/tilda/"+der, form, body); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("custom fields %d %s", w.Code, w.Body.String())
	}
	if x := last(); x.Result != "accepted" {
		t.Fatalf("custom fields attempt %+v", x)
	}
	e.tg.waitText(t, offOwner, "Жанар")
	// nothing to call: Tilda gets «ok» (no retries), the log says why
	if w := do("POST", "/api/v1/public/tilda/"+der, form, "Checkbox=yes&tranid=5555:2"); w.Code != 200 || w.Body.String() != "ok" || last().Reason != "пустой телефон и имя" {
		t.Fatalf("empty %d %+v", w.Code, last())
	}
	// the older script's /api/v1/bot/lead goes into the same log
	tl.ScriptLead(ctx, map[string]string{"name": "Б", "phone": "+77001112233"}, nil)
	if x := last(); x.Path != "/bot/lead" || x.Via != "script" || x.Result != "accepted" {
		t.Fatalf("script lead %+v", x)
	}
	tl.ScriptLead(ctx, nil, errors.New("подпись скрипта не совпала"))
	if x := last(); x.Result != "rejected" || x.Reason != "подпись скрипта не совпала" {
		t.Fatalf("script bad sig %+v", x)
	}
	// the log keeps 20
	for i := 0; i < 25; i++ {
		do("POST", "/api/v1/public/tilda/bad"+strconv.Itoa(i), form, "a=b")
	}
	if n := len(ReadTildaTries(ctx, meta)); n != tildaTriesKeep {
		t.Fatalf("kept %d", n)
	}
	in, _ := ReadTildaLeads(ctx, meta, time.Now())
	if in.Last == nil || in.Day != tildaTriesKeep || in.At.IsZero() {
		t.Fatalf("info %+v", in)
	}
	if ruTries(1) != "1 попытка" || ruTries(3) != "3 попытки" || ruTries(11) != "11 попыток" || ruTries(20) != "20+ попыток" {
		t.Fatal("plural")
	}
}
