package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

func claimSnap() *club.Snapshot {
	return &club.Snapshot{
		Residents: []club.Resident{
			{Name: "Асет Нурланов", TgID: 888},
			{Name: "Алия Каримова"},
			{Name: "Сапаров Ерлан"},
			{Name: "Бывший Резидент", TgID: 444, Former: true},
			{Name: "Мадина Ахметова"},
			{Name: "Данияр Сеитов", TgID: 999999},
		},
		Reports: []club.ReportEntry{
			{Username: "@Aliya_K", Name: "Алия Каримова"},
			{TgUserID: 321, Name: "Мадина Ахметова"},
			{Username: "daniyar", Name: "Данияр Сеитов"},
		},
	}
}

func claimCRM() map[string]any {
	return map[string]any{"leads": []any{
		map[string]any{"id": "x1", "col": "won", "name": "Алия Каримова", "phone": "+7 701 111 22 33"},
		map[string]any{"id": "tg654", "tgId": float64(654), "col": "new", "name": "Кто-то", "phone": "87011112233"},
	}}
}

func TestMatchClaim(t *testing.T) {
	snap, crm := claimSnap(), claimCRM()
	for _, c := range []struct {
		w          ClaimWho
		kind, name string
	}{
		{ClaimWho{TgID: 888, FirstName: "Асет"}, claimLinked, "Асет Нурланов"},
		{ClaimWho{TgID: 1001, FirstName: "Aliya", Username: "aliya_k"}, claimLink, "Алия Каримова"},
		{ClaimWho{TgID: 321, FirstName: "М"}, claimLink, "Мадина Ахметова"},
		{ClaimWho{TgID: 654, FirstName: "Кто-то"}, claimLink, "Алия Каримова"}, // by phone
		{ClaimWho{TgID: 1002, Username: "Daniyar"}, claimMaybe, "Данияр Сеитов"},
		{ClaimWho{TgID: 1003, FirstName: "Ерлан", LastName: "Сапаров"}, claimMaybe, "Сапаров Ерлан"},
		{ClaimWho{TgID: 444, FirstName: "Бывший"}, claimFormer, "Бывший Резидент"},
		{ClaimWho{TgID: 1004, FirstName: "Иван", LastName: "Петров", Username: "ivan"}, claimLead, ""},
		{ClaimWho{TgID: 1005, FirstName: "Ерлан"}, claimLead, ""}, // a first name alone is not enough
	} {
		m := MatchClaim(snap, crm, c.w)
		if m.Kind != c.kind || m.Name != c.name {
			t.Errorf("%+v: got %+v, want %s %q", c.w, m, c.kind, c.name)
		}
	}
	if m := MatchClaim(nil, nil, ClaimWho{TgID: 1}); m.Kind != claimLead {
		t.Fatal(m)
	}
	if phoneKey("+7 (701) 111-22-33") != phoneKey("87011112233") || phoneKey("123") != "" {
		t.Fatal("phoneKey")
	}
}

type fakeClaimClub struct{ snap *club.Snapshot }

func (f fakeClaimClub) Load(context.Context) (*club.Snapshot, error) { return f.snap, nil }

