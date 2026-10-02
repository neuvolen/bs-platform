package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

func TestLeadFunnel(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform_docs WHERE key IN ('bs_crm','bs_ck_stats')`,
		`DELETE FROM club_residents WHERE tg_id IN (888)`,
		`INSERT INTO club_residents (name, tg_id) VALUES ('Резидент Тест', 888)`,
	} {
		if _, err := e.db.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := pg.NewPlatformRepo(e.db)
	f := NewLeadFunnel(repo, e.svc.SendMessageKB, []int64{111})
	e.svc.SetStartHook(f.HandleStart)
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
	start := func(id, from int64, text string) {
		e.hook(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"first_name":"Айдар","username":"aidar"},"text":%q}}`,
			id, id, time.Now().Unix(), from, from, text))
	}
	start(5001, 777, "/start threads")
	start(5002, 111, "/start")       // admin: the script
	start(5003, 888, "/start")       // a resident: the script
	start(5004, 999, "/start ref_5") // a referral: the script
	e.waitRelayed(3, 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	e.script.mu.Lock()
	for _, u := range e.script.got {
		if int64(u["update_id"].(float64)) == 5001 {
			t.Fatal("a lead's /start must not reach the script")
		}
	}
	e.script.mu.Unlock()

	w := sentTo(777)
	if len(w) != 1 || !strings.Contains(w[0]["text"].(string), "99 гайдов") {
		t.Fatalf("welcome: %v", w)
	}
	kbs, _ := json.Marshal(w[0]["reply_markup"])
	if !strings.Contains(string(kbs), `?p=checklists`) || !strings.Contains(string(kbs), "i_am_resident") {
		t.Fatalf("buttons: %s", kbs)
	}
	if a := sentTo(111); len(a) != 1 || !strings.Contains(a[0]["text"].(string), "Threads") {
		t.Fatalf("admin note: %v", a)
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_crm")
	if d == nil || !strings.Contains(d.Value, `"tgId":777`) || !strings.Contains(d.Value, `"source":"Threads"`) || !strings.Contains(d.Value, `"col":"new"`) {
		t.Fatalf("crm: %+v", d)
	}
	// /start again within a minute: no second welcome
	start(5005, 777, "/start")
	time.Sleep(800 * time.Millisecond)
	if len(sentTo(777)) != 1 {
		t.Fatalf("repeat start answered: %d", len(sentTo(777)))
	}

	// Checklist progress: a lead finishes one and is invited, once.
	r := ckReport{ID: "c007", Title: "Платёжный календарь", Organ: "Финансы", Done: 9, Total: 10}
	if err := f.Progress(ctx, 777, "Айдар", "aidar", "lead", "", r); err != nil {
		t.Fatal(err)
	}
	if len(sentTo(777)) != 1 {
		t.Fatal("nudged before finishing")
	}
	r.Done = 10
	_ = f.Progress(ctx, 777, "Айдар", "aidar", "lead", "", r)
	_ = f.Progress(ctx, 777, "Айдар", "aidar", "lead", "", r)
	got := sentTo(777)
	if len(got) != 2 || !strings.Contains(got[1]["text"].(string), "Платёжный календарь") {
		t.Fatalf("nudge: %v", got)
	}
	_ = f.Progress(ctx, 888, "Резидент", "", "resident", "Резидент Тест", ckReport{ID: "c010", Title: "Найм", Organ: "Команда", Done: 3, Total: 8})
	st, _ := repo.GetDoc(ctx, "club", "bs_ck_stats")
	if st == nil || !strings.Contains(st.Value, `"resident":"Резидент Тест"`) || !strings.Contains(st.Value, `"finished"`) {
		t.Fatalf("stats: %+v", st)
	}
	d, _ = repo.GetDoc(ctx, "club", "bs_crm")
	if !strings.Contains(d.Value, `"hot":true`) || !strings.Contains(d.Value, `"done":["c007"]`) {
		t.Fatalf("crm ck: %s", d.Value)
	}

	// Warm-up: a day later at noon the first touch, then nothing until day 3.
	base := time.Now()
	noon := func(days float64) time.Time {
		x := base.Add(time.Duration(days*24) * time.Hour).In(almaty)
		return time.Date(x.Year(), x.Month(), x.Day(), 12, 0, 0, 0, almaty)
	}
	f.now = func() time.Time { return noon(2) }
	if n := f.WarmOnce(ctx); n != 1 {
		t.Fatalf("warm day 1: %d", n)
	}
	if n := f.WarmOnce(ctx); n != 0 {
		t.Fatalf("warm repeated: %d", n)
	}
	got = sentTo(777)
	if !strings.Contains(got[len(got)-1]["text"].(string), "Платёжный календарь") {
		t.Fatalf("warm text should know the checklist: %v", got[len(got)-1]["text"])
	}
	f.now = func() time.Time { return noon(2).Add(10 * time.Hour) } // 22:00: quiet hours
	f.now = func() time.Time { x := noon(4); return x.Add(10 * time.Hour) }
	if n := f.WarmOnce(ctx); n != 0 {
		t.Fatal("warm at night")
	}
	f.now = func() time.Time { return noon(4) }
	if n := f.WarmOnce(ctx); n != 1 {
		t.Fatal("warm day 3")
	}
}
