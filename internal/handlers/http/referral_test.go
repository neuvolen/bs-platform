package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// msgsTo: the bot messages the fake Telegram got for one chat.
func (e *botEnv) msgsTo(chat int64) []map[string]any {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	var out []map[string]any
	for _, m := range e.tg.sent {
		if int64(m["chat_id"].(float64)) == chat {
			out = append(out, m)
		}
	}
	return out
}

func (e *botEnv) waitMsgs(chat int64, n int) []map[string]any {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := e.msgsTo(chat); len(m) >= n {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	return e.msgsTo(chat)
}

func crmLead(t *testing.T, repo *pg.PlatformRepo, tg int64) map[string]any {
	t.Helper()
	d, err := repo.GetDoc(context.Background(), "club", "bs_crm")
	if err != nil || d == nil {
		t.Fatalf("crm: %v", err)
	}
	var crm map[string]any
	_ = json.Unmarshal([]byte(d.Value), &crm)
	leads, _ := crm["leads"].([]any)
	return findLeadByTg(leads, tg)
}

func setLead(t *testing.T, f *LeadFunnel, tg int64, kv map[string]any) {
	t.Helper()
	err := f.mutate(context.Background(), "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		l := findLeadByTg(leads, tg)
		if l == nil {
			t.Fatalf("no lead %d", tg)
		}
		for k, v := range kv {
			l[k] = v
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReferrals(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform_docs WHERE (scope='club' AND key IN ('bs_crm','bs_ck_stats')) OR (scope='server' AND key='ref_won')`,
		`DELETE FROM platform_residents WHERE tg_id IN (4001, 4002)`,
		`INSERT INTO platform_residents (tg_id, name, active) VALUES (4001, 'Резидент Реф', true), (4002, 'Бывший', false)`,
		`DELETE FROM club_residents WHERE tg_id IN (5555, 5556, 5557)`,
	} {
		if _, err := e.db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := pg.NewPlatformRepo(e.db)
	team := map[int64]string{111: "Рустам"}
	f := NewLeadFunnel(repo, e.svc.SendMessageKB, []int64{111})
	e.svc.SetStartHook(f.WithReferrals(repo, team))
	e.useTestRelay()
	start := func(id, from int64, first, text string) {
		e.hook(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":%q,"username":"u%d"},"text":%q}}`,
			id, id, time.Now().Unix(), from, from, first, from, text))
	}

	// A new person by a resident's link: welcome, CRM, one note to the inviter and one to the team.
	start(6001, 5555, "Айдар", "/start ref_4001")
	if w := e.waitMsgs(5555, 1); len(w) != 1 || !strings.Contains(w[0]["text"].(string), "99 гайдов") {
		t.Fatalf("welcome: %v", w)
	}
	inv := e.waitMsgs(4001, 1)
	if len(inv) != 1 || inv[0]["text"] != "🔥 Айдар перешёл по твоей ссылке и запустил бота. Если станет резидентом, твой бонус 100 000 ₸." {
		t.Fatalf("inviter: %v", inv)
	}
	if a := e.waitMsgs(111, 1); len(a) != 1 || !strings.Contains(a[0]["text"].(string), "Реферал: Резидент Реф") {
		t.Fatalf("admin: %v", a)
	}
	l := crmLead(t, repo, 5555)
	if l == nil || anyInt(l["ref"]) != 4001 || l["refName"] != "Резидент Реф" || l["source"] != "Реферал: Резидент Реф" || l["col"] != "new" {
		t.Fatalf("lead: %v", l)
	}
	// /start again: nothing more to the inviter
	start(6002, 5555, "Айдар", "/start ref_4001")
	time.Sleep(700 * time.Millisecond)
	if len(e.msgsTo(4001)) != 1 || len(e.msgsTo(5555)) != 1 {
		t.Fatal("repeat start notified again")
	}

	// A link of someone who is not a resident: recorded, nobody promised a bonus.
	start(6003, 5556, "Олжас", "/start ref_9999")
	e.waitMsgs(5556, 1)
	time.Sleep(300 * time.Millisecond)
	if len(e.msgsTo(9999)) != 0 {
		t.Fatal("a stranger must not get the bonus note")
	}
	if l := crmLead(t, repo, 5556); l == nil || anyInt(l["ref"]) != 9999 || l["refGuest"] != true || l["source"] != "Реферал: id 9999" {
		t.Fatalf("guest lead: %v", l)
	}
	// Own link: an ordinary start.
	start(6004, 5557, "Сам", "/start ref_5557")
	e.waitMsgs(5557, 1)
	if l := crmLead(t, repo, 5557); l == nil || l["ref"] != nil || l["source"] != "Telegram: бот" {
		t.Fatalf("own link: %v", l)
	}

	// The app: the resident's link and invited people.
	g := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	g.Admins, g.Boards, g.Funnel = team, repo, f
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAppGatewayModule(g).Register(r)
	get := func(id int64) (int, map[string]any) {
		w := httptest.NewRecorder()
		q := url.Values{"_tg": {makeInitData(testBotToken, id, "X", time.Now())}}
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/app/referral?"+q.Encode(), nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := get(5555); code != 403 || out["error"] != "residents_only" {
		t.Fatalf("lead: %d %v", code, out)
	}
	if code, _ := get(4002); code != 403 {
		t.Fatalf("former resident: %d", code)
	}
	code, out := get(4001)
	if code != 200 || out["link"] != "https://t.me/bsurgery_bot?start=ref_4001" {
		t.Fatalf("referral: %d %v", code, out)
	}
	txt := out["text"].(string)
	if !strings.Contains(txt, "99 гайдов") || !strings.Contains(txt, "разбор") || strings.Contains(txt, "—") || !strings.Contains(txt, "ref_4001") {
		t.Fatalf("text: %q", txt)
	}
	if n := strings.Count(txt, "\n") + 1; n < 3 || n > 5 {
		t.Fatalf("text lines: %d", n)
	}
	invited := out["invited"].([]any)
	if len(invited) != 1 || invited[0].(map[string]any)["stage"] != "new" || invited[0].(map[string]any)["name"] != "Айдар" || out["bonusTotal"] != float64(0) {
		t.Fatalf("invited: %v", out)
	}
	if code, out := get(111); code != 200 || len(out["invited"].([]any)) != 0 {
		t.Fatalf("team: %d %v", code, out)
	}

	// Stages from the CRM column.
	setLead(t, f, 5555, map[string]any{"col": "diag"})
	if _, out := get(4001); out["invited"].([]any)[0].(map[string]any)["stage"] != "razbor" {
		t.Fatalf("razbor stage: %v", out)
	}
	if n := f.RefWonOnce(ctx); n != 0 {
		t.Fatalf("won before won: %d", n)
	}

	// The team moves the card to won: the inviter and the team hear it once.
	setLead(t, f, 5555, map[string]any{"col": "won"})
	setLead(t, f, 5556, map[string]any{"col": "won"})
	adm := len(e.msgsTo(111))
	if n := f.RefWonOnce(ctx); n != 2 {
		t.Fatalf("won: %d", n)
	}
	got := e.msgsTo(4001)
	if len(got) != 2 || got[1]["text"] != "Поздравляем: Айдар стал резидентом BS. Твой бонус 100 000 ₸, команда свяжется по выплате." {
		t.Fatalf("congrats: %v", got)
	}
	if len(e.msgsTo(9999)) != 0 {
		t.Fatal("guest inviter congratulated")
	}
	if a := e.msgsTo(111); len(a) != adm+2 {
		t.Fatalf("admin won notes: %d", len(a)-adm)
	}
	if n := f.RefWonOnce(ctx); n != 0 {
		t.Fatalf("won again: %d", n)
	}
	setLead(t, f, 5555, map[string]any{"col": "lost"})
	setLead(t, f, 5555, map[string]any{"col": "won"})
	if n := f.RefWonOnce(ctx); n != 0 {
		t.Fatalf("won twice: %d", n)
	}
	_, out = get(4001)
	it := out["invited"].([]any)[0].(map[string]any)
	if it["stage"] != "resident" || it["bonus"] != float64(100000) || it["paid"] != false || out["bonusTotal"] != float64(100000) || out["bonusPaid"] != float64(0) {
		t.Fatalf("resident: %v", out)
	}
	setLead(t, f, 5555, map[string]any{"refPaid": true})
	_, out = get(4001)
	if out["bonusPaid"] != float64(100000) || out["invited"].([]any)[0].(map[string]any)["paid"] != true {
		t.Fatalf("paid: %v", out)
	}
}