// «Я резидент BS» end to end through the bot's webhook.
func TestResidentClaimFlow(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='club' AND key IN ('bs_crm','bs_slots')`); err != nil {
		t.Fatal(err)
	}
	repo := pg.NewPlatformRepo(e.db)
	f := NewLeadFunnel(repo, e.svc.SendMessageKB, []int64{111})
	base := time.Now().In(almaty)
	at := func(day, hour int) time.Time {
		x := base.AddDate(0, 0, day)
		return time.Date(x.Year(), x.Month(), x.Day(), hour, 0, 0, 0, almaty)
	}
	var clock struct {
		sync.Mutex
		t time.Time
	}
	setNow := func(t time.Time) { clock.Lock(); clock.t = t; clock.Unlock() }
	f.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return clock.t }
	setNow(at(0, 2)) // 02:00, night
	claims := NewResidentClaims(f, fakeClaimClub{claimSnap()})
	claims.Names = map[int64]string{111: "Рустам"}
	var linkMu sync.Mutex
	var linked []string
	claims.Link = func(_ context.Context, name string, tg int64) error {
		linkMu.Lock()
		linked = append(linked, fmt.Sprintf("%s=%d", name, tg))
		linkMu.Unlock()
		return nil
	}
	e.svc.SetClaimHook(claims.LeadCallback)
	e.svc.SetTeamCallbackHook("rcl_", claims.TeamCallback)
	e.useTestRelay()

	sentTo := func(chat int64) []map[string]any {
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
	waitSent := func(chat int64, n int) []map[string]any {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if s := sentTo(chat); len(s) >= n {
				return s
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("chat %d: %d messages, want %d", chat, len(sentTo(chat)), n)
		return nil
	}
	uid := 7000
	pressFull := func(from int64, first, last, user, data string) {
		uid++
		e.hook(fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"cb%d","from":{"id":%d,"first_name":%q,"last_name":%q,"username":%q},"data":%q,"message":{"message_id":%d,"chat":{"id":%d,"type":"private"}}}}`,
			uid, uid, from, first, last, user, data, uid, from))
	}
	press := func(from int64, first, user, data string) { pressFull(from, first, "", user, data) }
	text := func(m map[string]any) string { s, _ := m["text"].(string); return s }
	kbOf := func(m map[string]any) string { b, _ := json.Marshal(m["reply_markup"]); return string(b) }
	card := func(tg int64) map[string]any {
		d, _ := repo.GetDoc(ctx, "club", "bs_crm")
		var crm map[string]any
		_ = json.Unmarshal([]byte(d.Value), &crm)
		leads, _ := crm["leads"].([]any)
		return findLeadByTg(leads, tg)
	}
	claimOf := func(tg int64) map[string]any { m, _ := card(tg)["claim"].(map[string]any); return m }

	// 1. A lead at night: a warm answer, the CRM, nobody else woken up.
	press(777, "айдар", "aidar", "i_am_resident")
	m := waitSent(777, 1)[0]
	if !strings.Contains(text(m), "Айдар, спасибо") || !strings.Contains(text(m), "не нашёл") || !strings.Contains(text(m), "50 000 ₸") ||
		strings.Contains(text(m), "—") {
		t.Fatalf("offer: %s", text(m))
	}
	if k := kbOf(m); !strings.Contains(k, "?p=razbor") || !strings.Contains(k, "claim_recheck") || !strings.Contains(k, "app.bxclub.kz") {
		t.Fatalf("offer buttons: %s", k)
	}
	if cl := claimOf(777); cl["status"] != claimLead || card(777)["hot"] != true || card(777)["source"] != "Бот: «Я резидент BS»" {
		t.Fatalf("crm: %v", card(777))
	}
	time.Sleep(300 * time.Millisecond)
	e.script.mu.Lock()
	for _, u := range e.script.got {
		if cq, _ := u["callback_query"].(map[string]any); cq != nil && cq["data"] == "i_am_resident" {
			t.Fatal("the script got the claim: it would ping the team")
		}
	}
	e.script.mu.Unlock()
	// pressed again: the same answer, no second sequence
	press(777, "Айдар", "aidar", "i_am_resident")
	waitSent(777, 2)

	// 2. A resident by Chat ID: «уже резидент»; by username in the reports: linked.
	press(888, "Асет", "", "i_am_resident")
	if m := waitSent(888, 1)[0]; !strings.Contains(text(m), "уже резидент") {
		t.Fatal(text(m))
	}
	press(1001, "Aliya", "aliya_k", "i_am_resident")
	if m := waitSent(1001, 1)[0]; !strings.Contains(text(m), "Нашёл вас в списке резидентов: Алия Каримова") {
		t.Fatal(text(m))
	}
	if card(1001)["col"] != "won" || claimOf(1001)["by"] != "auto" {
		t.Fatalf("auto link in crm: %v", card(1001))
	}

	// 3. Doubtful at night (full name of a resident without a Chat ID): the
	// person is told, the team waits for the morning.
	pressFull(1013, "Ерлан", "Сапаров", "", "i_am_resident")
	if m := waitSent(1013, 1)[0]; !strings.Contains(text(m), "Похоже, вы есть в списке клуба") {
		t.Fatal(text(m))
	}
	if cl := claimOf(1013); cl["status"] != "pending" || cl["name"] != "Сапаров Ерлан" {
		t.Fatalf("pending: %v", cl)
	}
	if n := claims.NotifyPending(ctx); n != 0 || len(sentTo(111)) != 0 {
		t.Fatalf("night: %d sent, team got %d", n, len(sentTo(111)))
	}
	linkMu.Lock()
	if strings.Join(linked, ",") != "Алия Каримова=1001" {
		t.Fatalf("linked: %v", linked)
	}
	linkMu.Unlock()
	setNow(at(0, 11))
	if n := claims.NotifyPending(ctx); n != 1 {
		t.Fatalf("morning: %d", n)
	}
	tm := sentTo(111)
	if len(tm) != 1 || !strings.Contains(text(tm[0]), "Сапаров Ерлан") || !strings.Contains(kbOf(tm[0]), "rcl_res_1013") || !strings.Contains(kbOf(tm[0]), "rcl_lead_1013") {
		t.Fatalf("team message: %v", tm)
	}
	if n := claims.NotifyPending(ctx); n != 0 {
		t.Fatal("told twice")
	}
	// «Это лид»: the warming starts instead of a cold no
	before := len(sentTo(1013))
	e.hook(`{"update_id":7900,"callback_query":{"id":"t1","from":{"id":111,"first_name":"Рустам"},"data":"rcl_lead_1013","message":{"message_id":55,"chat":{"id":111,"type":"private"}}}}`)
	waitSent(1013, before+1)
	if cl := claimOf(1013); cl["status"] != claimLead || cl["by"] != "Рустам" {
		t.Fatalf("after «Это лид»: %v", cl)
	}

	// 4. The warming: a case on day 1, a slot on day 3, one touch a day.
	slotAt := at(5, 11)
	sb, _ := json.Marshal(map[string]any{"slots": []any{map[string]any{"id": "s9", "start": slotAt.Format(time.RFC3339), "dur": 60, "format": "Онлайн", "status": "free"}}})
	if _, err := repo.PutDoc(ctx, "club", "bs_slots", 0, string(sb), false, "test"); err != nil {
		t.Fatal(err)
	}
	setNow(at(1, 3)) // night: nothing
	if n := f.WarmOnce(ctx); n != 0 {
		t.Fatalf("night warm: %d", n)
	}
	n777 := len(sentTo(777))
	setNow(at(1, 12))
	if n := f.WarmOnce(ctx); n < 1 {
		t.Fatalf("day 1: %d", n)
	}
	got := sentTo(777)
	if len(got) != n777+1 || !strings.Contains(text(got[len(got)-1]), "Даурен") {
		t.Fatalf("day 1 case: %v", text(got[len(got)-1]))
	}
	if n := f.WarmOnce(ctx); n != 0 {
		t.Fatalf("repeated: %d", n)
	}
	setNow(at(3, 12))
	f.WarmOnce(ctx)
	got = sentTo(777)
	if last := text(got[len(got)-1]); !strings.Contains(last, whenRu(slotAt)) || !strings.Contains(last, "онлайн") {
		t.Fatalf("day 3 slot: %s", last)
	}
	if card(777)["claimWarm"].(float64) != 2 {
		t.Fatalf("claimWarm %v", card(777)["claimWarm"])
	}
	setNow(at(3, 18))
	f.WarmOnce(ctx)
	if len(sentTo(777)) != len(got) {
		t.Fatal("two touches in one day")
	}

	// 5. «Я уже в клубе»: the team is asked (daytime) and can link from the CRM.
	setNow(at(4, 11))
	nTeam := len(sentTo(111))
	press(777, "Айдар", "aidar", "claim_recheck")
	waitSent(111, nTeam+1)
	tm = sentTo(111)
	if !strings.Contains(kbOf(tm[len(tm)-1]), "Выбрать резидента в CRM") || claimOf(777)["status"] != "pending" {
		t.Fatalf("recheck: %v", text(tm[len(tm)-1]))
	}
	if _, err := claims.Resolve(ctx, 777, "resident", "", "Рустам"); err != errClaimNoName {
		t.Fatalf("no name: %v", err)
	}
	if res, err := claims.Resolve(ctx, 777, "resident", "Мадина Ахметова", "Рустам"); err != nil || !strings.Contains(res, "Мадина") {
		t.Fatalf("resolve: %v %v", res, err)
	}
	got = sentTo(777)
	if !strings.Contains(text(got[len(got)-1]), "Команда подтвердила") || card(777)["col"] != "won" || card(777)["warmStop"] != true {
		t.Fatalf("resolved: %s %v", text(got[len(got)-1]), card(777)["col"])
	}

	// 6. The script's own path (old button, direct app call): signed.
	g := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	g.Claims = claims
	NewAppGatewayModule(g).Register(e.router)
	w := e.signedPost("/api/v1/bot/claim", map[string]any{"chatId": "1004", "name": "Иван Петров", "username": "ivan", "via": "app"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"lead"`) {
		t.Fatalf("script claim: %d %s", w.Code, w.Body.String())
	}
	if m := waitSent(1004, 1)[0]; !strings.Contains(text(m), "Иван, спасибо") || card(1004)["source"] != "Приложение: «Я резидент BS»" {
		t.Fatalf("script claim answer: %s", text(m))
	}
	if w := e.do("POST", "/api/v1/bot/claim", []byte(`{"chatId":"1"}`), map[string]string{"X-BS-Signature": "00"}); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
	w = e.signedPost("/api/v1/bot/claim", map[string]any{"chatId": "1005", "action": "lead"})
	if w.Code != 200 || len(waitSent(1005, 1)) != 1 {
		t.Fatalf("old «Отклонить»: %d %s", w.Code, w.Body.String())
	}
}
