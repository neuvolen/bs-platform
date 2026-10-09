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

// «Я резидент BS» end to end through the bot's webhook (R70: the owner
// confirms with one tap, the bot does the rest).
func TestResidentClaimFlow(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE scope='club' AND key IN ('bs_crm','bs_slots')`); err != nil {
		t.Fatal(err)
	}
	repo := pg.NewPlatformRepo(e.db)
	botRepo := pg.NewBotRepo(e.db)
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
	var mu sync.Mutex
	var linked, added, edits []string
	claims.Link = func(_ context.Context, name string, tg int64) error {
		mu.Lock()
		linked = append(linked, fmt.Sprintf("%s=%d", name, tg))
		mu.Unlock()
		return nil
	}
	claims.Add = func(_ context.Context, name, format string, tg int64) error {
		mu.Lock()
		added = append(added, fmt.Sprintf("%s|%s|%d", name, format, tg))
		mu.Unlock()
		return nil
	}
	claims.Welcome = e.svc.WelcomeResident
	claims.Edit = func(_ context.Context, chat, msg int64, text string, keys map[string]any) error {
		b, _ := json.Marshal(keys)
		mu.Lock()
		edits = append(edits, text+" "+string(b))
		mu.Unlock()
		return nil
	}
	lastEdit := func() string { mu.Lock(); defer mu.Unlock(); return edits[len(edits)-1] }
	e.svc.SetClaimHook(claims.LeadCallback)
	for _, p := range []string{"rcl_", "approve_res_", "reject_res_"} {
		e.svc.SetTeamCallbackHook(p, claims.TeamCallback)
	}
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
	text := func(m map[string]any) string { s, _ := m["text"].(string); return s }
	kbOf := func(m map[string]any) string { b, _ := json.Marshal(m["reply_markup"]); return string(b) }
	// waitFor: the first message to chat (after skip of them) whose text has sub.
	// Matching by text, not by count: the onboarding may add its own day 1.
	waitFor := func(chat int64, skip int, sub string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			s := sentTo(chat)
			for i := skip; i < len(s); i++ {
				if strings.Contains(text(s[i]), sub) {
					return s[i]
				}
			}
			time.Sleep(40 * time.Millisecond)
		}
		var got []string
		for _, m := range sentTo(chat) {
			got = append(got, clip(text(m), 80))
		}
		t.Fatalf("chat %d: no message with %q after %d; got %q", chat, sub, skip, got)
		return nil
	}
	uid := 7000
	pressFull := func(from int64, first, last, user, data string) {
		uid++
		e.hook(fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"cb%d","from":{"id":%d,"first_name":%q,"last_name":%q,"username":%q},"data":%q,"message":{"message_id":%d,"chat":{"id":%d,"type":"private"}}}}`,
			uid, uid, from, first, last, user, data, uid, from))
	}
	press := func(from int64, first, user, data string) { pressFull(from, first, "", user, data) }
	owner := func(data string) { pressFull(111, "Рустам", "", "", data) }
	card := func(tg int64) map[string]any {
		d, _ := repo.GetDoc(ctx, "club", "bs_crm")
		var crm map[string]any
		_ = json.Unmarshal([]byte(d.Value), &crm)
		leads, _ := crm["leads"].([]any)
		return findLeadByTg(leads, tg)
	}
	claimOf := func(tg int64) map[string]any { m, _ := card(tg)["claim"].(map[string]any); return m }

	// 1. Not in the list, at night: the person is told the owner will confirm,
	// the CRM card is written before the answer, nobody is woken up.
	press(777, "айдар", "aidar", "i_am_resident")
	m := waitFor(777, 0, "Передал заявку команде")
	if !strings.Contains(text(m), "Айдар, спасибо") || !strings.Contains(text(m), "app.bxclub.kz") || !strings.Contains(text(m), "один тап") || strings.Contains(text(m), "—") {
		t.Fatalf("answer: %s", text(m))
	}
	if cl := claimOf(777); cl["status"] != "pending" || card(777)["warmStop"] != true || card(777)["source"] != "Бот: «Я резидент BS»" {
		t.Fatalf("crm: %v", card(777))
	}
	if len(sentTo(111)) != 0 {
		t.Fatal("the owner was woken up at night")
	}
	time.Sleep(200 * time.Millisecond)
	e.script.mu.Lock()
	for _, u := range e.script.got {
		if cq, _ := u["callback_query"].(map[string]any); cq != nil && cq["data"] == "i_am_resident" {
			t.Fatal("the script got the claim")
		}
	}
	e.script.mu.Unlock()
	press(777, "Айдар", "aidar", "i_am_resident") // again: no second request
	waitFor(777, 1, "Заявка уже у команды")

	// 2. A resident by Chat ID: «уже резидент»; by username in the reports: linked by itself.
	press(888, "Асет", "", "i_am_resident")
	if m := waitFor(888, 0, "уже резидент"); !strings.Contains(text(m), "один тап") {
		t.Fatal(text(m))
	}
	press(1001, "Aliya", "aliya_k", "i_am_resident")
	waitFor(1001, 0, "Нашёл вас в списке резидентов: Алия Каримова")
	if card(1001)["col"] != "won" || claimOf(1001)["by"] != "auto" {
		t.Fatalf("auto link in crm: %v", card(1001))
	}

	// 3. A resident without a Chat ID by full name, at night: pending with the
	// candidate; at 08:00 the owner gets both claims with one-tap buttons.
	pressFull(1013, "Ерлан", "Сапаров", "", "i_am_resident")
	waitFor(1013, 0, "Передал заявку команде")
	if cl := claimOf(1013); cl["status"] != "pending" || cl["name"] != "Сапаров Ерлан" {
		t.Fatalf("pending: %v", cl)
	}
	if n := claims.NotifyPending(ctx); n != 0 || len(sentTo(111)) != 0 {
		t.Fatalf("night: %d sent, owner got %d", n, len(sentTo(111)))
	}
	setNow(at(0, 8).Add(15 * time.Minute))
	if n := claims.NotifyPending(ctx); n != 2 {
		t.Fatalf("morning: %d", n)
	}
	om := waitFor(111, 0, "Сапаров Ерлан")
	if !strings.Contains(text(om), "говорит, что он резидент") || !strings.Contains(kbOf(om), "rcl_ok_1013") || !strings.Contains(kbOf(om), "rcl_no_1013") ||
		!strings.Contains(kbOf(om), "Подтвердить") || !strings.Contains(kbOf(om), "rcl_new_1013") {
		t.Fatalf("owner message: %s %s", text(om), kbOf(om))
	}
	if om := waitFor(111, 0, "айдар"); !strings.Contains(kbOf(om), "rcl_ok_777") || strings.Contains(kbOf(om), "rcl_new_777") {
		t.Fatalf("owner message 777: %s", kbOf(om))
	}
	if n := claims.NotifyPending(ctx); n != 0 {
		t.Fatal("told twice")
	}

	// 4. «Подтвердить» on a resident row without a Chat ID: linked, the
	// welcome with the platform, the Mini App, the group link, the offer; the
	// onboarding starts; the CRM card is «Резидент».
	owner("rcl_ok_1013")
	w := waitFor(1013, 1, "Добро пожаловать в Business Surgery")
	if k := kbOf(w); !strings.Contains(k, "https://t.me/+personal1") || !strings.Contains(k, "web_app") || !strings.Contains(k, "app.bxclub.kz") ||
		!strings.Contains(text(w), "Войти через Telegram") || strings.Contains(text(w), "—") {
		t.Fatalf("welcome: %s %s", text(w), k)
	}
	if !strings.Contains(kbOf(waitFor(1013, 1, "Публичная оферта")), "accept_terms") {
		t.Fatal("offer")
	}
	if v, _ := botRepo.GetMeta(ctx, "onb:1013"); v == "" {
		t.Fatal("onboarding did not start")
	}
	if cl := claimOf(1013); cl["status"] != "resident" || cl["by"] != "Рустам" || cl["welcome"] == nil || card(1013)["col"] != "won" {
		t.Fatalf("crm after confirm: %v", card(1013))
	}
	mu.Lock()
	if strings.Join(linked, ",") != "Алия Каримова=1001,Сапаров Ерлан=1013" || len(added) != 0 {
		t.Fatalf("linked %v added %v", linked, added)
	}
	mu.Unlock()
	if !strings.Contains(lastEdit(), "Сапаров Ерлан") || !strings.Contains(lastEdit(), "Рустам") {
		t.Fatalf("owner message after: %s", lastEdit())
	}
	if res, _ := claims.Confirm(ctx, 1013, "", "Рустам", false); !strings.Contains(res, "Уже подтверждён") {
		t.Fatalf("twice: %s", res)
	}

	// 5. «Подтвердить» on someone not in the list: the format is the second
	// tap, then the resident is created and welcomed.
	owner("rcl_ok_777")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(lastEdit(), "rcl_on_777") {
		time.Sleep(40 * time.Millisecond)
	}
	if !strings.Contains(lastEdit(), "rcl_on_777") || !strings.Contains(lastEdit(), "rcl_off_777") {
		t.Fatalf("format question: %s", lastEdit())
	}
	owner("rcl_on_777")
	waitFor(777, 2, "Добро пожаловать в Business Surgery, Айдар")
	mu.Lock()
	if strings.Join(added, ",") != "Айдар|Онлайн|777" {
		t.Fatalf("added: %v", added)
	}
	mu.Unlock()
	if card(777)["col"] != "won" {
		t.Fatalf("777 card: %v", card(777))
	}

	// 6. «Отклонить»: the warm offer and the 3 touches, as before.
	setNow(at(0, 11))
	pressFull(1020, "Иван", "Петров", "ivan", "i_am_resident")
	waitFor(1020, 0, "Передал заявку команде")
	owner("reject_res_1020") // the script's old button goes the same way
	if m := waitFor(1020, 1, "не нашёл"); !strings.Contains(text(m), "Иван, спасибо") || !strings.Contains(text(m), "50 000 ₸") ||
		!strings.Contains(kbOf(m), "?p=razbor") || !strings.Contains(kbOf(m), "claim_recheck") {
		t.Fatalf("offer: %s %s", text(m), kbOf(m))
	}
	if cl := claimOf(1020); cl["status"] != claimLead || cl["by"] != "Рустам" || card(1020)["warmStop"] != nil {
		t.Fatalf("after «Отклонить»: %v", card(1020))
	}
	slotAt := at(5, 11)
	sb, _ := json.Marshal(map[string]any{"slots": []any{map[string]any{"id": "s9", "start": slotAt.Format(time.RFC3339), "dur": 60, "format": "Онлайн", "status": "free"}}})
	if _, err := repo.PutDoc(ctx, "club", "bs_slots", 0, string(sb), false, "test"); err != nil {
		t.Fatal(err)
	}
	setNow(at(1, 3)) // night: nothing
	if n := f.WarmOnce(ctx); n != 0 {
		t.Fatalf("night warm: %d", n)
	}
	n1020 := len(sentTo(1020))
	setNow(at(1, 12))
	if n := f.WarmOnce(ctx); n < 1 {
		t.Fatalf("day 1: %d", n)
	}
	waitFor(1020, n1020, "Даурен")
	setNow(at(3, 12))
	f.WarmOnce(ctx)
	if last := text(waitFor(1020, n1020+1, "ближайшее свободное время")); !strings.Contains(last, whenRu(slotAt)) {
		t.Fatalf("day 3 slot: %s", last)
	}

	// 7. «Я уже в клубе» under the offer: back to the owner, one tap.
	setNow(at(4, 11))
	nOwner := len(sentTo(111))
	press(1020, "Иван", "ivan", "claim_recheck")
	if om := waitFor(111, nOwner, "говорит, что он резидент"); !strings.Contains(kbOf(om), "rcl_ok_1020") || claimOf(1020)["status"] != "pending" {
		t.Fatalf("recheck: %s", kbOf(om))
	}
	// the CRM strip without a pick confirms the same way (the format from the booked slot)
	if c := card(1020); c != nil {
		_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
			leads, _ := crm["leads"].([]any)
			findLeadByTg(leads, 1020)["razborSlot"] = "s9"
			return true
		})
	}
	if res, err := claims.Resolve(ctx, 1020, "resident", "", "Рустам"); err != nil || !strings.Contains(res, "онлайн") {
		t.Fatalf("strip confirm: %v %v", res, err)
	}

	// 8. A claim of the last day that went the old CRM way: re-sent once, not confirmed.
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		crm["leads"] = append(leads, map[string]any{"id": "tg2001", "tgId": float64(2001), "col": "new", "name": "Амир Тестов",
			"claim": map[string]any{"status": claimLead, "at": f.now().Add(-3 * time.Hour).UTC().Format(time.RFC3339), "why": "нет в списке резидентов"}})
		return true
	})
	nOwner = len(sentTo(111))
	if names := claims.ResendRecent(ctx, 30*time.Hour); strings.Join(names, ",") != "Амир Тестов" {
		t.Fatalf("resend: %v", names)
	}
	if om := waitFor(111, nOwner, "Амир Тестов"); !strings.Contains(kbOf(om), "rcl_ok_2001") || claimOf(2001)["status"] != "pending" {
		t.Fatalf("resend message: %s", kbOf(om))
	}
	if names := claims.ResendRecent(ctx, 30*time.Hour); len(names) != 0 {
		t.Fatalf("resent twice: %v", names)
	}

	// 9. The script's own path (old button, direct app call): signed.
	g := NewAppGateway(testBotToken, "http://127.0.0.1:1/exec")
	g.Claims = claims
	NewAppGatewayModule(g).Register(e.router)
	resp := e.signedPost("/api/v1/bot/claim", map[string]any{"chatId": "1004", "name": "Иван Петров", "username": "ivan2", "via": "app"})
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"kind":"lead"`) {
		t.Fatalf("script claim: %d %s", resp.Code, resp.Body.String())
	}
	waitFor(1004, 0, "Передал заявку команде")
	if card(1004)["source"] != "Приложение: «Я резидент BS»" {
		t.Fatalf("script claim card: %v", card(1004))
	}
	if r := e.do("POST", "/api/v1/bot/claim", []byte(`{"chatId":"1"}`), map[string]string{"X-BS-Signature": "00"}); r.Code != 401 {
		t.Fatalf("unsigned: %d", r.Code)
	}
	resp = e.signedPost("/api/v1/bot/claim", map[string]any{"chatId": "1005", "action": "lead"})
	if resp.Code != 200 {
		t.Fatalf("old «Отклонить»: %d %s", resp.Code, resp.Body.String())
	}
	waitFor(1005, 0, "экспресс-разбор")
}
